package tui

import "strings"

type conversationReadiness int

const (
	readinessChecking conversationReadiness = iota
	readinessReady
	readinessBlocked
	readinessUnavailable
)

type conversationActivity int

const (
	activityIdle conversationActivity = iota
	activitySubmitting
	activityRunning
	activityWaitingApproval
	activityWaitingQuestion
	activityInterrupted
)

type conversationOutcome int

const (
	outcomeNone conversationOutcome = iota
	outcomeCompleted
	outcomeDegraded
	outcomeFailed
	outcomeCancelled
)

func normalizeConversationOutcome(status string) conversationOutcome {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "succeeded", "success":
		return outcomeCompleted
	case "degraded":
		return outcomeDegraded
	case "failed", "failure", "aborted", "denied":
		return outcomeFailed
	case "cancelled", "canceled":
		return outcomeCancelled
	default:
		return outcomeNone
	}
}

func (o conversationOutcome) taskStatus() string {
	switch o {
	case outcomeCompleted:
		return "completed"
	case outcomeDegraded:
		return "degraded"
	case outcomeFailed:
		return "failed"
	case outcomeCancelled:
		return "cancelled"
	default:
		return ""
	}
}

type conversationEvidence struct {
	SessionID    string
	ActiveTaskID string
	TerminalID   string
	EventType    string
	Status       string
	DecisionID   string
	QuestionID   string
}

type conversationProjection struct {
	Readiness conversationReadiness
	Activity  conversationActivity
	Outcome   conversationOutcome
	Evidence  conversationEvidence
}

type conversationTransitionKind int

const (
	transitionReset conversationTransitionKind = iota
	transitionConnected
	transitionUnavailable
	transitionReadiness
	transitionIdle
	transitionSubmitting
	transitionRunning
	transitionWaitingApproval
	transitionWaitingQuestion
	transitionApprovalResolved
	transitionQuestionResolved
	transitionInterrupted
	transitionTerminal
)

type conversationTransition struct {
	Kind       conversationTransitionKind
	SessionID  string
	TaskID     string
	EventType  string
	Status     string
	DecisionID string
	QuestionID string
	Outcome    conversationOutcome
	Readiness  conversationReadiness
}

func (p *conversationProjection) reduce(t conversationTransition) bool {
	switch t.Kind {
	case transitionReset:
		*p = conversationProjection{Readiness: readinessChecking}
		p.Evidence.SessionID = t.SessionID
	case transitionConnected:
		p.Readiness = readinessChecking
		if t.SessionID != "" {
			p.Evidence.SessionID = t.SessionID
		}
	case transitionUnavailable:
		p.Readiness = readinessUnavailable
	case transitionReadiness:
		p.Readiness = t.Readiness
	case transitionIdle:
		if p.Evidence.ActiveTaskID != "" && t.TaskID != "" && t.TaskID != p.Evidence.ActiveTaskID {
			return false
		}
		p.Activity = activityIdle
		p.Evidence.ActiveTaskID = ""
	case transitionSubmitting:
		p.Activity = activitySubmitting
		p.Outcome = outcomeNone
		p.Evidence.ActiveTaskID = t.TaskID
		p.Evidence.TerminalID = ""
	case transitionRunning:
		p.Activity = activityRunning
		p.Outcome = outcomeNone
		p.Evidence.ActiveTaskID = t.TaskID
		p.Evidence.TerminalID = ""
	case transitionWaitingApproval:
		p.Activity = activityWaitingApproval
		p.Outcome = outcomeNone
		if t.TaskID != "" {
			p.Evidence.ActiveTaskID = t.TaskID
		}
		p.Evidence.DecisionID = t.DecisionID
		p.Evidence.QuestionID = ""
	case transitionWaitingQuestion:
		p.Activity = activityWaitingQuestion
		p.Outcome = outcomeNone
		if t.TaskID != "" {
			p.Evidence.ActiveTaskID = t.TaskID
		}
		p.Evidence.QuestionID = t.QuestionID
		p.Evidence.DecisionID = ""
	case transitionApprovalResolved:
		if p.Evidence.DecisionID != "" && t.DecisionID != p.Evidence.DecisionID {
			return false
		}
		p.Evidence.DecisionID = ""
		if p.Activity == activityWaitingApproval {
			p.resumeAfterDecision()
		}
	case transitionQuestionResolved:
		if p.Evidence.QuestionID != "" && t.QuestionID != p.Evidence.QuestionID {
			return false
		}
		p.Evidence.QuestionID = ""
		if p.Activity == activityWaitingQuestion {
			p.resumeAfterDecision()
		}
	case transitionInterrupted:
		if p.Evidence.ActiveTaskID != "" && t.TaskID != "" && t.TaskID != p.Evidence.ActiveTaskID {
			return false
		}
		p.Activity = activityInterrupted
		p.Outcome = outcomeNone
		if t.TaskID != "" {
			p.Evidence.ActiveTaskID = t.TaskID
		}
	case transitionTerminal:
		if p.Evidence.ActiveTaskID != "" && t.TaskID != "" && t.TaskID != p.Evidence.ActiveTaskID {
			return false
		}
		if t.Outcome == outcomeNone {
			return false
		}
		p.Activity = activityIdle
		p.Outcome = t.Outcome
		p.Evidence.TerminalID = t.TaskID
		p.Evidence.ActiveTaskID = ""
		p.Evidence.DecisionID = ""
		p.Evidence.QuestionID = ""
	default:
		return false
	}
	p.Evidence.EventType = t.EventType
	p.Evidence.Status = strings.ToLower(strings.TrimSpace(t.Status))
	return true
}

func (p *conversationProjection) resumeAfterDecision() {
	if p.Evidence.ActiveTaskID != "" {
		p.Activity = activityRunning
		return
	}
	p.Activity = activityIdle
}

func eventTaskID(ev, payload map[string]any) string {
	if id := str(ev["task_id"]); id != "" {
		return id
	}
	return str(payload["task_id"])
}

func eventStatus(ev, payload map[string]any) string {
	if status := firstValue(ev, "status", "outcome"); status != "" {
		return status
	}
	return firstValue(payload, "status", "outcome")
}

func conversationTransitionForEvent(ev map[string]any) (conversationTransition, bool) {
	typ := str(ev["type"])
	payload, _ := ev["payload"].(map[string]any)
	taskID := eventTaskID(ev, payload)
	status := strings.ToLower(strings.TrimSpace(eventStatus(ev, payload)))
	base := conversationTransition{TaskID: taskID, EventType: typ, Status: status}
	if status == "approval_resolved" {
		base.Kind = transitionApprovalResolved
		base.DecisionID = firstValue(payload, "decision_id", "permission_decision_id")
		if base.DecisionID == "" {
			base.DecisionID = firstValue(ev, "decision_id", "permission_decision_id")
		}
		return base, true
	}
	if status == "user_question_resolved" {
		base.Kind = transitionQuestionResolved
		base.QuestionID = firstValue(payload, "question_id")
		if base.QuestionID == "" {
			base.QuestionID = firstValue(ev, "question_id")
		}
		return base, true
	}

	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "permission.request":
		base.Kind = transitionWaitingApproval
		base.DecisionID = firstValue(ev, "decision_id", "permission_decision_id")
		return base, true
	case "user.question":
		base.Kind = transitionWaitingQuestion
		base.QuestionID = firstValue(ev, "question_id")
		return base, true
	case "task.completed", "taskcomplete", "taskcompleted", "task.failed", "task.cancelled", "task.canceled":
		if status == "interrupted" {
			base.Kind = transitionInterrupted
			return base, true
		}
		base.Kind = transitionTerminal
		base.Outcome = normalizeConversationOutcome(status)
		if base.Outcome == outcomeNone {
			switch strings.ToLower(strings.TrimSpace(typ)) {
			case "task.failed":
				base.Outcome = outcomeFailed
			case "task.cancelled", "task.canceled":
				base.Outcome = outcomeCancelled
			default:
				base.Outcome = outcomeCompleted
			}
		}
		return base, true
	case "taskfailed":
		base.Kind, base.Outcome = transitionTerminal, outcomeFailed
		return base, true
	case "taskcancelled", "taskcanceled":
		base.Kind, base.Outcome = transitionTerminal, outcomeCancelled
		return base, true
	case "taskinterrupted":
		base.Kind = transitionInterrupted
		return base, true
	case "taskcreated":
		if str(payload["workflow"]) != "" || strings.HasPrefix(status, "workflow_") {
			return conversationTransition{}, false
		}
		if outcome := normalizeConversationOutcome(status); outcome != outcomeNone {
			base.Kind, base.Outcome = transitionTerminal, outcome
			return base, true
		}
		switch {
		case status == "interrupted":
			base.Kind = transitionInterrupted
		case strings.Contains(status, "approval"):
			base.Kind = transitionWaitingApproval
			base.DecisionID = firstValue(payload, "decision_id", "permission_decision_id")
		case strings.Contains(status, "question"):
			base.Kind = transitionWaitingQuestion
			base.QuestionID = firstValue(payload, "question_id")
		case status == "", status == "queued", status == "running":
			base.Kind = transitionRunning
		default:
			return conversationTransition{}, false
		}
		return base, true
	}

	return conversationTransition{}, false
}

func terminalConversationEvent(ev map[string]any) (conversationOutcome, bool) {
	t, ok := conversationTransitionForEvent(ev)
	return t.Outcome, ok && t.Kind == transitionTerminal
}

func (m *Model) applyConversation(t conversationTransition) bool {
	switch t.Kind {
	case transitionReset, transitionSubmitting, transitionRunning, transitionWaitingApproval,
		transitionWaitingQuestion, transitionInterrupted, transitionTerminal:
		m.clearOperationalNotice()
	}
	if !m.conversation.reduce(t) {
		return false
	}
	m.tasks.observeConversation(m.conversation)
	return true
}

func (m *Model) reduceConversationEvent(ev map[string]any) {
	if t, ok := conversationTransitionForEvent(ev); ok {
		m.applyConversation(t)
	}
}

func (m *Model) syncConversationGovernance() {
	if m.approval != nil {
		m.applyConversation(conversationTransition{
			Kind: transitionWaitingApproval, TaskID: m.inFlightTaskID,
			DecisionID: m.approval.DecisionID, EventType: "permission.request",
		})
		return
	}
	if m.question != nil {
		m.applyConversation(conversationTransition{
			Kind: transitionWaitingQuestion, TaskID: m.question.TaskID,
			QuestionID: m.question.QuestionID, EventType: "user.question",
		})
	}
}

func (m *Model) syncConversationLocalActivity(eventType string) {
	if m.approval != nil || m.question != nil {
		m.syncConversationGovernance()
		return
	}
	if m.submitting != nil {
		m.applyConversation(conversationTransition{Kind: transitionSubmitting, TaskID: m.inFlightTaskID, EventType: eventType})
		return
	}
	if m.inFlightTaskID != "" {
		m.applyConversation(conversationTransition{Kind: transitionRunning, TaskID: m.inFlightTaskID, EventType: eventType, Status: "running"})
		return
	}
	m.applyConversation(conversationTransition{Kind: transitionIdle, EventType: eventType})
}

func (m *Model) conversationSnapshot() conversationProjection {
	p := m.conversation
	if m.approval != nil {
		p.Activity = activityWaitingApproval
		p.Evidence.DecisionID = m.approval.DecisionID
		return p
	}
	if m.question != nil {
		p.Activity = activityWaitingQuestion
		p.Evidence.QuestionID = m.question.QuestionID
		if m.question.TaskID != "" {
			p.Evidence.ActiveTaskID = m.question.TaskID
		}
		return p
	}
	if m.submitting != nil {
		p.Activity = activitySubmitting
		return p
	}
	if m.inFlightTaskID != "" {
		p.Activity = activityRunning
		p.Outcome = outcomeNone
		p.Evidence.ActiveTaskID = m.inFlightTaskID
	}
	return p
}
