package tui

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestConversationProjectionTransitions(t *testing.T) {
	var p conversationProjection
	steps := []struct {
		transition conversationTransition
		activity   conversationActivity
		outcome    conversationOutcome
	}{
		{conversationTransition{Kind: transitionConnected, SessionID: "sess_1"}, activityIdle, outcomeNone},
		{conversationTransition{Kind: transitionSubmitting}, activitySubmitting, outcomeNone},
		{conversationTransition{Kind: transitionRunning, TaskID: "task_1"}, activityRunning, outcomeNone},
		{conversationTransition{Kind: transitionWaitingApproval, TaskID: "task_1", DecisionID: "perm_1"}, activityWaitingApproval, outcomeNone},
		{conversationTransition{Kind: transitionApprovalResolved, DecisionID: "perm_1"}, activityRunning, outcomeNone},
		{conversationTransition{Kind: transitionWaitingQuestion, TaskID: "task_1", QuestionID: "q_1"}, activityWaitingQuestion, outcomeNone},
		{conversationTransition{Kind: transitionQuestionResolved, QuestionID: "q_1"}, activityRunning, outcomeNone},
		{conversationTransition{Kind: transitionInterrupted, TaskID: "task_1", Status: "interrupted"}, activityInterrupted, outcomeNone},
		{conversationTransition{Kind: transitionTerminal, TaskID: "task_1", Status: "degraded", Outcome: outcomeDegraded}, activityIdle, outcomeDegraded},
	}
	for i, step := range steps {
		if !p.reduce(step.transition) {
			t.Fatalf("step %d was rejected: %+v", i, step.transition)
		}
		if p.Activity != step.activity || p.Outcome != step.outcome {
			t.Fatalf("step %d projection = %+v", i, p)
		}
	}
	if p.Evidence.SessionID != "sess_1" || p.Evidence.TerminalID != "task_1" || p.Evidence.ActiveTaskID != "" {
		t.Fatalf("terminal evidence = %+v", p.Evidence)
	}
}

func TestConversationReadinessNeverInventsReady(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	if got := m.statusActivityText(); got != "checking runtime" {
		t.Fatalf("initial activity = %q", got)
	}
	m.conversation.Readiness = readinessBlocked
	if got := m.statusActivityText(); got != "setup required" {
		t.Fatalf("blocked activity = %q", got)
	}
	m.conversation.Readiness = readinessReady
	if got := m.statusActivityText(); got != "ready" {
		t.Fatalf("ready activity = %q", got)
	}
}

func TestConversationProjectionRejectsUnrelatedTerminalAndResolution(t *testing.T) {
	p := conversationProjection{Activity: activityRunning, Evidence: conversationEvidence{ActiveTaskID: "task_active", DecisionID: "perm_active"}}
	if p.reduce(conversationTransition{Kind: transitionTerminal, TaskID: "task_other", Outcome: outcomeFailed}) {
		t.Fatal("unrelated terminal event changed the active conversation")
	}
	if p.reduce(conversationTransition{Kind: transitionApprovalResolved, DecisionID: "perm_other"}) {
		t.Fatal("unrelated approval resolution changed the active conversation")
	}
	if p.Activity != activityRunning || p.Evidence.ActiveTaskID != "task_active" {
		t.Fatalf("projection changed after unrelated events: %+v", p)
	}
}

func TestConversationTerminalNormalization(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   conversationOutcome
	}{
		{"completed", outcomeCompleted},
		{"degraded", outcomeDegraded},
		{"failed", outcomeFailed},
		{"cancelled", outcomeCancelled},
	} {
		outcome, terminal := terminalConversationEvent(map[string]any{
			"type": "task.completed", "task_id": "task_1", "status": tc.status,
		})
		if !terminal || outcome != tc.want {
			t.Errorf("status %q = (%v, %v), want %v", tc.status, outcome, terminal, tc.want)
		}
	}
}

func TestDurableDecisionResolutionWinsOverWaitingSubstring(t *testing.T) {
	p := conversationProjection{
		Activity: activityWaitingApproval,
		Evidence: conversationEvidence{ActiveTaskID: "task_1", DecisionID: "perm_1"},
	}
	transition, ok := conversationTransitionForEvent(map[string]any{
		"type": "TaskCreated", "task_id": "task_1",
		"payload": map[string]any{"status": "approval_resolved", "decision_id": "perm_1"},
	})
	if !ok || transition.Kind != transitionApprovalResolved {
		t.Fatalf("resolution normalized as %+v, ok=%v", transition, ok)
	}
	if !p.reduce(transition) || p.Activity != activityRunning || p.Evidence.DecisionID != "" {
		t.Fatalf("resolution did not resume the active task: %+v", p)
	}
}

func TestInterruptedConversationRequiresAttention(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	text, ok := m.attentionEventText(map[string]any{
		"type": "TaskInterrupted", "task_id": "task_1", "payload": map[string]any{"status": "interrupted"},
	})
	if !ok || text != "Task interrupted" {
		t.Fatalf("attention = %q, %v", text, ok)
	}
}

func TestDegradedConversationAgreesAcrossFooterRailTranscriptAndAttention(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionID = "sess_1"
	m.conn = ConnConnected
	m.inFlightTaskID = "task_1"
	m.tasks.ensure("task_1", "", "task", "inspect workspace", "running")
	m.applyConversation(conversationTransition{Kind: transitionRunning, TaskID: "task_1", Status: "running"})

	ev := map[string]any{"type": "task.completed", "task_id": "task_1", "status": "degraded", "summary": "partial result"}
	m.handleEvent(ev)
	footer := m.statusActivityText()
	if footer != "degraded" {
		t.Fatalf("footer = %q, want degraded", footer)
	}
	rail := strings.Join(m.tasks.lines(m, 80, 4), "\n")
	if !strings.Contains(rail, "degraded") {
		t.Fatalf("rail missing degraded: %q", rail)
	}
	if transcript := strings.Join(m.tr.lines, "\n"); !strings.Contains(transcript, "degraded") {
		t.Fatalf("transcript missing degraded: %q", transcript)
	}
	if text, ok := m.attentionEventText(ev); !ok || text != "Task degraded" {
		t.Fatalf("attention = %q, %v", text, ok)
	}
}
