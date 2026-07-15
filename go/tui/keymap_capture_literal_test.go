package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

func newLiteralCaptureModel(t *testing.T) (*Model, *[]string) {
	t.Helper()
	var saved []string
	const context = KeyContextKeymapAction
	const action = ActionKeymapActionBack
	m, err := NewChecked(Options{
		Theme: theme.New(theme.Mono),
		KeymapUpdater: func(_ string, keys []string, _ bool) ([]KeyBindingOverride, error) {
			saved = append([]string(nil), keys...)
			return []KeyBindingOverride{{Context: context, Action: action, Keys: keys}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.openKeymapEditor()
	m.keymapEditor.selected = descriptorIndex(m.keymapEditor.bindings, action)
	m.beginKeymapCapture(false)
	return m, &saved
}

func runCaptureKeys(t *testing.T, m *Model, keys ...string) tea.Cmd {
	t.Helper()
	var result tea.Cmd
	for _, key := range keys {
		cmd, handled := m.keymapEditorKey(key)
		if !handled {
			t.Fatalf("capture key %q was not handled", key)
		}
		if cmd != nil {
			result = cmd
		}
	}
	return result
}

func TestKeymapCaptureQuotedInsertRecordsEscape(t *testing.T) {
	m, saved := newLiteralCaptureModel(t)
	cmd := runCaptureKeys(t, m, "ctrl+v", "esc")
	if cmd == nil {
		t.Fatal("quoted Escape did not schedule persistence")
	}
	drain(m, cmd)
	if len(*saved) != 1 || (*saved)[0] != "esc" {
		t.Fatalf("saved keys = %#v, want [esc]", *saved)
	}
}

func TestKeymapCaptureQuotedInsertRecordsChordTerminators(t *testing.T) {
	for _, terminator := range []string{"enter", "esc"} {
		t.Run(terminator, func(t *testing.T) {
			m, saved := newLiteralCaptureModel(t)
			cmd := runCaptureKeys(t, m, "ctrl+x", "ctrl+v", terminator, "enter")
			if cmd == nil {
				t.Fatal("quoted chord did not schedule persistence")
			}
			drain(m, cmd)
			want := "ctrl+x " + terminator
			if len(*saved) != 1 || (*saved)[0] != want {
				t.Fatalf("saved keys = %#v, want [%s]", *saved, want)
			}
		})
	}
}

func TestKeymapCaptureQuotedInsertKeepsCancelReachable(t *testing.T) {
	m, _ := newLiteralCaptureModel(t)
	runCaptureKeys(t, m, "esc")
	if m.keymapEditor.mode != keymapChooseAction || !strings.Contains(m.keymapEditor.status, "cancelled") {
		t.Fatalf("bare Escape did not cancel capture: %#v", m.keymapEditor)
	}
}

func TestKeymapCaptureQuotedInsertTimesOutSafely(t *testing.T) {
	m, _ := newLiteralCaptureModel(t)
	timeout := runCaptureKeys(t, m, "ctrl+v")
	if timeout == nil || !m.keymapEditor.quoted {
		t.Fatalf("quoted insert was not armed: %#v", m.keymapEditor)
	}
	m.Update(timeout())
	if m.keymapEditor.mode != keymapChooseAction || m.keymapEditor.quoted ||
		!strings.Contains(m.keymapEditor.status, "timed out") {
		t.Fatalf("quoted insert timeout state = %#v", m.keymapEditor)
	}
}

func TestKeymapCaptureQuotedInsertCanRecordItself(t *testing.T) {
	m, saved := newLiteralCaptureModel(t)
	cmd := runCaptureKeys(t, m, "ctrl+v", "ctrl+v", "enter")
	if cmd == nil {
		t.Fatal("quoted Ctrl+V did not schedule persistence")
	}
	drain(m, cmd)
	if len(*saved) != 1 || (*saved)[0] != "ctrl+v" {
		t.Fatalf("saved keys = %#v, want [ctrl+v]", *saved)
	}
}
