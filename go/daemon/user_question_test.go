package daemon

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAskUserBlocksUntilStructuredAnswerAndAuditsLifecycle(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "choose")

	questions := make(chan map[string]any, 1)
	d.events.Tap(func(_ string, ev map[string]any) {
		if ev["type"] == "user.question" {
			questions <- ev
		}
	})
	result := make(chan string, 1)
	go func() {
		result <- d.askUser(sess, task, "Which approach?", []userQuestionOption{
			{Label: "Minimal fix", Value: "minimal"},
			{Label: "Refactor", Value: "refactor"},
		})
	}()

	var question map[string]any
	select {
	case question = <-questions:
	case <-time.After(2 * time.Second):
		t.Fatal("user.question was not published")
	}
	questionID, _ := question["question_id"].(string)
	if questionID == "" || question["prompt"] != "Which approach?" {
		t.Fatalf("invalid question envelope: %+v", question)
	}
	if got, _ := d.sched.Get(task.TaskID); got.Status != "waiting_input" {
		t.Fatalf("task status = %s, want waiting_input", got.Status)
	}
	if _, err := d.handleUserAnswer(mustJSON(t, map[string]any{
		"question_id": questionID, "value": "unknown",
	})); err == nil {
		t.Fatal("invalid option was accepted")
	}
	if _, err := d.handleUserAnswer(mustJSON(t, map[string]any{
		"question_id": questionID, "value": "minimal",
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case observation := <-result:
		if !strings.Contains(observation, "Minimal fix") {
			t.Fatalf("observation = %q", observation)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask_user did not resume")
	}

	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	log := string(raw)
	if !strings.Contains(log, "user_question_requested") || !strings.Contains(log, "user_question_resolved") {
		t.Fatalf("question lifecycle missing from audit: %s", log)
	}
}

func TestAgentLoopUsesStructuredUserAnswer(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	reasoner := &promptRecordingReasoner{steps: []string{
		`{"tool":"ask_user","prompt":"Choose mode","options":[{"label":"Safe","value":"safe"},{"label":"Fast","value":"fast"}]}`,
		`{"tool":"done","summary":"used safe mode"}`,
	}}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "choose")

	questions := make(chan map[string]any, 1)
	d.events.Tap(func(_ string, ev map[string]any) {
		if ev["type"] == "user.question" {
			questions <- ev
		}
	})
	done := make(chan struct{})
	go func() {
		d.runTask(sess, task)
		close(done)
	}()
	question := <-questions
	if _, err := d.handleUserAnswer(mustJSON(t, map[string]any{
		"question_id": question["question_id"], "value": "safe",
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not finish after user answer")
	}
	if len(reasoner.prompts) < 2 || !strings.Contains(reasoner.prompts[1], "value: safe") {
		t.Fatalf("answer missing from next prompt: %+v", reasoner.prompts)
	}
	if got, _ := d.sched.Get(task.TaskID); got.Status != "completed" {
		t.Fatalf("task status = %s, want completed", got.Status)
	}
}

func TestAskUserCancellationDoesNotRestoreRunningStatus(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "choose")

	questions := make(chan map[string]any, 1)
	d.events.Tap(func(_ string, ev map[string]any) {
		if ev["type"] == "user.question" {
			questions <- ev
		}
	})
	result := make(chan toolExecutionOutcome, 1)
	go d.withTaskContext(task.TaskID, func(context.Context) {
		result <- d.askUserOutcome(sess, task, "Which approach?", []userQuestionOption{
			{Label: "Minimal fix", Value: "minimal"},
			{Label: "Refactor", Value: "refactor"},
		})
	})
	var questionID string
	select {
	case question := <-questions:
		questionID, _ = question["question_id"].(string)
	case <-time.After(2 * time.Second):
		t.Fatal("user.question was not published")
	}
	if _, err := d.handleTaskCancel(mustJSON(t, map[string]any{"task_id": task.TaskID})); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-result:
		if outcome.status != "cancelled" {
			t.Fatalf("outcome = %#v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask_user did not stop after cancellation")
	}
	current, _ := d.sched.Get(task.TaskID)
	if current.Status != "cancelled" {
		t.Fatalf("task status = %s, want cancelled", current.Status)
	}
	if _, err := d.handleUserAnswer(mustJSON(t, map[string]any{
		"question_id": questionID, "value": "minimal",
	})); err == nil {
		t.Fatal("late answer was accepted after cancellation")
	}
}

func TestNormalizeUserQuestionRejectsInvalidShape(t *testing.T) {
	_, _, err := normalizeUserQuestion("", nil)
	if err == nil {
		t.Fatal("empty question was accepted")
	}
	_, _, err = normalizeUserQuestion("choose", []userQuestionOption{{Label: "One", Value: "same"}, {Label: "Two", Value: "same"}})
	if err == nil {
		t.Fatal("duplicate option value was accepted")
	}
}

func TestPendingUserQuestionsReportsOnlyLiveWaiters(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	d.questionMu.Lock()
	d.pendingQuestions["question_b"] = &pendingUserQuestion{}
	d.pendingQuestions["question_a"] = &pendingUserQuestion{}
	d.questionMu.Unlock()

	result, err := d.handlePendingUserQuestions(nil)
	if err != nil {
		t.Fatal(err)
	}
	got := result.(map[string]any)["question_ids"].([]string)
	if len(got) != 2 || got[0] != "question_a" || got[1] != "question_b" {
		t.Fatalf("pending question ids = %#v", got)
	}
}
