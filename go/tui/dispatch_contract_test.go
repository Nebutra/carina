package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestComposerDefaultSpaceHomeEndReachTextarea(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("ab")

	m.Update(tea.KeyPressMsg{Text: " ", Code: ' '})
	if got := m.input.Value(); got != "ab " {
		t.Fatalf("space was intercepted by transcript paging: %q", got)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	m.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if got := m.input.Value(); got != "xab " {
		t.Fatalf("Home did not move the composer caret to line start: %q", got)
	}

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	m.Update(tea.KeyPressMsg{Text: "y", Code: 'y'})
	if got := m.input.Value(); got != "xab y" {
		t.Fatalf("End did not move the composer caret to line end: %q", got)
	}
}

func TestComposerRetainsNonConflictingTranscriptKeys(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("draft")
	m.followTail = false

	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if got := m.input.Value(); got != "draft" {
		t.Fatalf("PageDown mutated composer text: %q", got)
	}
	if !m.followTail {
		t.Fatal("PageDown did not retain normal transcript navigation")
	}
}

func TestNormalComposerDoesNotActivateOverlayOnlyPagerChord(t *testing.T) {
	keys, err := newRuntimeKeymap([]KeyBindingOverride{{
		Context: KeyContextPager, Action: ActionPagerClose, Keys: []string{"ctrl+x ctrl+q"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := newTestModel(&fakeCaller{})
	m.keys = keys

	m.Update(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if len(m.chord.parts) != 0 {
		t.Fatalf("overlay-only pager chord captured normal composer input: %v", m.chord.parts)
	}
	m.Update(tea.KeyPressMsg{Text: "q", Code: 'q'})
	if got := m.input.Value(); got != "q" {
		t.Fatalf("pager chord leaked literal or swallowed composer text: %q", got)
	}
}

func TestPrintableSuggestionOverrideIsRejected(t *testing.T) {
	_, err := newRuntimeKeymap([]KeyBindingOverride{{
		Context: KeyContextSuggestion, Action: ActionSuggestionNext, Keys: []string{"x"},
	}})
	if err == nil || !strings.Contains(err.Error(), "shadows normal composer text input") {
		t.Fatalf("expected printable-input validation, got %v", err)
	}
}

func TestPrintablePagerOverrideRemainsOverlayOnly(t *testing.T) {
	keys, err := newRuntimeKeymap([]KeyBindingOverride{{
		Context: KeyContextPager, Action: ActionPagerToggleDetail, Keys: []string{"x"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	m, _ := newTestModel(&fakeCaller{})
	m.keys = keys
	m.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if got := m.input.Value(); got != "x" {
		t.Fatalf("printable pager override stole normal composer text: %q", got)
	}
}

func TestModifierAliasesNormalizeToRuntimeVocabulary(t *testing.T) {
	tests := map[string]string{
		"control-k": "ctrl+k", "option+left": "alt+left", "opt-f": "alt+f",
		"cmd+k": "super+k", "command-k": "super+k", "win+k": "super+k",
	}
	for raw, want := range tests {
		got, err := normalizeKeySpec(raw)
		if err != nil || got != want {
			t.Errorf("normalizeKeySpec(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
}
