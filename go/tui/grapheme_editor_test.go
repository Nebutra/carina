package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestComposerMovesAcrossExtendedGraphemesAtomically(t *testing.T) {
	for _, grapheme := range []string{
		"😀",       // single code point
		"☺️",      // variation selector
		"👍🏿",      // skin-tone modifier
		"👩🏽‍💻",    // modifier + ZWJ
		"👨‍👩‍👧‍👦", // family ZWJ sequence
		"🇨🇳",      // regional-indicator flag
		"1️⃣",     // keycap
		"e\u0301", // combining mark
	} {
		t.Run(grapheme, func(t *testing.T) {
			m, _ := newTestModel(nil)
			m.input.SetValue("A" + grapheme + "B")

			m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
			wantAfterGrapheme := len([]rune("A" + grapheme))
			if got := m.input.Column(); got != wantAfterGrapheme {
				t.Fatalf("left before suffix = %d, want %d", got, wantAfterGrapheme)
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
			if got := m.input.Column(); got != 1 {
				t.Fatalf("left split grapheme at rune column %d", got)
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyRight})
			if got := m.input.Column(); got != wantAfterGrapheme {
				t.Fatalf("right split grapheme at rune column %d, want %d", got, wantAfterGrapheme)
			}
		})
	}
}

func TestComposerDeletesExtendedGraphemesAtomically(t *testing.T) {
	for _, grapheme := range []string{"☺️", "👍🏿", "👩🏽‍💻", "👨‍👩‍👧‍👦", "🇨🇳", "1️⃣", "e\u0301"} {
		t.Run(grapheme, func(t *testing.T) {
			m, _ := newTestModel(nil)
			original := "A" + grapheme + "B"
			m.input.SetValue(original)
			m.input.SetCursorColumn(len([]rune("A" + grapheme)))
			m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
			if got := m.input.Value(); got != "AB" || m.input.Column() != 1 {
				t.Fatalf("backspace = %q @ %d, want AB @ 1", got, m.input.Column())
			}
			composerKey(t, m, "ctrl+z")
			if got := m.input.Value(); got != original {
				t.Fatalf("undo did not restore grapheme: %q", got)
			}

			m.input.SetCursorColumn(1)
			m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
			if got := m.input.Value(); got != "AB" || m.input.Column() != 1 {
				t.Fatalf("delete = %q @ %d, want AB @ 1", got, m.input.Column())
			}
		})
	}
}

func TestComposerRepairsCaretInsideGraphemeBeforeEditing(t *testing.T) {
	m, _ := newTestModel(nil)
	m.input.SetValue("A👩🏽‍💻B")
	m.input.SetCursorColumn(3) // deliberately inside the extended grapheme
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := m.input.Value(); got != "AB" || m.input.Column() != 1 {
		t.Fatalf("inside-cluster backspace = %q @ %d, want AB @ 1", got, m.input.Column())
	}
}

func TestComposerGraphemeEditingPreservesMultilineSemantics(t *testing.T) {
	m, _ := newTestModel(nil)
	m.input.SetValue("A👩🏽‍💻\n🇨🇳B")
	m.setComposerCaret(1, 0)
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if got := m.input.Value(); got != "A👩🏽‍💻🇨🇳B" || m.input.Column() != len([]rune("A👩🏽‍💻")) {
		t.Fatalf("line merge = %q @ %d", got, m.input.Column())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyDelete})
	if got := m.input.Value(); got != "A👩🏽‍💻B" {
		t.Fatalf("forward delete after merge split flag: %q", got)
	}
}

func TestComposerTransposesWholeGraphemes(t *testing.T) {
	m, _ := newTestModel(nil)
	m.input.SetValue("A👩🏽‍💻B")
	m.input.SetCursorColumn(len([]rune("A👩🏽‍💻")))
	composerKey(t, m, "ctrl+t")
	if got := m.input.Value(); got != "AB👩🏽‍💻" {
		t.Fatalf("transpose split grapheme: %q", got)
	}
}

func TestDropLastGraphemeMatrix(t *testing.T) {
	for _, grapheme := range []string{"☺️", "👍🏿", "👩🏽‍💻", "👨‍👩‍👧‍👦", "🇨🇳", "1️⃣", "e\u0301"} {
		if got := dropLastGrapheme("query" + grapheme); got != "query" {
			t.Errorf("dropLastGrapheme(%q) = %q", grapheme, got)
		}
	}
}
