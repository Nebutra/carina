package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestChordBoundConfirmationActionsRemainReachable(t *testing.T) {
	t.Run("interrupt", func(t *testing.T) {
		clock := &testClock{now: time.Unix(100, 0)}
		m, err := NewChecked(Options{
			Now: func() time.Time { return clock.now },
			Keybindings: []KeyBindingOverride{{
				Context: KeyContextGlobal, Action: ActionGlobalInterrupt, Keys: []string{"ctrl+x ctrl+c"},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}

		m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		if _, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}); cmd != nil {
			t.Fatal("first interrupt chord should only arm exit")
		}
		clock.advance(time.Second)
		m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		if cmd == nil {
			t.Fatal("second interrupt chord did not exit")
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatal("second interrupt chord did not return tea.Quit")
		}
	})

	t.Run("rewind", func(t *testing.T) {
		m, err := NewChecked(Options{Keybindings: []KeyBindingOverride{{
			Context: KeyContextChat, Action: ActionChatRewind, Keys: []string{"ctrl+x ctrl+r"},
		}}})
		if err != nil {
			t.Fatal(err)
		}

		m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
		if !m.rewindPrimed {
			t.Fatal("first rewind chord did not arm checkpoint selection")
		}
		m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
		_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
		if cmd == nil || m.checkpointPicker == nil {
			t.Fatal("second rewind chord did not open checkpoint picker")
		}
	})
}

func TestChordTimeoutDisarmsConfirmationGestures(t *testing.T) {
	m, err := NewChecked(Options{Keybindings: []KeyBindingOverride{{
		Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: []string{"ctrl+x ctrl+h"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	m.lastCtrlC = time.Unix(100, 0)
	m.rewindPrimed = true
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	generation := m.chord.generation
	m.Update(chordTimeoutMsg{generation: generation})
	if !m.lastCtrlC.IsZero() || m.rewindPrimed {
		t.Fatalf("timed-out chord left confirmation armed: ctrlC=%v rewind=%v", m.lastCtrlC, m.rewindPrimed)
	}
}

func TestHistoryScopeLoadBlocksOldResultActionsAndRestoresStableScope(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"history.recent": errors.New("global history unavailable"),
	}}
	m, _ := newTestModel(nil)
	m.call = caller
	m.history = []promptDraft{{Text: "workspace deploy"}}
	startHistorySearch(t, m)
	typeHistoryQuery(t, m, "deploy")

	cmd, handled := m.handleKey("ctrl+s")
	if !handled || cmd == nil || !m.historySearch.loading || m.historySearch.scope != historyScopeGlobal {
		t.Fatalf("scope load did not start: handled=%v cmd=%v search=%#v", handled, cmd != nil, m.historySearch)
	}
	for _, key := range []string{"enter", "tab", "esc", "up", "down"} {
		if action, handled := m.handleKey(key); !handled || action != nil || m.historySearch == nil {
			t.Fatalf("loading history accepted old result for %q: handled=%v cmd=%v search=%#v", key, handled, action != nil, m.historySearch)
		}
	}

	drain(m, cmd)
	search := m.historySearch
	if search == nil || search.loading || search.scope != historyScopeWorkspace || search.loadedScope != historyScopeWorkspace {
		t.Fatalf("failed load did not restore stable scope: %#v", search)
	}
	if !strings.Contains(search.loadError, "global load failed") || !strings.Contains(search.loadError, "workspace kept") {
		t.Fatalf("failed load label is inaccurate: %q", search.loadError)
	}
	assertDraftText(t, m, "workspace deploy")
	if _, handled := m.handleKey("tab"); !handled || m.historySearch != nil {
		t.Fatal("restored stable scope result was not editable after load failure")
	}
}

func TestKeymapCaptureControlActionsRejectChords(t *testing.T) {
	for _, action := range []KeyAction{ActionKeymapCaptureCommit, ActionKeymapCaptureCancel} {
		t.Run(string(action), func(t *testing.T) {
			_, err := newRuntimeKeymap([]KeyBindingOverride{{
				Context: KeyContextKeymapCapture, Action: action, Keys: []string{"ctrl+x ctrl+k"},
			}})
			if err == nil || !strings.Contains(err.Error(), string(action)) ||
				!strings.Contains(err.Error(), "single key") || !strings.Contains(err.Error(), "unreachable") {
				t.Fatalf("capture control chord error = %v", err)
			}
		})
	}
}

func TestAttentionEventsAreDeduplicatedAcrossFocusCycles(t *testing.T) {
	cases := []map[string]any{
		{"type": "permission.request", "decision_id": "perm_dedup"},
		{"type": "user.question", "question_id": "question_dedup"},
		{"type": "task.completed", "task_id": "task_dedup", "status": "completed"},
	}
	for _, ev := range cases {
		t.Run(str(ev["type"]), func(t *testing.T) {
			m, _ := newTestModel(nil)
			m.terminalBlurredNow()
			if cmd := m.noteAttention(ev); cmd == nil || m.unreadAttention != 1 {
				t.Fatalf("first event was not counted: cmd=%v unread=%d", cmd != nil, m.unreadAttention)
			}
			if cmd := m.noteAttention(ev); cmd != nil || m.unreadAttention != 1 {
				t.Fatalf("duplicate event was counted: cmd=%v unread=%d", cmd != nil, m.unreadAttention)
			}
			m.terminalFocusedNow()
			m.terminalBlurredNow()
			if cmd := m.noteAttention(ev); cmd != nil || m.unreadAttention != 0 {
				t.Fatalf("duplicate event re-alerted after focus cycle: cmd=%v unread=%d", cmd != nil, m.unreadAttention)
			}
		})
	}
}

func TestAttentionDedupStorageIsBounded(t *testing.T) {
	m, _ := newTestModel(nil)
	for i := 0; i <= attentionDedupCapacity; i++ {
		_ = m.noteAttention(map[string]any{
			"type": "task.completed", "task_id": fmt.Sprintf("task-%d", i),
		})
	}
	if len(m.attentionSeen) != attentionDedupCapacity || len(m.attentionOrder) != attentionDedupCapacity {
		t.Fatalf("attention dedup size = map %d order %d, want %d", len(m.attentionSeen), len(m.attentionOrder), attentionDedupCapacity)
	}
	m.terminalBlurredNow()
	if cmd := m.noteAttention(map[string]any{"type": "task.completed", "task_id": "task-0"}); cmd == nil {
		t.Fatal("evicted attention event was not eligible for a later alert")
	}
}

func TestExplicitResumeFailurePreservesExistingPausedRestore(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []string{}},
		"task.resume":    errors.New("unknown task"),
	}}
	m, _ := newTestModel(caller)
	original := &checkpointRestoreResult{CheckpointID: "task-old:3", TaskID: "task-old", Turn: 3}
	m.pausedRestore = original

	cmd := m.slashCommand("/resume task-invalid")
	if cmd == nil || m.pausedRestore != original {
		t.Fatal("explicit resume replaced the known paused target before RPC confirmation")
	}
	drain(m, cmd)
	if m.pausedRestore != original || m.checkpointPicker == nil || m.checkpointPicker.resumeError == "" {
		t.Fatalf("failed explicit resume corrupted paused state: paused=%#v picker=%#v", m.pausedRestore, m.checkpointPicker)
	}

	caller.handler["task.resume"] = map[string]any{"task_id": "task-old", "status": "running"}
	drain(m, m.resumePausedRestore(""))
	if m.pausedRestore != nil || m.inFlightTaskID != "task-old" {
		t.Fatalf("valid retry did not clear matching paused target: paused=%#v active=%q", m.pausedRestore, m.inFlightTaskID)
	}
}
