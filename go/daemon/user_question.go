package daemon

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	maxUserQuestionOptions = 6
	maxUserQuestionPrompt  = 500
)

type userQuestionOption struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type userQuestionAnswer struct {
	Value string
}

type pendingUserQuestion struct {
	mu        sync.Mutex
	resolved  bool
	sessionID string
	taskID    string
	options   map[string]userQuestionOption
	answer    chan userQuestionAnswer
}

func normalizeUserQuestion(prompt string, options []userQuestionOption) (string, []userQuestionOption, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", nil, fmt.Errorf("ask_user requires a prompt")
	}
	if len([]rune(prompt)) > maxUserQuestionPrompt {
		return "", nil, fmt.Errorf("ask_user prompt exceeds %d characters", maxUserQuestionPrompt)
	}
	if len(options) < 2 || len(options) > maxUserQuestionOptions {
		return "", nil, fmt.Errorf("ask_user requires 2-%d options", maxUserQuestionOptions)
	}
	seen := make(map[string]bool, len(options))
	normalized := make([]userQuestionOption, 0, len(options))
	for _, option := range options {
		option.Label = strings.TrimSpace(option.Label)
		option.Value = strings.TrimSpace(option.Value)
		option.Description = strings.TrimSpace(option.Description)
		if option.Label == "" || option.Value == "" {
			return "", nil, fmt.Errorf("ask_user option label and value are required")
		}
		if seen[option.Value] {
			return "", nil, fmt.Errorf("ask_user option value %q is duplicated", option.Value)
		}
		seen[option.Value] = true
		normalized = append(normalized, option)
	}
	return prompt, normalized, nil
}

func (d *Daemon) askUser(sess *sessionstore.Session, task *scheduler.Task, prompt string, options []userQuestionOption) string {
	return d.askUserOutcome(sess, task, prompt, options).display
}

func (d *Daemon) askUserOutcome(sess *sessionstore.Session, task *scheduler.Task, prompt string, options []userQuestionOption) toolExecutionOutcome {
	prompt, options, err := normalizeUserQuestion(prompt, options)
	if err != nil {
		return toolFailed("ask_user error: "+err.Error(), "invalid_input")
	}
	if err := d.ensureActiveToolStarted(task.TaskID); err != nil {
		return toolFailed("governance error: "+err.Error(), "audit_persistence_error")
	}
	questionID := sessionstore.NewID("question")
	optionMap := make(map[string]userQuestionOption, len(options))
	for _, option := range options {
		optionMap[option.Value] = option
	}
	pending := &pendingUserQuestion{
		sessionID: sess.SessionID,
		taskID:    task.TaskID,
		options:   optionMap,
		answer:    make(chan userQuestionAnswer, 1),
	}
	d.questionMu.Lock()
	if d.pendingQuestions == nil {
		d.pendingQuestions = make(map[string]*pendingUserQuestion)
	}
	d.pendingQuestions[questionID] = pending
	d.questionMu.Unlock()
	defer func() {
		d.questionMu.Lock()
		delete(d.pendingQuestions, questionID)
		d.questionMu.Unlock()
	}()

	d.sched.SetStatus(task.TaskID, "waiting_input")
	ev := map[string]any{
		"type":        "user.question",
		"session_id":  sess.SessionID,
		"task_id":     task.TaskID,
		"question_id": questionID,
		"prompt":      prompt,
		"options":     options,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
	}
	if err := d.kern.RecordEvent(sess.SessionID, "ToolRequested", task.TaskID, "go", map[string]any{
		"status": "user_question_requested", "question_id": questionID, "request": ev,
	}, ""); err != nil {
		d.sched.SetStatus(task.TaskID, "running")
		return toolFailed("ask_user error: persist request: "+err.Error(), "audit_persistence_error")
	}
	d.events.Publish(sess.SessionID, ev)

	timeout := d.approvalTimeout
	if timeout <= 0 {
		timeout = defaultApprovalTimeout
	}
	ctx := d.contextForTask(task.TaskID)
	var answer userQuestionAnswer
	timedOut := false
	cancelled := false
	select {
	case answer = <-pending.answer:
	case <-ctx.Done():
		cancelled = true
	case <-time.After(timeout):
		timedOut = true
	case <-d.stopCh:
		cancelled = true
	}
	if ctx.Err() != nil || taskCancelled(d, task.TaskID) {
		cancelled = true
	}
	pending.mu.Lock()
	pending.resolved = true
	pending.mu.Unlock()
	if cancelled {
		d.record(sess.SessionID, "TaskCreated", task.TaskID, "operator", map[string]any{
			"status": "user_question_resolved", "question_id": questionID, "cancelled": true,
		}, "")
		return toolExecutionOutcome{display: "User question cancelled.", status: "cancelled", errorCategory: "operator_cancelled"}
	}
	d.sched.SetStatus(task.TaskID, "running")
	payload := map[string]any{
		"status": "user_question_resolved", "question_id": questionID,
		"value": answer.Value, "timed_out": timedOut,
	}
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "operator", payload, "")
	if timedOut {
		return toolTimedOut("User did not answer the structured question before it expired. Continue with the safest reversible option or ask again if the choice is required.")
	}
	option := optionMap[answer.Value]
	return toolCompleted(fmt.Sprintf("User selected %q (value: %s).", option.Label, option.Value))
}

func (d *Daemon) handleUserAnswer(params json.RawMessage) (any, error) {
	var p struct {
		QuestionID string `json:"question_id"`
		Value      string `json:"value"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.QuestionID = strings.TrimSpace(p.QuestionID)
	p.Value = strings.TrimSpace(p.Value)
	if p.QuestionID == "" || p.Value == "" {
		return nil, fmt.Errorf("question_id and value are required")
	}
	d.questionMu.Lock()
	pending := d.pendingQuestions[p.QuestionID]
	d.questionMu.Unlock()
	if pending == nil {
		return nil, fmt.Errorf("no pending user question %s", p.QuestionID)
	}
	if _, ok := pending.options[p.Value]; !ok {
		return nil, fmt.Errorf("invalid answer %q for question %s", p.Value, p.QuestionID)
	}
	pending.mu.Lock()
	defer pending.mu.Unlock()
	if pending.resolved {
		return nil, fmt.Errorf("user question %s is already resolved", p.QuestionID)
	}
	select {
	case pending.answer <- userQuestionAnswer{Value: p.Value}:
		pending.resolved = true
	default:
		return nil, fmt.Errorf("user question %s is already resolved", p.QuestionID)
	}
	return map[string]any{"question_id": p.QuestionID, "accepted": true, "value": p.Value}, nil
}

func (d *Daemon) handlePendingUserQuestions(_ json.RawMessage) (any, error) {
	d.questionMu.Lock()
	questionIDs := make([]string, 0, len(d.pendingQuestions))
	for questionID := range d.pendingQuestions {
		questionIDs = append(questionIDs, questionID)
	}
	d.questionMu.Unlock()
	sort.Strings(questionIDs)
	return map[string]any{"question_ids": questionIDs}, nil
}
