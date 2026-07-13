package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestQuestionMarkRemainsPromptTextAndF1ShowsHelp(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(tea.KeyPressMsg{Text: "?", Code: '?'})
	if got := m.input.Value(); got != "?" {
		t.Fatalf("question mark was intercepted: input=%q", got)
	}
	if _, handled := m.handleKey("f1"); !handled {
		t.Fatal("f1 did not open help")
	}
}

func TestSuggestionUsesStandardNavigationAndCompletion(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("/he")
	m.suggest = &suggestState{
		Kind: mentionCommand, Query: "he", Matches: []string{"help", "keys"}, Start: 0, Row: 0,
	}

	if _, handled := m.handleKey("down"); !handled || m.suggest.Selected != 1 {
		t.Fatalf("down did not move suggestion selection: handled=%v selected=%d", handled, m.suggest.Selected)
	}
	if _, handled := m.handleKey("up"); !handled || m.suggest.Selected != 0 {
		t.Fatalf("up did not move suggestion selection: handled=%v selected=%d", handled, m.suggest.Selected)
	}
	if _, handled := m.handleKey("enter"); !handled {
		t.Fatal("enter did not complete the selected suggestion")
	}
	if got := m.input.Value(); got != "/help " {
		t.Fatalf("enter submitted the partial command instead of completing it: %q", got)
	}
	if m.suggest != nil {
		t.Fatal("completion did not close the suggestion panel")
	}
}

func TestSuggestionDoesNotConsumePlainDigits(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("@issue")
	m.suggest = &suggestState{Kind: mentionFile, Matches: []string{"issue-123.md"}, Start: 0, Row: 0}
	if _, handled := m.handleKey("1"); handled {
		t.Fatal("plain digit was consumed as a suggestion accelerator")
	}
}

func TestConstrainedSuggestionAlwaysShowsSelectedCandidate(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(tea.WindowSizeMsg{Width: 48, Height: 8})
	m.pendingPaste = []string{"one\ntwo", "three\nfour"}
	m.suggest = &suggestState{
		Kind:     mentionCommand,
		Matches:  []string{"one", "two", "three", "four", "five", "six", "seven", "selected"},
		Selected: 7,
	}
	m.layout()
	if m.root.suggestLines == 0 {
		t.Fatal("visible suggestion must take priority over paste previews")
	}
	visible := strings.Join(m.visibleSuggestPanelLines(m.root.suggestLines), "\n")
	if !strings.Contains(visible, "> /selected") {
		t.Fatalf("selected candidate is off-screen:\n%s", visible)
	}
}
