package tui

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestKeymapRejectsSameContextConflictWithActionableError(t *testing.T) {
	_, err := NewChecked(Options{
		Theme: theme.New(theme.Mono),
		Keybindings: []KeyBindingOverride{
			{Context: KeyContextGlobal, Action: ActionGlobalRedraw, Keys: []string{"f1"}},
		},
	})
	if err == nil {
		t.Fatal("same-context conflict was accepted")
	}
	for _, want := range []string{"context \"global\"", "f1", "global.help", "global.redraw"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("conflict error missing %q: %v", want, err)
		}
	}

	_, err = NewChecked(Options{
		Theme: theme.New(theme.Mono),
		Keybindings: []KeyBindingOverride{
			{Context: KeyContextQuestion, Action: ActionQuestionPrevious, Keys: []string{"1"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "question.answer") {
		t.Fatalf("range conflict was not detected: %v", err)
	}

	_, err = NewChecked(Options{
		Theme: theme.New(theme.Mono),
		Keybindings: []KeyBindingOverride{
			{Context: KeyContextApproval, Action: ActionApprovalSession, Keys: []string{"1"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "approval.once") {
		t.Fatalf("approval numeric conflict was not detected: %v", err)
	}
}

func TestKeymapCanonicalizesModifierOrderAndRejectsUnreachableSpecs(t *testing.T) {
	keys, err := newRuntimeKeymap([]KeyBindingOverride{{
		Context: KeyContextHistory,
		Action:  ActionHistoryCancel,
		Keys:    []string{"shift+ctrl+x"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !keys.matches(KeyContextHistory, ActionHistoryCancel, "ctrl+shift+x") {
		t.Fatal("modifier order was not canonicalized to Bubble Tea event order")
	}
	for _, spec := range []string{"ctrl+ctrl+x", "banana+x", "ctrl+not-a-key", "ctrl+", "x ctrl+k", "ctrl+x ctrl+k ctrl+l ctrl+m"} {
		_, err := newRuntimeKeymap([]KeyBindingOverride{{
			Context: KeyContextHistory,
			Action:  ActionHistoryCancel,
			Keys:    []string{spec},
		}})
		if err == nil {
			t.Fatalf("unreachable key spec %q was accepted", spec)
		}
	}
}

func TestKeymapFoldsTraditionalTerminalEquivalentKeys(t *testing.T) {
	keys, err := newRuntimeKeymap([]KeyBindingOverride{{
		Context: KeyContextHistory,
		Action:  ActionHistoryAccept,
		Keys:    []string{"ctrl+["},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !keys.matches(KeyContextHistory, ActionHistoryAccept, "esc") {
		t.Fatal("Ctrl+[ and Esc were not treated as the same terminal key")
	}

	cases := []struct {
		name   string
		action KeyAction
		key    string
		want   string
	}{
		{"enter-ctrl-m", ActionGlobalHelp, "ctrl+m", "composer.submit"},
		{"tab-ctrl-i", ActionGlobalHelp, "ctrl+i", "composer.queue"},
		{"esc-ctrl-bracket", ActionGlobalRedraw, "ctrl+[", "chat.interrupt"},
		{"backspace-ctrl-h", ActionGlobalHelp, "ctrl+h", "editor.delete-backward"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newRuntimeKeymap([]KeyBindingOverride{{
				Context: KeyContextGlobal, Action: tc.action, Keys: []string{tc.key},
			}})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "rebind one action") {
				t.Fatalf("terminal-equivalent conflict was not actionable: %v", err)
			}
		})
	}
}

func TestEditorSemanticBindingsInstallIntoTextarea(t *testing.T) {
	keys, err := newRuntimeKeymap([]KeyBindingOverride{{
		Context: KeyContextEditor,
		Action:  ActionEditorMoveLeft,
		Keys:    []string{"f12"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	input := textarea.New()
	installEditorKeymap(&input, keys)

	cases := []struct {
		name   string
		got    []string
		action KeyAction
	}{
		{"move-left", input.KeyMap.CharacterBackward.Keys(), ActionEditorMoveLeft},
		{"move-right", input.KeyMap.CharacterForward.Keys(), ActionEditorMoveRight},
		{"move-up", input.KeyMap.LinePrevious.Keys(), ActionEditorMoveUp},
		{"move-down", input.KeyMap.LineNext.Keys(), ActionEditorMoveDown},
		{"word-left", input.KeyMap.WordBackward.Keys(), ActionEditorMoveWordLeft},
		{"word-right", input.KeyMap.WordForward.Keys(), ActionEditorMoveWordRight},
		{"line-start", input.KeyMap.LineStart.Keys(), ActionEditorMoveLineStart},
		{"line-end", input.KeyMap.LineEnd.Keys(), ActionEditorMoveLineEnd},
		{"delete-backward", input.KeyMap.DeleteCharacterBackward.Keys(), ActionEditorDeleteBackward},
		{"delete-forward", input.KeyMap.DeleteCharacterForward.Keys(), ActionEditorDeleteForward},
		{"delete-word-backward", input.KeyMap.DeleteWordBackward.Keys(), ActionEditorDeleteWordBackward},
		{"delete-word-forward", input.KeyMap.DeleteWordForward.Keys(), ActionEditorDeleteWordForward},
		{"kill-line-start", input.KeyMap.DeleteBeforeCursor.Keys(), ActionEditorKillLineStart},
		{"kill-line-end", input.KeyMap.DeleteAfterCursor.Keys(), ActionEditorKillLineEnd},
		{"transpose-backward", input.KeyMap.TransposeCharacterBackward.Keys(), ActionEditorTransposeBackward},
		{"yank", input.KeyMap.Paste.Keys(), ActionEditorYank},
		{"newline", input.KeyMap.InsertNewline.Keys(), ActionEditorInsertNewline},
	}
	for _, tc := range cases {
		if want := keys.keys(KeyContextEditor, tc.action); !reflect.DeepEqual(tc.got, want) {
			t.Errorf("%s textarea keys = %#v, want semantic binding %#v", tc.name, tc.got, want)
		}
	}
	if got := input.KeyMap.CharacterBackward.Keys(); !reflect.DeepEqual(got, []string{"f12"}) {
		t.Fatalf("editor override was not installed: %#v", got)
	}
	if got := input.KeyMap.InsertNewline.Keys(); !reflect.DeepEqual(got, []string{"shift+enter", "alt+enter", "ctrl+j"}) {
		t.Fatalf("multiline entry regressed: %#v", got)
	}
}

func TestBindingDescriptorsAndAtomicOverrideSupportPicker(t *testing.T) {
	keys, err := newRuntimeKeymap(nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptors := keys.BindingDescriptors()
	if len(descriptors) == 0 || descriptors[0].Action != ActionGlobalHelp {
		t.Fatalf("binding descriptors are not in stable declaration order: %#v", descriptors)
	}
	descriptors[0].Keys[0] = "mutated"
	if keys.keys(KeyContextGlobal, ActionGlobalHelp)[0] != "f1" {
		t.Fatal("picker descriptor mutated the live keymap")
	}

	next, err := keys.withOverride(KeyBindingOverride{
		Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: []string{"f2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !next.matches(KeyContextGlobal, ActionGlobalHelp, "f2") || !keys.matches(KeyContextGlobal, ActionGlobalHelp, "f1") {
		t.Fatal("atomic override changed the old map or did not build the replacement")
	}
	if _, err := next.withOverride(KeyBindingOverride{
		Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: []string{"enter"},
	}); err == nil {
		t.Fatal("picker replacement accepted a dispatch-path conflict")
	}

	aliased, err := keys.withOverride(KeyBindingOverride{
		Context: KeyContextComposer, Action: ActionComposerNewline, Keys: []string{"f12"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(
		aliased.keys(KeyContextComposer, ActionComposerNewline),
		aliased.keys(KeyContextEditor, ActionEditorInsertNewline),
	) {
		t.Fatal("legacy composer newline drifted from the installed editor binding")
	}
}

func TestKeymapOverrideDrivesDispatchPlaceholderStatusAndHelp(t *testing.T) {
	m, err := NewChecked(Options{
		Theme:  theme.New(theme.Mono),
		Locale: "en",
		Keybindings: []KeyBindingOverride{
			{Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: []string{"f2"}},
			{Context: KeyContextComposer, Action: ActionComposerSubmit, Keys: []string{"ctrl+enter"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if _, handled := m.handleKey("f1"); handled {
		t.Fatal("overridden default help key is still active")
	}
	if _, handled := m.handleKey("f2"); !handled || !m.helpOpen {
		t.Fatal("overridden help key did not open help")
	}
	body := strings.Join(m.helpBodyLines(), "\n")
	for _, want := range []string{"f2", "show keyboard help", "ctrl+enter", "submit or steer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("dynamic help missing %q:\n%s", want, body)
		}
	}
	m.closeHelp()
	view := m.View().Content
	if !strings.Contains(view, "f2 help") || !strings.Contains(m.input.Placeholder, "ctrl+enter submits") {
		t.Fatalf("visible hints drifted from runtime bindings:\n%s\nplaceholder=%q", view, m.input.Placeholder)
	}
}

func TestDispatchPathRejectsOverlappingGlobalHelp(t *testing.T) {
	_, err := NewChecked(Options{
		Theme: theme.New(theme.Mono),
		Keybindings: []KeyBindingOverride{
			{Context: KeyContextGlobal, Action: ActionGlobalHelp, Keys: []string{"enter"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "global.help") || !strings.Contains(err.Error(), "composer.submit") {
		t.Fatalf("global/composer shadowing was not rejected: %v", err)
	}
}

func TestDispatchPathRejectsOverlappingGlobalTranscript(t *testing.T) {
	_, err := NewChecked(Options{
		Theme: theme.New(theme.Mono),
		Keybindings: []KeyBindingOverride{{
			Context: KeyContextGlobal,
			Action:  ActionGlobalTranscript,
			Keys:    []string{"enter"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "global.transcript") || !strings.Contains(err.Error(), "composer.submit") {
		t.Fatalf("global/composer transcript shadowing was not rejected: %v", err)
	}
}

func TestFocusedModalCancelWinsOverOverlappingGlobalRedraw(t *testing.T) {
	m, err := NewChecked(Options{
		Theme: theme.New(theme.Mono),
		Keybindings: []KeyBindingOverride{{
			Context: KeyContextPager,
			Action:  ActionPagerClose,
			Keys:    []string{"ctrl+l"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	m.recordHistory(promptDraft{Text: "previous"})
	if !m.beginHistorySearch() {
		t.Fatal("history search did not open")
	}
	if cmd := m.historySearchKeyPress(tea.KeyPressMsg{Code: tea.KeyEsc}); cmd != nil || m.historySearch != nil {
		t.Fatalf("history cancel lost focused priority: cmd=%v search=%#v", cmd != nil, m.historySearch)
	}

	m.openTranscriptPager()
	if m.transcriptPager == nil {
		t.Fatal("transcript pager did not open")
	}
	if cmd, handled := m.handleKey("ctrl+l"); !handled || cmd != nil || m.transcriptPager != nil {
		t.Fatalf("pager close lost focused priority: handled=%v cmd=%v pager=%#v",
			handled, cmd != nil, m.transcriptPager)
	}
}

func TestDefaultNumericGovernanceBindingsRemainDistinct(t *testing.T) {
	keys, err := newRuntimeKeymap(nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		key    string
		action KeyAction
	}{
		{"1", ActionApprovalOnce}, {"2", ActionApprovalSession},
		{"3", ActionApprovalProject}, {"4", ActionApprovalDeny},
	}
	for _, tc := range cases {
		if !keys.matches(KeyContextApproval, tc.action, tc.key) {
			t.Errorf("approval key %q does not match %s", tc.key, tc.action)
		}
	}

	fc := &fakeCaller{handler: map[string]any{"task.user.answer": nil}}
	m, _ := newTestModel(fc)
	options := make([]questionOption, 9)
	for i := range options {
		options[i] = questionOption{Label: "answer", Value: string(rune('a' + i))}
	}
	m.question = &questionState{QuestionID: "q_numeric", Options: options}
	cmd, handled := m.questionKey("7")
	if !handled || cmd == nil || !m.question.Resolving || m.question.Selected != 6 {
		t.Fatalf("numeric question selection did not choose index 6: handled=%v selected=%d", handled, m.question.Selected)
	}
	drain(m, cmd)
}

func TestHelpIsImmediateAboveScrolledTranscriptAndNarrowViewCanExit(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	for i := 0; i < 80; i++ {
		m.push("history line")
	}
	m.handleKey("pgup")
	offset := m.vp.YOffset()
	cmd, handled := m.handleKey("f1")
	if !handled || cmd != nil || !m.helpOpen {
		t.Fatal("F1 did not open help synchronously")
	}
	if view := m.View().Content; !strings.Contains(view, "Carina help") || strings.Contains(view, "history line") {
		t.Fatalf("help was not the immediate visible surface:\n%s", view)
	}
	if m.vp.YOffset() != offset {
		t.Fatal("opening help moved the transcript read position")
	}
	m.Update(tea.WindowSizeMsg{Width: 12, Height: 1})
	if _, handled := m.handleKey("esc"); !handled || m.helpOpen {
		t.Fatal("narrow help overlay could not be closed")
	}
}

func TestGovernanceModalStaysAboveHelpAndOwnsOrdinaryKeys(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.showHelp()
	m.Update(permissionRequestEvent("perm_over_help"))
	if view := m.View().Content; !strings.Contains(view, "Approval required") || strings.Contains(view, "Carina help") {
		t.Fatalf("help covered governance modal:\n%s", view)
	}
	if _, handled := m.handleKey("f1"); !handled || !m.helpOpen {
		t.Fatal("governance modal did not consume F1 without changing underlying help state")
	}
}

func TestCtrlCSemanticPriorityAndDraftRecovery(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.cancel": nil}}
	m, clock := newTestModel(fc)
	m.inFlightTaskID = "tsk_running"
	m.suggest = &suggestState{Kind: mentionCommand, Matches: []string{"help"}}
	if _, handled := m.handleKey("ctrl+c"); !handled || m.suggest != nil {
		t.Fatal("search-like suggestion did not consume Ctrl+C first")
	}
	if len(fc.calls) != 0 || m.inFlightTaskID == "" {
		t.Fatal("suggestion Ctrl+C leaked into task cancellation")
	}

	cmd, _ := m.handleKey("ctrl+c")
	drain(m, cmd)
	if len(fc.calls) != 1 || fc.last().method != "task.cancel" {
		t.Fatal("running Ctrl+C did not cancel the task")
	}

	clock.advance(3 * time.Second)
	m.input.SetValue("recover this")
	m.pendingPaste = []string{"pasted\ncontent"}
	if cmd, handled := m.handleKey("ctrl+c"); !handled || cmd != nil {
		t.Fatal("draft Ctrl+C should synchronously clear without quitting")
	}
	if m.input.Value() != "" || len(m.pendingPaste) != 0 {
		t.Fatal("draft Ctrl+C did not clear the complete composer")
	}
	if _, handled := m.handleKey("ctrl+p"); !handled {
		t.Fatal("cleared draft was not recoverable from prompt history")
	}
	if m.input.Value() != "recover this" || len(m.pendingPaste) != 1 {
		t.Fatalf("recovered draft mismatch: text=%q paste=%#v", m.input.Value(), m.pendingPaste)
	}
}

func TestCtrlDStateMatrixAndCtrlLPreservesDraft(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	cmd, handled := m.handleKey("ctrl+d")
	if !handled || cmd == nil {
		t.Fatal("idle empty Ctrl+D must exit")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("idle empty Ctrl+D did not return tea.Quit")
	}

	m.input.SetValue("ab")
	m.input.SetCursorColumn(0)
	m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if got := m.input.Value(); got != "b" {
		t.Fatalf("non-empty Ctrl+D lost textarea delete-forward: %q", got)
	}

	m.input.SetValue("draft")
	m.pendingPaste = []string{"keep\nme"}
	cmd, handled = m.handleKey("ctrl+l")
	if !handled || cmd == nil {
		t.Fatal("Ctrl+L did not request a redraw")
	}
	if _, quit := cmd().(tea.QuitMsg); quit {
		t.Fatal("Ctrl+L unexpectedly quit")
	}
	if m.input.Value() != "draft" || len(m.pendingPaste) != 1 {
		t.Fatal("Ctrl+L changed the draft")
	}

	m.input.Reset()
	m.pendingPaste = nil
	m.inFlightTaskID = "tsk_running"
	if cmd, handled = m.handleKey("ctrl+d"); !handled || cmd != nil {
		t.Fatal("empty Ctrl+D must be inert while a task is running")
	}
	m.inFlightTaskID = ""
	m.approval = &approvalState{DecisionID: "perm_d", Action: "command.exec"}
	if cmd, handled = m.handleKey("ctrl+d"); !handled || cmd != nil {
		t.Fatal("Ctrl+D must not exit through a governance modal")
	}
}

func TestOverriddenInterruptRetainsDoublePressState(t *testing.T) {
	clock := &testClock{now: time.Unix(100, 0)}
	m, err := NewChecked(Options{
		Now: func() time.Time { return clock.now },
		Keybindings: []KeyBindingOverride{{
			Context: KeyContextGlobal,
			Action:  ActionGlobalInterrupt,
			Keys:    []string{"ctrl+x"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}); cmd != nil {
		t.Fatal("first overridden interrupt should only arm exit")
	}
	if got := transcriptText(m); !strings.Contains(got, "press ctrl+x again") || strings.Contains(got, "press ctrl+c again") {
		t.Fatalf("interrupt hint drifted from override: %q", got)
	}
	clock.advance(time.Second)
	if _, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}); cmd == nil {
		t.Fatal("second overridden interrupt did not exit")
	} else if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("second overridden interrupt did not return tea.Quit")
	}
}

func TestResolvingGovernanceStillAllowsInterruptAndRedraw(t *testing.T) {
	for _, tc := range []struct {
		name string
		open func(*Model)
	}{
		{"approval", func(m *Model) { m.approval = &approvalState{DecisionID: "p", Resolving: true} }},
		{"question", func(m *Model) { m.question = &questionState{QuestionID: "q", Resolving: true} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := &fakeCaller{handler: map[string]any{"task.cancel": nil}}
			m, _ := newTestModel(fc)
			m.inFlightTaskID = "tsk_running"
			tc.open(m)
			cmd, handled := m.handleKey("ctrl+l")
			if !handled || cmd == nil {
				t.Fatal("resolving governance swallowed redraw")
			}
			cmd, handled = m.handleKey("ctrl+c")
			if !handled || cmd == nil {
				t.Fatal("resolving governance swallowed interrupt")
			}
			drain(m, cmd)
			if len(fc.calls) != 1 || fc.calls[0].method != "task.cancel" {
				t.Fatalf("interrupt calls = %#v", fc.calls)
			}
		})
	}
}

func TestHelpMouseWheelOwnsVisibleOverlay(t *testing.T) {
	m, _ := newTestModel(nil)
	for i := 0; i < 40; i++ {
		m.push("line")
	}
	m.vp.PageUp()
	m.showHelp()
	transcriptOffset := m.vp.YOffset()
	m.handleMouseWheel(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if m.helpScroll == 0 || m.vp.YOffset() != transcriptOffset {
		t.Fatalf("help wheel leaked to transcript: help=%d transcript=%d->%d", m.helpScroll, transcriptOffset, m.vp.YOffset())
	}
}

func TestHistoryAndTranscriptSurfacesKeepGlobalRedraw(t *testing.T) {
	m, _ := newTestModel(nil)
	m.history = []promptDraft{{Text: "history"}}
	startHistorySearch(t, m)
	if cmd, handled := m.handleKey("ctrl+l"); !handled || cmd == nil {
		t.Fatal("history search swallowed global redraw")
	}
	composerKey(t, m, "esc")
	m.handleKey("alt+r")
	if cmd, handled := m.handleKey("ctrl+l"); !handled || cmd == nil {
		t.Fatal("transcript pager swallowed global redraw")
	}
}

func TestCriticalKeybindingsCannotBeUnbound(t *testing.T) {
	_, err := NewChecked(Options{Keybindings: []KeyBindingOverride{{
		Context: KeyContextHistory,
		Action:  ActionHistoryCancel,
		Keys:    []string{},
	}}})
	if err == nil || !strings.Contains(err.Error(), "cannot be unbound") {
		t.Fatalf("critical unbind was accepted: %v", err)
	}
}

func TestParseKeyBindingOverridesUsesActionContext(t *testing.T) {
	overrides, err := ParseKeyBindingOverrides(map[string][]string{
		"composer.submit": {"ctrl+enter"},
		"global.help":     {"ctrl+h"},
	})
	if err != nil || len(overrides) != 2 {
		t.Fatalf("parse overrides = %#v, %v", overrides, err)
	}
	if overrides[0].Context != KeyContextComposer || overrides[0].Action != ActionComposerSubmit {
		t.Fatalf("first override = %#v", overrides[0])
	}
}
