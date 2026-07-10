package tui

import (
	"strings"
	"testing"
)

func userQuestionEvent(id string) EventMsg {
	return EventMsg{Raw: map[string]any{
		"type":        "user.question",
		"session_id":  "sess_test",
		"task_id":     "tsk_1",
		"question_id": id,
		"prompt":      "Which target should Carina update?",
		"options": []any{
			map[string]any{"label": "Production", "value": "prod", "description": "deploy the release"},
			map[string]any{"label": "Staging", "value": "stage"},
		},
	}}
}

func TestUserQuestionNumberAnswersStructuredRPC(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.user.answer": nil}}
	m, _ := newTestModel(fc)
	m.Update(userQuestionEvent("q_1"))
	if m.question == nil || m.question.QuestionID != "q_1" {
		t.Fatalf("question overlay = %+v, want q_1", m.question)
	}
	if !strings.Contains(m.questionOverlayView(), "[2] Staging") {
		t.Fatalf("question options not rendered:\n%s", m.questionOverlayView())
	}

	cmd, handled := m.handleKey("2")
	if !handled || cmd == nil {
		t.Fatal("numeric question choice was not handled")
	}
	drain(m, cmd)
	last := fc.last()
	if last.method != "task.user.answer" {
		t.Fatalf("rpc method = %q, want task.user.answer", last.method)
	}
	if len(last.params) != 2 || last.params["question_id"] != "q_1" || last.params["value"] != "stage" {
		t.Fatalf("answer params = %#v", last.params)
	}
	if m.question != nil {
		t.Fatal("question overlay remained open after successful answer")
	}
}

func TestQuestionAndApprovalQueuesDoNotClobberEachOther(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.approve": map[string]any{
			"decision": map[string]any{"decision_id": "perm_1", "decision": "allowed"},
		},
		"task.user.answer": nil,
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_1"))
	m.Update(userQuestionEvent("q_1"))
	if m.approval == nil || m.question != nil || len(m.questionQueue) != 1 {
		t.Fatalf("question must queue behind approval: approval=%+v question=%+v queued=%d", m.approval, m.question, len(m.questionQueue))
	}
	cmd, _ := m.handleKey("1")
	drain(m, cmd)
	if m.approval != nil || m.question == nil || m.question.QuestionID != "q_1" {
		t.Fatalf("queued question did not surface after approval: approval=%+v question=%+v", m.approval, m.question)
	}

	m.Update(permissionRequestEvent("perm_2"))
	if len(m.approvalQueue) != 1 || m.approval != nil {
		t.Fatalf("approval must queue behind question: active=%+v queued=%d", m.approval, len(m.approvalQueue))
	}
	cmd, _ = m.handleKey("2")
	drain(m, cmd)
	if m.question != nil || m.approval == nil || m.approval.DecisionID != "perm_2" {
		t.Fatalf("queued approval did not surface after answer: approval=%+v question=%+v", m.approval, m.question)
	}
}

func TestQuestionRPCFailureKeepsOverlayOpen(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.user.answer": errTestRPC}}
	m, _ := newTestModel(fc)
	m.Update(userQuestionEvent("q_retry"))
	cmd, _ := m.handleKey("1")
	drain(m, cmd)
	if m.question == nil || m.question.QuestionID != "q_retry" {
		t.Fatal("failed answer must keep the question available for retry")
	}
	if !strings.Contains(transcriptText(m), errTestRPC.Error()) {
		t.Fatal("failed answer is not visible in the transcript")
	}
}

func TestQuestionRestoresSteeringAndExternalResolutionClosesOverlay(t *testing.T) {
	m, _ := newTestModel(nil)
	m.Update(userQuestionEvent("q_external"))
	if m.inFlightTaskID != "tsk_1" {
		t.Fatalf("in-flight task = %q, want question task", m.inFlightTaskID)
	}
	m.Update(EventMsg{Raw: map[string]any{
		"type": "TaskCreated", "task_id": "tsk_1",
		"payload": map[string]any{
			"status": "user_question_resolved", "question_id": "q_external", "value": "prod",
		},
	}})
	if m.question != nil || !m.questionResolved["q_external"] {
		t.Fatalf("external resolution did not close question: active=%+v resolved=%v", m.question, m.questionResolved)
	}
}

var errTestRPC = &questionTestError{}

type questionTestError struct{}

func (*questionTestError) Error() string { return "answer rejected" }
