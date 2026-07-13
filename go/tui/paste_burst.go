package tui

import (
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
)

// Some terminals deliver a paste as rapid KeyPressMsg values instead of one
// bracketed PasteMsg. Keep this detector deliberately conservative: text is
// inserted immediately, and only structural keys are reinterpreted once a
// burst is established. This avoids any echo delay for normal typing and IME.
const (
	pasteBurstCharInterval = 8 * time.Millisecond
	pasteBurstEnterWindow  = 120 * time.Millisecond
	pasteBurstMinChars     = 2
)

type pasteBurstState struct {
	lastPlain       time.Time
	hasLastPlain    bool
	consecutive     int
	structuralUntil time.Time
}

func (s *pasteBurstState) observeASCII(now time.Time, count int) {
	if count < 1 {
		return
	}
	rapid := s.hasLastPlain && !now.Before(s.lastPlain) && now.Sub(s.lastPlain) <= pasteBurstCharInterval
	if rapid {
		s.consecutive += count
	} else {
		s.consecutive = count
	}
	s.lastPlain = now
	s.hasLastPlain = true
	if s.consecutive >= pasteBurstMinChars {
		s.extend(now)
	}
}

func (s *pasteBurstState) structuralKeyIsText(now time.Time) bool {
	return !s.structuralUntil.IsZero() && !now.After(s.structuralUntil)
}

func (s *pasteBurstState) extend(now time.Time) {
	s.structuralUntil = now.Add(pasteBurstEnterWindow)
}

func (s *pasteBurstState) reset() {
	*s = pasteBurstState{}
}

// plainASCIITextCount accepts text-producing keys without command modifiers.
// Shift and lock modifiers are allowed because Key.Text already contains the
// resulting printable character. Repeats are excluded so holding a key cannot
// accidentally turn the following Enter into a newline.
func plainASCIITextCount(msg tea.KeyPressMsg) (int, bool) {
	key := msg.Key()
	if key.IsRepeat || key.Text == "" || key.Mod.Contains(tea.ModCtrl) ||
		key.Mod.Contains(tea.ModAlt) || key.Mod.Contains(tea.ModMeta) ||
		key.Mod.Contains(tea.ModHyper) || key.Mod.Contains(tea.ModSuper) {
		return 0, false
	}
	count := 0
	for _, r := range key.Text {
		if r < 0x20 || r > 0x7e {
			return 0, false
		}
		count++
	}
	return count, count > 0 && utf8.ValidString(key.Text)
}

// handlePasteBurstKey returns handled only for structural keys captured as
// pasted text. All printable text, including non-ASCII/IME commits, continues
// through the normal textarea path in the same Update call.
func (m *Model) handlePasteBurstKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	now := m.now()
	if count, ok := plainASCIITextCount(msg); ok {
		m.pasteBurst.observeASCII(now, count)
		return nil, false
	}

	key := msg.Key()
	if (key.Code == tea.KeyEnter || key.Code == tea.KeyKpEnter || key.Code == tea.KeyTab) &&
		m.pasteBurst.structuralKeyIsText(now) {
		text := "\n"
		if key.Code == tea.KeyTab {
			text = "\t"
		}
		m.input.InsertString(text)
		m.pasteBurst.extend(now)
		if m.suggest != nil {
			m.closeSuggest()
		}
		m.layout()
		return m.refreshSuggestTrigger(), true
	}

	// Non-ASCII text is commonly an IME commit. It must be visible
	// immediately and must not inherit an ASCII burst's Enter semantics.
	m.pasteBurst.reset()
	return nil, false
}
