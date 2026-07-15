package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const chordTimeout = 1200 * time.Millisecond

type chordState struct {
	parts      []string
	generation int
	hint       string
}

type chordTimeoutMsg struct {
	generation int
	capture    bool
}

// resolveChordKey runs before modal dispatch and textarea.Update. It consumes
// a valid prefix, returns a complete chord as one semantic key, cancels on Esc,
// and replays an unmatched final key through the ordinary input path.
func (m *Model) resolveChordKey(raw string) (resolved string, cmd tea.Cmd, consumed bool) {
	if m.keymapEditor != nil && m.keymapEditor.mode == keymapCapture {
		return raw, nil, false
	}
	key, err := normalizeSingleKeySpec(raw)
	if err != nil {
		return raw, nil, false
	}
	key = terminalKeyIdentity(key)
	if len(m.chord.parts) > 0 {
		if key == "esc" {
			m.clearChord()
			return "", nil, true
		}
		test := append(append([]string(nil), m.chord.parts...), key)
		exact, longer := m.chordMatches(test)
		switch {
		case longer:
			m.setChord(test)
			return "", m.chordTimeoutCmd(), true
		case exact:
			m.clearChord()
			return strings.Join(test, " "), nil, false
		default:
			m.clearChord()
			return raw, nil, false
		}
	}
	_, longer := m.chordMatches([]string{key})
	if !longer {
		return raw, nil, false
	}
	m.setChord([]string{key})
	return "", m.chordTimeoutCmd(), true
}

func (m *Model) chordMatches(prefix []string) (exact, longer bool) {
	for _, binding := range m.keys.order {
		if !m.chordBindingActive(binding) {
			continue
		}
		for _, configured := range binding.Keys {
			parts := strings.Fields(terminalKeyIdentity(configured))
			if !chordPartsPrefix(prefix, parts) {
				continue
			}
			switch {
			case len(parts) == len(prefix):
				exact = true
			case len(parts) > len(prefix):
				longer = true
			}
		}
	}
	return exact, longer
}

func (m *Model) chordBindingActive(binding keyBinding) bool {
	global := func(actions ...KeyAction) bool {
		if binding.Context != KeyContextGlobal {
			return false
		}
		for _, action := range actions {
			if binding.Action == action {
				return true
			}
		}
		return false
	}
	switch {
	case m.question != nil:
		return binding.Context == KeyContextQuestion || global(ActionGlobalRedraw, ActionGlobalInterrupt)
	case m.approval != nil:
		return binding.Context == KeyContextApproval || global(ActionGlobalRedraw, ActionGlobalInterrupt)
	case m.keymapEditor != nil:
		switch m.keymapEditor.mode {
		case keymapChooseAction:
			return binding.Context == KeyContextKeymapAction
		case keymapCapture:
			return binding.Context == KeyContextKeymapCapture
		default:
			return binding.Context == KeyContextKeymap
		}
	case m.checkpointPicker != nil:
		switch {
		case m.checkpointPicker.restored != nil:
			return binding.Context == KeyContextCheckpointRestored
		case m.checkpointPicker.preview != nil:
			return binding.Context == KeyContextCheckpointPreview
		default:
			return binding.Context == KeyContextCheckpointList
		}
	case m.transcriptPager != nil:
		return binding.Context == KeyContextPager
	case m.helpOpen:
		return binding.Context == KeyContextPager || global(ActionGlobalHelp, ActionGlobalRedraw, ActionGlobalInterrupt)
	case m.historySearch != nil:
		return binding.Context == KeyContextHistory || global(ActionGlobalRedraw)
	default:
		switch binding.Context {
		case KeyContextGlobal, KeyContextChat, KeyContextComposer, KeyContextEditor:
			return true
		case KeyContextPager:
			return transcriptBindingActive(binding)
		case KeyContextSuggestion:
			return m.suggest != nil && m.calculateLayout().suggestLines > 0
		default:
			return false
		}
	}
}

func (m *Model) setChord(parts []string) {
	m.chord.parts = append([]string(nil), parts...)
	m.chord.generation++
	m.chord.hint = strings.Join(parts, " ") + " ..."
}

func (m *Model) clearChord() {
	m.chord.parts = nil
	m.chord.hint = ""
	m.chord.generation++
}

func (m *Model) chordTimeoutCmd() tea.Cmd {
	generation := m.chord.generation
	return tea.Tick(chordTimeout, func(time.Time) tea.Msg {
		return chordTimeoutMsg{generation: generation}
	})
}

func (m *Model) handleChordTimeout(msg chordTimeoutMsg) {
	if msg.capture {
		m.handleKeymapCaptureTimeout(msg.generation)
		return
	}
	if msg.generation == m.chord.generation && len(m.chord.parts) > 0 {
		m.clearChord()
		m.lastCtrlC = time.Time{}
		m.ctrlCHint = ""
		m.rewindPrimed = false
	}
}

func (m *Model) keymapCaptureTimeoutCmd(generation int) tea.Cmd {
	return tea.Tick(chordTimeout, func(time.Time) tea.Msg {
		return chordTimeoutMsg{generation: generation, capture: true}
	})
}
