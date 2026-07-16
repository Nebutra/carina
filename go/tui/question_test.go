package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
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

func TestQuestionWithoutOptionsAcceptsFreeText(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.user.answer": nil}}
	m, _ := newTestModel(fc)
	m.Update(EventMsg{Raw: map[string]any{
		"type": "user.question", "session_id": "sess_test", "task_id": "tsk_1",
		"question_id": "q_wait", "prompt": "Waiting for choices", "options": []any{},
	}})
	if m.question == nil || m.question.QuestionID != "q_wait" {
		t.Fatalf("zero-option question was discarded: %#v", m.question)
	}
	if !strings.Contains(ansi.Strip(m.questionOverlayView()), "Type your answer") {
		t.Fatalf("zero-option state is not explained: %q", m.questionOverlayView())
	}
	if cmd, handled := m.questionKey("enter"); !handled || cmd != nil {
		t.Fatal("empty free-text answer must be rejected")
	}
	for _, key := range []string{"s", "h", "i", "p", " ", "i", "t"} {
		m.questionKey(key)
	}
	cmd, handled := m.questionKey("enter")
	if !handled || cmd == nil {
		t.Fatal("free-text answer did not submit")
	}
	drain(m, cmd)
	if got := fc.last().params["value"]; got != "ship it" {
		t.Fatalf("free-text value = %#v", got)
	}
}

func TestQuestionFreeTextTreatsEmojiAsAtomicInput(t *testing.T) {
	m, _ := newTestModel(nil)
	m.Update(EventMsg{Raw: map[string]any{
		"type": "user.question", "session_id": "sess_test", "task_id": "tsk_1",
		"question_id": "q_emoji", "prompt": "Describe it", "options": []any{},
	}})
	m.Update(tea.KeyPressMsg{Text: "👩🏽‍💻", Code: tea.KeyExtended})
	if got := m.question.FreeText; got != "👩🏽‍💻" {
		t.Fatalf("emoji input = %q", got)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := m.question.FreeText; got != "" {
		t.Fatalf("backspace split emoji: %q", got)
	}
	m.Update(tea.PasteMsg{Content: "🇨🇳\n1️⃣"})
	if got := m.question.FreeText; got != "🇨🇳 1️⃣" {
		t.Fatalf("emoji paste = %q", got)
	}
}

func TestFreeTextQuestionFailureAndReconnectPreserveDraft(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.user.answer": errTestRPC}}
	m, _ := newTestModel(fc)
	m.Update(EventMsg{Raw: map[string]any{"type": "user.question", "question_id": "q_free_retry", "task_id": "tsk_1", "prompt": "Explain", "options": []any{}}})
	for _, key := range []string{"保", "留"} {
		m.questionKey(key)
	}
	cmd, handled := m.questionKey("enter")
	drain(m, mustQuestionCmd(t, cmd, handled))
	if m.question == nil || m.question.FreeText != "保留" || m.question.Resolving {
		t.Fatalf("failed answer lost draft: %#v", m.question)
	}
	m.Update(ConnLostMsg{SessionID: m.sessionID, Generation: m.sessionGeneration, Err: errTestRPC})
	m.Update(ConnRestoredMsg{SessionID: m.sessionID, Generation: m.sessionGeneration})
	fc.handler["task.user.answer"] = nil
	cmd, handled = m.questionKey("enter")
	drain(m, mustQuestionCmd(t, cmd, handled))
	if m.question != nil || fc.last().params["value"] != "保留" {
		t.Fatalf("retry after reconnect failed: question=%#v call=%#v", m.question, fc.last())
	}
}

func mustQuestionCmd(t *testing.T, cmd tea.Cmd, handled bool) tea.Cmd {
	t.Helper()
	if !handled || cmd == nil {
		t.Fatal("expected question command")
	}
	return cmd
}

func TestFreeTextQuestionNarrowViewportKeepsInputAndSubmitVisible(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.width, m.height = 18, 8
	m.Update(EventMsg{Raw: map[string]any{"type": "user.question", "question_id": "q_narrow", "task_id": "tsk_1", "prompt": "A very long prompt that wraps", "options": []any{}}})
	m.questionKey("x")
	view := ansi.Strip(m.questionOverlayView())
	if !strings.Contains(view, "> x|") || !strings.Contains(view, "enter") {
		t.Fatalf("narrow free-text question lost input/action: %q", view)
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
	if m.question.Resolving || !strings.Contains(ansi.Strip(m.questionOverlayView()), "Press [enter] to retry") {
		t.Fatal("failed answer must return to a visible, retryable state")
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

func TestQuestionArrowTabEnterAndNumericKeysAreConsistent(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.user.answer": nil}}
	m, _ := newTestModel(fc)
	m.Update(userQuestionEvent("q_keys"))

	if cmd, handled := m.questionKey("down"); !handled || cmd != nil || m.question.Selected != 1 {
		t.Fatalf("down selection = %d, want 1", m.question.Selected)
	}
	if cmd, handled := m.questionKey("tab"); !handled || cmd != nil || m.question.Selected != 0 {
		t.Fatalf("tab must wrap selection, got %d", m.question.Selected)
	}
	if cmd, _ := m.questionKey("up"); cmd != nil || m.question.Selected != 1 {
		t.Fatalf("up must wrap selection, got %d", m.question.Selected)
	}
	cmd, handled := m.questionKey("enter")
	if !handled || cmd == nil {
		t.Fatal("Enter did not submit the highlighted option")
	}
	drain(m, cmd)
	if got := fc.last().params["value"]; got != "stage" {
		t.Fatalf("Enter submitted %v, want highlighted value stage", got)
	}

	m.Update(userQuestionEvent("q_number"))
	cmd, _ = m.questionKey("1")
	drain(m, cmd)
	if got := fc.last().params["value"]; got != "prod" {
		t.Fatalf("numeric shortcut submitted %v, want prod", got)
	}
}

func TestQuestionResolvingSuppressesDuplicateRPC(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.user.answer": nil}}
	m, _ := newTestModel(fc)
	m.Update(userQuestionEvent("q_once"))

	first, _ := m.questionKey("enter")
	if first == nil || !m.question.Resolving {
		t.Fatal("answer must synchronously enter resolving state")
	}
	second, handled := m.questionKey("enter")
	if !handled || second != nil {
		t.Fatal("repeated Enter must be consumed without another command")
	}
	if !strings.Contains(ansi.Strip(m.questionOverlayView()), "Sending answer") {
		t.Fatal("question busy state is not visible")
	}
	drain(m, first)
	if len(fc.calls) != 1 {
		t.Fatalf("answer RPC calls = %d, want exactly 1", len(fc.calls))
	}
}

func TestQuestionFailureRetriesWithoutAdvancingQueue(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.user.answer": errTestRPC}}
	m, _ := newTestModel(fc)
	m.Update(userQuestionEvent("q_retry_current"))
	m.Update(userQuestionEvent("q_after_retry"))

	cmd, _ := m.questionKey("2")
	drain(m, cmd)
	if m.question == nil || m.question.QuestionID != "q_retry_current" || m.question.Selected != 1 {
		t.Fatalf("failed answer lost current selection: active=%+v", m.question)
	}
	if len(m.questionQueue) != 1 {
		t.Fatalf("failed answer changed queue length to %d", len(m.questionQueue))
	}

	fc.handler["task.user.answer"] = nil
	cmd, _ = m.questionKey("enter")
	drain(m, cmd)
	if m.question == nil || m.question.QuestionID != "q_after_retry" {
		t.Fatalf("successful retry did not advance exactly once: active=%+v", m.question)
	}
	if got := fc.last().params["value"]; got != "stage" {
		t.Fatalf("retry lost selected value: got %v", got)
	}
}

func TestQuestionEscapeRefusesToOrphanPendingQuestion(t *testing.T) {
	m, _ := newTestModel(nil)
	m.Update(userQuestionEvent("q_pending"))

	cmd, handled := m.questionKey("esc")
	if !handled || cmd != nil || m.question == nil {
		t.Fatal("Esc must keep and consume the pending question")
	}
	if !strings.Contains(ansi.Strip(m.questionOverlayView()), "Esc cannot dismiss") {
		t.Fatal("Esc refusal is not explained in the question overlay")
	}
}

func TestQuestionLongContentScrollsWithFooterAnchored(t *testing.T) {
	m, _ := newTestModel(nil)
	m.Update(userQuestionEvent("q_long"))
	m.height = 12
	m.question.Prompt = strings.Repeat("review this context carefully ", 20)
	m.question.Options = make([]questionOption, 12)
	for i := range m.question.Options {
		m.question.Options[i] = questionOption{
			Label:       fmt.Sprintf("Option %d", i+1),
			Value:       fmt.Sprintf("option-%d", i+1),
			Description: strings.Repeat("detailed consequence ", 4),
		}
	}

	before := ansi.Strip(m.questionOverlayView())
	if got := len(strings.Split(before, "\n")); got > m.height {
		t.Fatalf("question overlay height = %d, terminal height = %d", got, m.height)
	}
	if !containsAll(before, "select", "answer", "scroll") {
		t.Fatalf("anchored question footer missing:\n%s", before)
	}
	_, _ = m.questionKey("pgdown")
	after := ansi.Strip(m.questionOverlayView())
	if m.question.Scroll == 0 || before == after {
		t.Fatal("PageDown did not move the question viewport")
	}
	if !containsAll(after, "select", "answer") {
		t.Fatal("question actions disappeared after scrolling")
	}
}

func TestQuestionZeroOptionsIsDefensiveAndDoesNotPanic(t *testing.T) {
	m, _ := newTestModel(nil)
	m.question = &questionState{QuestionID: "q_empty", Prompt: "No choices arrived"}

	cmd, handled := m.questionKey("down")
	if !handled || cmd != nil {
		t.Fatal("empty question must consume overlay input without a command")
	}
	if !strings.Contains(ansi.Strip(m.questionOverlayView()), "Type your answer") {
		t.Fatal("empty question does not explain its recoverable state")
	}
}

func TestQuestionNarrowFooterKeepsAnswerActionVisible(t *testing.T) {
	m, _ := newTestModel(nil)
	m.Update(userQuestionEvent("q_narrow"))
	m.width = 30
	m.height = 10

	view := ansi.Strip(m.questionOverlayView())
	if !strings.Contains(view, "answer") {
		t.Fatalf("narrow question footer lost its primary action:\n%s", view)
	}
	if got := len(strings.Split(view, "\n")); got > m.height {
		t.Fatalf("narrow question height = %d, terminal height = %d", got, m.height)
	}
}

func TestQuestionFooterSurvivesOneRowModalDegradation(t *testing.T) {
	m, _ := newTestModel(nil)
	m.Update(userQuestionEvent("q_tiny"))
	m.width, m.height = 30, 1

	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "answer") {
		t.Fatalf("one-row question lost its action footer: %q", view)
	}
}

var errTestRPC = &questionTestError{}

type questionTestError struct{}

func (*questionTestError) Error() string { return "answer rejected" }
