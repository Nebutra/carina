package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestEscInterruptsActiveTurnWithoutArmingExitOrRewind(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{"history.recent": map[string]any{"entries": []string{}}, "task.cancel": nil}}
	m, _ := newTestModel(caller)
	m.inFlightTaskID = "tsk_active"

	cmd, handled := m.handleKey("esc")
	if !handled || cmd == nil || m.rewindPrimed || !m.lastCtrlC.IsZero() {
		t.Fatalf("active Esc state: handled=%v cmd=%v rewind=%v ctrlC=%v", handled, cmd != nil, m.rewindPrimed, m.lastCtrlC)
	}
	drain(m, cmd)
	call := caller.last()
	if call.method != "task.cancel" || call.params["task_id"] != "tsk_active" {
		t.Fatalf("Esc cancel call = %+v", call)
	}
}

func TestDoubleEscOpensNewestCheckpointPicker(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []string{}},
		"session.checkpoint.list": []map[string]any{
			{"checkpoint_id": "tsk:1", "task_id": "tsk", "turn": 1, "summary": "first"},
			{"checkpoint_id": "tsk:2", "task_id": "tsk", "turn": 2, "summary": "latest"},
		},
	}}
	m, _ := newTestModel(caller)
	if cmd, handled := m.handleKey("esc"); !handled || cmd != nil || !m.rewindPrimed || m.checkpointPicker != nil {
		t.Fatalf("first Esc did not prime: handled=%v cmd=%v state=%#v", handled, cmd != nil, m.checkpointPicker)
	}
	cmd, handled := m.handleKey("esc")
	if !handled || cmd == nil || m.checkpointPicker == nil {
		t.Fatalf("second Esc did not open picker: handled=%v cmd=%v", handled, cmd != nil)
	}
	drain(m, cmd)
	if len(m.checkpointPicker.items) != 2 || m.checkpointPicker.selected != 1 {
		t.Fatalf("picker did not select newest checkpoint: %#v", m.checkpointPicker)
	}
}

func TestCheckpointRestoreRequiresPreviewAndExplicitArm(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []string{}},
		"session.checkpoint.list": []map[string]any{
			{"checkpoint_id": "tsk:2", "task_id": "tsk", "turn": 2, "summary": "stable"},
		},
		"session.checkpoint.preview": map[string]any{
			"checkpoint":         map[string]any{"checkpoint_id": "tsk:2", "task_id": "tsk", "turn": 2, "summary": "stable"},
			"conversation_turns": 2, "summary": "stable", "rollback_patches": []string{"patch_3"}, "will_resume": "paused",
		},
		"session.checkpoint.restore": map[string]any{"checkpoint_id": "tsk:2", "task_id": "tsk", "turn": 2},
		"task.resume":                map[string]any{"task_id": "tsk", "status": "running"},
	}}
	m, _ := newTestModel(caller)
	drain(m, m.openCheckpointPicker())
	previewCmd, handled := m.checkpointPickerKey("enter")
	if !handled || previewCmd == nil {
		t.Fatal("checkpoint selection did not request a preview")
	}
	drain(m, previewCmd)
	if m.checkpointPicker.preview == nil || len(m.checkpointPicker.preview.RollbackPatches) != 1 {
		t.Fatalf("preview missing rollback impact: %#v", m.checkpointPicker)
	}
	if cmd, _ := m.checkpointPickerKey("enter"); cmd != nil {
		t.Fatal("restore ran without explicit y arm")
	}
	_, _ = m.checkpointPickerKey("y")
	restoreCmd, _ := m.checkpointPickerKey("enter")
	if restoreCmd == nil {
		t.Fatal("armed restore did not run")
	}
	drain(m, restoreCmd)
	if m.checkpointPicker == nil || m.checkpointPicker.restored == nil ||
		!strings.Contains(transcriptText(m), "model context rolled back; audit transcript retained") {
		t.Fatalf("restore did not remain paused and report exact semantics: picker=%#v transcript=%s", m.checkpointPicker, transcriptText(m))
	}
	if node := m.tasks.nodes["tsk"]; node == nil || node.Status != "paused" || m.tasks.activeCount() != 0 {
		t.Fatalf("restored task projection = %#v active=%d", node, m.tasks.activeCount())
	}
	restoreCall := caller.last()
	if restoreCall.method != "session.checkpoint.restore" || restoreCall.params["confirmed"] != true {
		t.Fatalf("restore confirmation RPC = %+v", restoreCall)
	}
	resumeCmd, handled := m.checkpointPickerKey("enter")
	if !handled || resumeCmd == nil {
		t.Fatal("restored task did not offer explicit resume")
	}
	drain(m, resumeCmd)
	if m.checkpointPicker != nil || m.inFlightTaskID != "tsk" {
		t.Fatalf("resume result: picker=%#v active=%q", m.checkpointPicker, m.inFlightTaskID)
	}
	if last := caller.last(); last.method != "task.resume" || last.params["task_id"] != "tsk" {
		t.Fatalf("resume RPC = %+v", last)
	}
}

func TestKeymapPickerPersistsThenAtomicallySwapsBindings(t *testing.T) {
	var savedAction string
	var savedKeys []string
	m, err := NewChecked(Options{
		Theme: theme.New(theme.Mono),
		KeymapUpdater: func(action string, keys []string, remove bool) ([]KeyBindingOverride, error) {
			savedAction, savedKeys = action, append([]string(nil), keys...)
			return []KeyBindingOverride{{Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: keys}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.openKeymapEditor()
	if cmd, _ := m.keymapEditorKey("enter"); cmd != nil {
		t.Fatal("opening keymap action menu should be synchronous")
	}
	_, _ = m.keymapEditorKey("r")
	cmd, _ := m.keymapEditorKey("f2")
	if cmd == nil {
		t.Fatal("captured key was not persisted")
	}
	drain(m, cmd)
	if savedAction != string(ActionGlobalHelp) || len(savedKeys) != 1 || savedKeys[0] != "f2" {
		t.Fatalf("saved keymap = %s %v", savedAction, savedKeys)
	}
	if !m.keys.matches(KeyContextGlobal, ActionGlobalHelp, "f2") || m.keys.matches(KeyContextGlobal, ActionGlobalHelp, "f1") {
		t.Fatal("runtime keymap did not atomically swap to persisted binding")
	}
}

func TestKeymapPickerRecordsTwoAndThreeStepChords(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []string
		want  string
	}{
		{name: "two-step", steps: []string{"ctrl+x", "ctrl+k", "enter"}, want: "ctrl+x ctrl+k"},
		{name: "three-step", steps: []string{"ctrl+x", "ctrl+k", "ctrl+l"}, want: "ctrl+x ctrl+k ctrl+l"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var saved []string
			m, err := NewChecked(Options{
				Theme: theme.New(theme.Mono),
				KeymapUpdater: func(action string, keys []string, remove bool) ([]KeyBindingOverride, error) {
					saved = append([]string(nil), keys...)
					return []KeyBindingOverride{{Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: keys}}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			m.openKeymapEditor()
			_, _ = m.keymapEditorKey("enter")
			_, _ = m.keymapEditorKey("r")
			var persist tea.Cmd
			for _, step := range tc.steps {
				cmd, handled := m.keymapEditorKey(step)
				if !handled {
					t.Fatalf("capture step %q was not handled", step)
				}
				if cmd != nil {
					persist = cmd
				}
			}
			if persist == nil {
				t.Fatal("completed chord did not schedule persistence")
			}
			drain(m, persist)
			if len(saved) != 1 || saved[0] != tc.want {
				t.Fatalf("saved chord = %#v, want %q", saved, tc.want)
			}
			if !m.keys.matches(KeyContextGlobal, ActionGlobalHelp, tc.want) {
				t.Fatalf("runtime keymap did not install %q", tc.want)
			}
		})
	}
}

func TestKeymapChordCaptureShowsPendingAndCancelsOnTimeoutOrEscape(t *testing.T) {
	m, err := NewChecked(Options{Theme: theme.New(theme.Mono)})
	if err != nil {
		t.Fatal(err)
	}
	m.openKeymapEditor()
	_, _ = m.keymapEditorKey("enter")
	_, _ = m.keymapEditorKey("r")
	timeout, handled := m.keymapEditorKey("ctrl+x")
	if !handled || timeout == nil || len(m.keymapEditor.capture) != 1 {
		t.Fatalf("pending capture = handled=%v timeout=%v state=%#v", handled, timeout != nil, m.keymapEditor)
	}
	if view := ansi.Strip(m.keymapEditorView()); !strings.Contains(view, "Pending chord: ctrl+x") ||
		!strings.Contains(view, m.keys.label(KeyContextKeymapCapture, ActionKeymapCaptureCommit)) {
		t.Fatalf("pending capture is not visible with runtime labels:\n%s", view)
	}
	m.Update(timeout())
	if m.keymapEditor.mode != keymapChooseAction || len(m.keymapEditor.capture) != 0 ||
		!strings.Contains(m.keymapEditor.status, "timed out") {
		t.Fatalf("capture timeout state = %#v", m.keymapEditor)
	}

	m.beginKeymapCapture(false)
	_, _ = m.keymapEditorKey("ctrl+x")
	_, _ = m.keymapEditorKey("esc")
	if m.keymapEditor.mode != keymapChooseAction || len(m.keymapEditor.capture) != 0 ||
		!strings.Contains(m.keymapEditor.status, "cancelled") {
		t.Fatalf("capture Esc state = %#v", m.keymapEditor)
	}
}

func TestKeymapEditorDispatchAndHintsFollowRuntimeBindings(t *testing.T) {
	m, err := NewChecked(Options{
		Theme: theme.New(theme.Mono),
		Keybindings: []KeyBindingOverride{
			{Context: KeyContextKeymap, Action: ActionKeymapEdit, Keys: []string{"f2"}},
			{Context: KeyContextKeymap, Action: ActionKeymapClose, Keys: []string{"f3"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.openKeymapEditor()
	if view := ansi.Strip(m.keymapEditorView()); !strings.Contains(view, "[f2] edit") || !strings.Contains(view, "[f3] close") {
		t.Fatalf("keymap browse hints ignored runtime bindings:\n%s", view)
	}
	if _, handled := m.keymapEditorKey("enter"); !handled || m.keymapEditor.mode != keymapBrowse {
		t.Fatal("old hard-coded Enter still opened the action menu")
	}
	if _, handled := m.keymapEditorKey("f2"); !handled || m.keymapEditor.mode != keymapChooseAction {
		t.Fatal("runtime edit binding did not open the action menu")
	}
}

func TestKeymapRejectsPrintableBindingThatWouldStealComposerText(t *testing.T) {
	_, err := NewChecked(Options{Theme: theme.New(theme.Mono), Keybindings: []KeyBindingOverride{{
		Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: []string{"x"},
	}}})
	if err == nil || !strings.Contains(err.Error(), "shadows normal composer text input") {
		t.Fatalf("printable global binding error = %v", err)
	}
}

func TestChordDispatchTimeoutCancelAndUnmatchedReplay(t *testing.T) {
	m, err := NewChecked(Options{Theme: theme.New(theme.Mono), Keybindings: []KeyBindingOverride{{
		Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: []string{"ctrl+x ctrl+h"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	_, timeout := m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if timeout == nil || m.chord.hint != "ctrl+x ..." || m.helpOpen {
		t.Fatalf("chord prefix state = hint=%q timeout=%v help=%v", m.chord.hint, timeout != nil, m.helpOpen)
	}
	m.Update(tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	if !m.helpOpen || len(m.chord.parts) != 0 {
		t.Fatalf("complete chord did not dispatch help: help=%v chord=%#v", m.helpOpen, m.chord)
	}
	m.closeHelp()

	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Text: "z", Code: 'z'})
	if got := m.input.Value(); got != "z" || len(m.chord.parts) != 0 {
		t.Fatalf("unmatched chord did not replay final key: input=%q chord=%#v", got, m.chord)
	}
	m.input.Reset()
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	generation := m.chord.generation
	m.Update(chordTimeoutMsg{generation: generation})
	if len(m.chord.parts) != 0 || m.chord.hint != "" {
		t.Fatalf("chord timeout did not clear state: %#v", m.chord)
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if len(m.chord.parts) != 0 || m.rewindPrimed {
		t.Fatalf("Esc did not cancel pending chord cleanly: chord=%#v rewind=%v", m.chord, m.rewindPrimed)
	}
}

func TestEditorChordRunsThroughTextareaBeforeTextInsertion(t *testing.T) {
	m, err := NewChecked(Options{Theme: theme.New(theme.Mono), Keybindings: []KeyBindingOverride{{
		Context: KeyContextEditor, Action: ActionEditorMoveLeft, Keys: []string{"ctrl+x ctrl+b"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	m.input.SetValue("ab")
	want := m.input.Column() - 1
	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	m.Update(tea.KeyPressMsg{Code: 'b', Mod: tea.ModCtrl})
	if m.input.Value() != "ab" || m.input.Column() != want {
		t.Fatalf("editor chord result = value %q column %d, want column %d", m.input.Value(), m.input.Column(), want)
	}
}

func TestKeymapRejectsAmbiguousSingleKeyChordPrefix(t *testing.T) {
	_, err := NewChecked(Options{Theme: theme.New(theme.Mono), Keybindings: []KeyBindingOverride{
		{Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: []string{"ctrl+x"}},
		{Context: KeyContextGlobal, Action: ActionGlobalTranscript, Keys: []string{"ctrl+x ctrl+r"}},
	}})
	if err == nil || !strings.Contains(err.Error(), "ambiguous prefix") {
		t.Fatalf("ambiguous chord prefix error = %v", err)
	}
}

func TestViewCanPreserveNativeScrollbackAndEnablesWheelEvents(t *testing.T) {
	m, err := NewChecked(Options{Theme: theme.New(theme.Mono), NoAlternateScreen: true})
	if err != nil {
		t.Fatal(err)
	}
	view := m.View()
	if view.AltScreen {
		t.Fatal("no-alt-screen model still requested the alternate buffer")
	}
	if view.MouseMode != tea.MouseModeCellMotion || view.OnMouse == nil {
		t.Fatalf("mouse wheel path is not enabled: mode=%v callback=%v", view.MouseMode, view.OnMouse != nil)
	}
}

func TestHistoryRecallDefaultsToCurrentWorkspaceScope(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{"history.recent": map[string]any{"entries": []string{}}}}
	m, _ := newTestModel(caller)
	cmd := m.loadRecentHistory(caller)
	_ = cmd()
	last := caller.last()
	if last.method != "history.recent" || last.params["scope"] != "workspace" || last.params["session_id"] != m.sessionID {
		t.Fatalf("history scope call = %+v", last)
	}
}
