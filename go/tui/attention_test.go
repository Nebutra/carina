package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestViewRequestsTerminalFocusReporting(t *testing.T) {
	m, _ := newTestModel(nil)
	if view := m.View(); !view.ReportFocus {
		t.Fatal("TUI did not request terminal focus reporting")
	}
}

func TestBackgroundAttentionLatchesUntilFocusReturns(t *testing.T) {
	m, _ := newTestModel(nil)
	m.Update(tea.BlurMsg{})

	_, cmd := m.Update(EventMsg{Raw: map[string]any{
		"type": "task.completed", "task_id": "tsk_background_1", "status": "completed",
	}})
	if cmd == nil {
		t.Fatal("first important background event did not alert")
	}
	msg := cmd()
	raw, ok := msg.(tea.RawMsg)
	if !ok {
		t.Fatalf("background alert message = %T, want tea.RawMsg", msg)
	}
	sequence, ok := raw.Msg.(string)
	if !ok || !strings.Contains(sequence, attentionBell) ||
		!strings.Contains(sequence, "]9;Task finished") ||
		!strings.Contains(sequence, "]777;notify;Carina;Task finished") {
		t.Fatalf("background alert sequence = %q", sequence)
	}
	if m.unreadAttention != 1 || !m.attentionAlerted {
		t.Fatalf("first attention state = unread %d, alerted %v", m.unreadAttention, m.attentionAlerted)
	}

	_, cmd = m.Update(EventMsg{Raw: map[string]any{
		"type": "task.completed", "task_id": "tsk_background_2", "status": "completed",
	}})
	if cmd != nil {
		t.Fatal("second important event in one blur interval emitted a notification storm")
	}
	if m.unreadAttention != 2 {
		t.Fatalf("latched background events = %d, want 2", m.unreadAttention)
	}
	if status := m.View().Content; !strings.Contains(status, "2 attention") {
		t.Fatalf("status line did not expose unread attention:\n%s", status)
	}

	m.Update(tea.FocusMsg{})
	if m.terminalBlurred || m.unreadAttention != 0 || m.attentionAlerted {
		t.Fatalf("focus did not clear attention: blurred=%v unread=%d alerted=%v",
			m.terminalBlurred, m.unreadAttention, m.attentionAlerted)
	}
}

func TestFocusedImportantEventDoesNotAlert(t *testing.T) {
	m, _ := newTestModel(nil)
	_, cmd := m.Update(permissionRequestEvent("perm_focused"))
	if cmd != nil || m.unreadAttention != 0 || m.attentionAlerted {
		t.Fatalf("focused event alerted: cmd=%v unread=%d alerted=%v",
			cmd != nil, m.unreadAttention, m.attentionAlerted)
	}
}

func TestAttentionOSCUsesFixedTrustedCopy(t *testing.T) {
	m, _ := newTestModel(nil)
	m.terminalBlurredNow()
	cmd := m.noteAttention(map[string]any{
		"type": "permission.request", "resource": "\x1b]777;notify;Injected;Payload\x07",
	})
	if cmd == nil {
		t.Fatal("important event did not produce an alert")
	}
	raw := cmd().(tea.RawMsg)
	sequence := raw.Msg.(string)
	if strings.Contains(sequence, "Injected") || strings.Contains(sequence, "Payload") {
		t.Fatalf("runtime-controlled event data reached OSC sequence: %q", sequence)
	}
}

func TestMemoryProjectionFailureRequiresAttentionButBlockedDoesNotDuplicateApproval(t *testing.T) {
	m, _ := newTestModel(nil)
	for _, status := range []string{"failed", "reconcile"} {
		text, important := m.attentionEventText(map[string]any{"type": "MemoryProjectionChanged", "payload": map[string]any{"status": status, "document_id": "mem_1"}})
		if !important || !strings.Contains(text, "Memory sync") {
			t.Fatalf("status %s did not surface actionable attention: %q %v", status, text, important)
		}
	}
	if _, important := m.attentionEventText(map[string]any{"type": "MemoryProjectionChanged", "payload": map[string]any{"status": "blocked"}}); important {
		t.Fatal("blocked projection duplicated the permission approval attention")
	}
}
