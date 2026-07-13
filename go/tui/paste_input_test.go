package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestPasteDoesNotMutateComposerBehindGovernanceModal(t *testing.T) {
	modalCases := []struct {
		name string
		open func(*Model)
	}{
		{
			name: "approval",
			open: func(m *Model) { m.approval = &approvalState{DecisionID: "d1"} },
		},
		{
			name: "approval resolving",
			open: func(m *Model) { m.approval = &approvalState{DecisionID: "d1", Resolving: true} },
		},
		{
			name: "question",
			open: func(m *Model) { m.question = &questionState{QuestionID: "q1"} },
		},
		{
			name: "question resolving",
			open: func(m *Model) { m.question = &questionState{QuestionID: "q1", Resolving: true} },
		},
	}
	for _, modal := range modalCases {
		for _, paste := range []struct {
			name    string
			content string
		}{
			{name: "single line", content: "hidden"},
			{name: "multiple lines", content: "hidden\nsecond"},
		} {
			t.Run(modal.name+"/"+paste.name, func(t *testing.T) {
				m, _ := newTestModel(nil)
				m.input.SetValue("visible draft")
				m.pendingPaste = []string{"existing paste"}
				m.pasteBurst.observeASCII(m.now(), 2)
				modal.open(m)

				_, cmd := m.Update(tea.PasteMsg{Content: paste.content})
				if cmd != nil {
					t.Fatal("paste behind a modal scheduled work")
				}
				if got := m.input.Value(); got != "visible draft" {
					t.Fatalf("background composer changed to %q", got)
				}
				if len(m.pendingPaste) != 1 || m.pendingPaste[0] != "existing paste" {
					t.Fatalf("background paste drafts changed: %#v", m.pendingPaste)
				}
				if m.pasteBurst.structuralKeyIsText(m.now()) {
					t.Fatal("modal paste left a stale burst window")
				}
			})
		}
	}
}

func TestSingleLinePasteRefreshesSlashSuggestions(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	_, cmd := m.Update(tea.PasteMsg{Content: "/he"})
	if cmd == nil {
		t.Fatal("paste opening a slash trigger did not schedule suggestions")
	}
	settleCurrentSuggestion(t, m)
	if m.suggest == nil || !containsString(m.suggest.Matches, "help") {
		t.Fatalf("slash suggestion did not open for /he: %#v", m.suggest)
	}

	oldGen := m.suggestGen
	m.suggest.Selected = len(m.suggest.Matches) - 1
	_, cmd = m.Update(tea.PasteMsg{Content: "l"})
	if cmd == nil || m.suggestGen <= oldGen {
		t.Fatal("paste changing a slash query did not refresh its generation")
	}
	if m.suggest != nil {
		t.Fatalf("stale slash selection remained visible during refresh: %#v", m.suggest)
	}
	settleCurrentSuggestion(t, m)
	if m.suggest == nil || m.suggest.Query != "hel" || !containsString(m.suggest.Matches, "help") {
		t.Fatalf("slash suggestion did not refresh for /hel: %#v", m.suggest)
	}

	_, cmd = m.Update(tea.PasteMsg{Content: " "})
	if cmd != nil || m.suggest != nil {
		t.Fatalf("paste ending the slash trigger left suggestions open: %#v", m.suggest)
	}
}

func TestSingleLinePasteRefreshesMentionSuggestions(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"workspace.tree": []treeEntry{{Path: "main.go"}, {Path: "manual.md"}},
		"agent.list":     map[string]any{"agents": []map[string]any{{"name": "maintainer"}}},
	}}
	m, _ := newTestModel(fc)
	_, cmd := m.Update(tea.PasteMsg{Content: "@ma"})
	if cmd == nil {
		t.Fatal("paste opening a mention trigger did not schedule suggestions")
	}
	settleCurrentSuggestion(t, m)
	if m.suggest == nil || !containsString(m.suggest.Matches, "main.go") {
		t.Fatalf("mention suggestion did not open for @ma: %#v", m.suggest)
	}

	oldGen := m.suggestGen
	m.suggest.Selected = len(m.suggest.Matches) - 1
	_, cmd = m.Update(tea.PasteMsg{Content: "i"})
	if cmd == nil || m.suggestGen <= oldGen {
		t.Fatal("paste changing a mention query did not refresh its generation")
	}
	if m.suggest != nil {
		t.Fatalf("stale mention selection remained visible during refresh: %#v", m.suggest)
	}
	settleCurrentSuggestion(t, m)
	if m.suggest == nil || m.suggest.Query != "mai" || !containsString(m.suggest.Matches, "main.go") {
		t.Fatalf("mention suggestion did not refresh for @mai: %#v", m.suggest)
	}

	_, cmd = m.Update(tea.PasteMsg{Content: " "})
	if cmd != nil || m.suggest != nil {
		t.Fatalf("paste ending the mention trigger left suggestions open: %#v", m.suggest)
	}
}

func TestRapidASCIIEnterIsInsertedAsPasteNewline(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_burst"},
	}}
	m, clock := newTestModel(fc)
	m.Update(keyText("a"))
	clock.advance(time.Millisecond)
	m.Update(keyText("b"))
	clock.advance(time.Millisecond)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	if cmd != nil {
		t.Fatal("Enter inside a paste burst attempted to submit")
	}
	if got := m.input.Value(); got != "ab\n" {
		t.Fatalf("burst Enter produced %q, want a literal newline", got)
	}
	if len(fc.calls) != 0 {
		t.Fatalf("burst Enter reached RPC: %#v", fc.calls)
	}

	clock.advance(pasteBurstEnterWindow + time.Millisecond)
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter after the burst window did not submit")
	}
	drain(m, cmd)
	if len(fc.calls) != 1 || fc.calls[0].method != "task.submit" {
		t.Fatalf("normal Enter after burst called %#v", fc.calls)
	}
}

func TestMultiCharacterKeyEventStartsPasteBurst(t *testing.T) {
	m, clock := newTestModel(nil)
	m.Update(tea.KeyPressMsg{Text: "pasted", Code: tea.KeyExtended})
	clock.advance(time.Millisecond)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.input.Value(); got != "pasted\n" {
		t.Fatalf("multi-character key event plus Enter = %q, want pasted newline", got)
	}
}

func TestNonASCIIIMECommitIsImmediateAndDoesNotSuppressEnter(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_ime"},
	}}
	m, _ := newTestModel(fc)
	// Establish an ASCII burst first: the IME commit must break this window.
	m.Update(keyText("a"))
	m.Update(keyText("b"))
	m.Update(tea.KeyPressMsg{Text: "你", Code: '你'})
	if got := m.input.Value(); got != "ab你" {
		t.Fatalf("IME commit was not visible in the same Update: %q", got)
	}

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter after an IME commit was incorrectly retained as burst text")
	}
	if got := m.input.Value(); got != "ab你" {
		t.Fatalf("Enter after IME inserted a newline: %q", got)
	}
	drain(m, cmd)
}

func TestHumanPacedASCIIStillSubmitsNormally(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_typed"},
	}}
	m, clock := newTestModel(fc)
	m.Update(keyText("a"))
	clock.advance(pasteBurstCharInterval + time.Millisecond)
	m.Update(keyText("b"))
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("human-paced Enter was misclassified as paste")
	}
	drain(m, cmd)
}

func TestPasteBurstTimingBoundaries(t *testing.T) {
	var state pasteBurstState
	t0 := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	state.observeASCII(t0, 1)
	if state.structuralKeyIsText(t0) {
		t.Fatal("one character must not establish a burst")
	}
	state.observeASCII(t0.Add(pasteBurstCharInterval), 1)
	if !state.structuralKeyIsText(t0.Add(pasteBurstCharInterval)) {
		t.Fatal("second character at the inclusive interval boundary did not establish a burst")
	}
	if !state.structuralKeyIsText(t0.Add(pasteBurstCharInterval + pasteBurstEnterWindow)) {
		t.Fatal("structural window should be inclusive at its boundary")
	}
	if state.structuralKeyIsText(t0.Add(pasteBurstCharInterval + pasteBurstEnterWindow + time.Nanosecond)) {
		t.Fatal("structural window remained active past its boundary")
	}
}

func settleCurrentSuggestion(t *testing.T, m *Model) {
	t.Helper()
	row := m.input.Line()
	trigger := detectTrigger(currentLine(m.input.Value(), row), m.input.Column())
	if trigger.Kind == mentionNone {
		t.Fatalf("input %q has no current suggestion trigger", m.input.Value())
	}
	_, fetch := m.Update(suggestDebounceMsg{
		gen:     m.suggestGen,
		trigger: trigger,
		row:     row,
	})
	if fetch == nil {
		t.Fatal("current suggestion debounce did not schedule a fetch")
	}
	m.Update(fetch())
}

func keyText(text string) tea.KeyPressMsg {
	runes := []rune(text)
	code := tea.KeyExtended
	if len(runes) == 1 {
		code = runes[0]
	}
	return tea.KeyPressMsg{Text: text, Code: code}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
