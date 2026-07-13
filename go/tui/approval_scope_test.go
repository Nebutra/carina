package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestApprovalScopeIsExplicitInRPC(t *testing.T) {
	for _, tc := range []struct {
		key   string
		scope string
	}{
		{key: "y", scope: "once"},
		{key: "2", scope: "session"},
		{key: "3", scope: "project"},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			fc := &fakeCaller{handler: map[string]any{
				"task.action.approve": map[string]any{
					"decision": map[string]any{"decision_id": "perm_scope", "decision": "allowed"},
					"scope":    tc.scope,
				},
			}}
			m, _ := newTestModel(fc)
			m.Update(permissionRequestEvent("perm_scope"))
			cmd, handled := m.handleKey(tc.key)
			if !handled {
				t.Fatalf("key %q was not handled", tc.key)
			}
			drain(m, cmd)
			last := fc.last()
			if got := last.params["scope"]; got != tc.scope {
				t.Fatalf("approval scope RPC param = %v, want %s", got, tc.scope)
			}
		})
	}
}

func TestApprovalUsesDaemonReportedScope(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.approve": map[string]any{
			"decision":    map[string]any{"decision_id": "perm_scope", "decision": "allowed"},
			"scope":       "once",
			"grant_error": "audit unavailable",
		},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_scope"))
	cmd, _ := m.handleKey("3")
	drain(m, cmd)
	text := transcriptText(m)
	if !containsAll(text, "Scope: once", "requested project scope was not persisted") {
		t.Fatalf("TUI presented an unpersisted project grant as active:\n%s", text)
	}
}

func TestExternalApprovalResolutionAdvancesQueueAndIgnoresLateRPC(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(permissionRequestEvent("perm_external"))
	m.Update(permissionRequestEvent("perm_next"))
	if m.approval == nil || len(m.approvalQueue) != 1 {
		t.Fatal("precondition: one active and one queued approval expected")
	}

	m.Update(EventMsg{Raw: map[string]any{
		"type":                   "TaskCreated",
		"permission_decision_id": "perm_external",
		"payload": map[string]any{
			"status": "approval_resolved", "decision_id": "perm_external", "granted": true,
		},
	}})
	if m.approval == nil || m.approval.DecisionID != "perm_next" {
		t.Fatalf("external resolution did not advance to queued approval: %#v", m.approval)
	}
	m.handleApprovalDone(approvalDoneMsg{decisionID: "perm_external", verdict: "allowed"})
	if m.approval == nil || m.approval.DecisionID != "perm_next" {
		t.Fatal("late RPC completion closed the newer approval")
	}
	m.Update(permissionRequestEvent("perm_queued_resolved"))
	m.Update(EventMsg{Raw: map[string]any{
		"type": "TaskCreated",
		"payload": map[string]any{
			"status": "approval_resolved", "decision_id": "perm_queued_resolved", "granted": false,
		},
	}})
	if len(m.approvalQueue) != 0 {
		t.Fatal("external resolution must remove a queued approval")
	}

	m.Update(permissionRequestEvent("perm_external"))
	if len(m.approvalQueue) != 0 {
		t.Fatal("resolved approval must not reopen or requeue")
	}
}

func TestApprovalWrapsEveryReviewableCell(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 12})
	longCommand := "rm-safe-prefix-" + strings.Repeat("x", 70) + "-dangerous-suffix"
	ev := permissionRequestEvent("perm_wrap")
	ev.Raw["label"] = longCommand
	m.Update(ev)

	body := m.approvalBodyLines()
	if !strings.Contains(ansi.Strip(strings.Join(body, "")), "dangerous-suffix") {
		t.Fatalf("wrapped review body hid the command suffix: %#v", body)
	}
	for _, line := range body {
		if width := ansi.StringWidth(line); width > m.approvalContentWidth() {
			t.Fatalf("approval line width=%d exceeds content width=%d: %q", width, m.approvalContentWidth(), line)
		}
	}
}

func TestEventFirstApprovalResolutionStillRecordsLocalVerdict(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.action.deny": nil}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_race"))
	m.Update(permissionRequestEvent("perm_after"))
	cmd, handled := m.handleKey("esc")
	if !handled || cmd == nil || m.approval == nil || !m.approval.Resolving {
		t.Fatal("local deny must enter resolving before the RPC command runs")
	}

	m.Update(EventMsg{Raw: map[string]any{
		"type": "TaskCreated",
		"payload": map[string]any{
			"status": "approval_resolved", "decision_id": "perm_race", "granted": false, "scope": "deny",
		},
	}})
	if m.approval == nil || m.approval.DecisionID != "perm_after" {
		t.Fatal("event-first resolution must advance the visible approval queue")
	}
	drain(m, cmd)
	if m.approval == nil || m.approval.DecisionID != "perm_after" {
		t.Fatal("late local verdict must not close the newer approval")
	}
	if m.Outcome() != OutcomeUserDenied {
		t.Fatalf("event-first local deny outcome = %v, want OutcomeUserDenied", m.Outcome())
	}
	if _, pending := m.approvalPending["perm_race"]; pending {
		t.Fatal("local verdict reconciliation entry was not cleared")
	}
}

func TestEventFirstResolutionUsesDurableVerdictWhenRPCErrs(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.action.approve": errors.New("reply lost")}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_policy_race"))
	cmd, _ := m.handleKey("y")
	m.Update(EventMsg{Raw: map[string]any{
		"type": "TaskCreated",
		"payload": map[string]any{
			"status": "approval_resolved", "decision_id": "perm_policy_race", "granted": false, "scope": "once",
		},
	}})
	drain(m, cmd)
	if m.Outcome() != OutcomePolicyDenied {
		t.Fatalf("durable event verdict outcome = %v, want OutcomePolicyDenied", m.Outcome())
	}
	if !strings.Contains(transcriptText(m), "Denied: command.exec") {
		t.Fatalf("durable event verdict was not rendered:\n%s", transcriptText(m))
	}
}

func TestApprovalOutcomeFollowsDecisionOrderNotRPCCompletionOrder(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.deny": nil,
		"task.action.approve": map[string]any{
			"decision": map[string]any{"decision_id": "perm_b", "decision": "allowed"},
		},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_a"))
	m.Update(permissionRequestEvent("perm_b"))
	cmdA, _ := m.handleKey("esc")
	m.Update(EventMsg{Raw: map[string]any{
		"type": "TaskCreated",
		"payload": map[string]any{
			"status": "approval_resolved", "decision_id": "perm_a", "granted": false, "scope": "deny",
		},
	}})
	if m.approval == nil || m.approval.DecisionID != "perm_b" {
		t.Fatal("approval B did not surface after approval A's durable resolution")
	}
	cmdB, _ := m.handleKey("y")
	drain(m, cmdB)
	if m.Outcome() != OutcomeOK {
		t.Fatalf("approval B outcome = %v, want OutcomeOK", m.Outcome())
	}
	drain(m, cmdA)
	if m.Outcome() != OutcomeOK {
		t.Fatalf("late approval A completion overwrote newer outcome: %v", m.Outcome())
	}
}

func TestApprovalResolvingSuppressesDuplicateRPC(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.approve": map[string]any{
			"decision": map[string]any{"decision_id": "perm_once", "decision": "allowed"},
		},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_once"))

	first, handled := m.approvalKey("y")
	if !handled || first == nil || !m.approval.Resolving {
		t.Fatal("first decision must synchronously enter resolving state")
	}
	second, handled := m.approvalKey("y")
	if !handled || second != nil {
		t.Fatal("repeated decision key must be consumed without another command")
	}
	if !strings.Contains(ansi.Strip(m.overlayView()), "Resolving decision") {
		t.Fatal("busy state is not visible in the approval footer")
	}
	drain(m, first)
	if len(fc.calls) != 1 {
		t.Fatalf("approval RPC calls = %d, want exactly 1", len(fc.calls))
	}
}

func TestApprovalFailureKeepsCurrentDecisionAndQueueForRetry(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.action.approve": errTestRPC}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_retry"))
	m.Update(permissionRequestEvent("perm_next"))

	cmd, _ := m.approvalKey("y")
	drain(m, cmd)
	if m.approval == nil || m.approval.DecisionID != "perm_retry" {
		t.Fatalf("failed RPC advanced the queue: active=%+v", m.approval)
	}
	if m.approval.Resolving || len(m.approvalQueue) != 1 {
		t.Fatalf("failed state = resolving %v, queued %d; want retryable with queue intact", m.approval.Resolving, len(m.approvalQueue))
	}
	if !strings.Contains(ansi.Strip(m.overlayView()), "Press the decision key to retry") {
		t.Fatal("retry guidance is not visible in the overlay")
	}

	fc.handler["task.action.approve"] = map[string]any{
		"decision": map[string]any{"decision_id": "perm_retry", "decision": "allowed"},
	}
	cmd, _ = m.approvalKey("y")
	drain(m, cmd)
	if m.approval == nil || m.approval.DecisionID != "perm_next" {
		t.Fatalf("successful retry did not advance exactly once: active=%+v", m.approval)
	}
}

func TestApprovalEscapeExplicitlyDeniesPendingDecision(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.deny": map[string]any{"decision_id": "perm_pending", "decision": "denied"},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_pending"))

	cmd, handled := m.approvalKey("esc")
	if !handled || cmd == nil || !m.approval.Resolving {
		t.Fatal("Esc must enter resolving and issue an explicit deny command")
	}
	if second, handled := m.approvalKey("esc"); !handled || second != nil {
		t.Fatal("repeated Esc must not issue a duplicate denial")
	}
	drain(m, cmd)
	if m.approval != nil || len(fc.calls) != 1 || fc.last().method != "task.action.deny" {
		t.Fatalf("Esc denial did not resolve exactly once: active=%+v calls=%v", m.approval, fc.calls)
	}
}

func TestApprovalLongBodyScrollsWithinAnchoredFrame(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(permissionRequestEvent("perm_diff"))
	m.height = 12
	m.approval.Body = make([]string, 40)
	for i := range m.approval.Body {
		m.approval.Body[i] = strings.Repeat("x", i+1)
	}

	before := ansi.Strip(m.overlayView())
	if got := len(strings.Split(before, "\n")); got > m.height {
		t.Fatalf("overlay height = %d, terminal height = %d:\n%s", got, m.height, before)
	}
	if !containsAll(before, "approve", "deny", "pgdown") {
		t.Fatalf("anchored approval footer missing:\n%s", before)
	}
	_, _ = m.approvalKey("pgdown")
	after := ansi.Strip(m.overlayView())
	if m.approval.Scroll == 0 || before == after {
		t.Fatal("PageDown did not move the approval viewport")
	}
	if !containsAll(after, "approve", "deny") {
		t.Fatal("approval actions disappeared after scrolling")
	}
}

func TestApprovalNarrowFooterKeepsBothDecisionsVisible(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(permissionRequestEvent("perm_narrow"))
	m.width = 32
	m.height = 10

	view := ansi.Strip(m.overlayView())
	if !containsAll(view, "allow", "deny") {
		t.Fatalf("narrow approval footer lost a decision action:\n%s", view)
	}
	if got := len(strings.Split(view, "\n")); got > m.height {
		t.Fatalf("narrow approval height = %d, terminal height = %d", got, m.height)
	}
}

func TestApprovalFooterSurvivesOneRowModalDegradation(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(permissionRequestEvent("perm_tiny"))
	// Use the actual root view: its modal clipper intentionally preserves the
	// final content row when only one terminal row exists.
	m.width, m.height = 32, 1
	view := ansi.Strip(m.View().Content)
	if !containsAll(view, "allow", "deny") {
		t.Fatalf("one-row approval lost its action footer: %q", view)
	}
}

func containsAll(text string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(text, value) {
			return false
		}
	}
	return true
}
