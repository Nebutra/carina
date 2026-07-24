package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

// KeymapUpdateFunc persists one concrete override (or removes it) and returns
// the fully resolved override set. The model swaps only a validated snapshot.
type KeymapUpdateFunc func(action string, keys []string, remove bool) ([]KeyBindingOverride, error)

type keymapEditorMode int

const (
	keymapBrowse keymapEditorMode = iota
	keymapChooseAction
	keymapCapture
)

type keymapEditorState struct {
	bindings   []KeyBindingDescriptor
	selected   int
	scroll     int
	mode       keymapEditorMode
	add        bool
	capture    []string
	quoted     bool
	captureGen int
	pending    bool
	generation int
	status     string
}

type keymapUpdatedMsg struct {
	generation int
	overrides  []KeyBindingOverride
	err        error
}

func (m *Model) openKeymapEditor() {
	m.closeSuggest()
	m.keymapEditor = &keymapEditorState{bindings: m.keys.BindingDescriptors()}
	m.cancelBacktrack()
	m.layout()
}

func (m *Model) closeKeymapEditor() {
	if m.keymapEditor != nil {
		m.keymapEditor.captureGen++
	}
	m.keymapEditor = nil
	m.layout()
}

func (m *Model) keymapEditorKey(key string) (tea.Cmd, bool) {
	state := m.keymapEditor
	if state == nil {
		return nil, false
	}
	if state.pending {
		return nil, true
	}
	switch state.mode {
	case keymapCapture:
		return m.keymapCaptureKey(key), true

	case keymapChooseAction:
		switch {
		case m.keys.matches(KeyContextKeymapAction, ActionKeymapActionBack, key):
			state.mode = keymapBrowse
			state.status = ""
		case m.keys.matches(KeyContextKeymapAction, ActionKeymapActionReplace, key):
			m.beginKeymapCapture(false)
		case m.keys.matches(KeyContextKeymapAction, ActionKeymapActionAdd, key):
			m.beginKeymapCapture(true)
		case m.keys.matches(KeyContextKeymapAction, ActionKeymapActionRestore, key):
			if m.keymapUpdater == nil {
				state.status = m.text(MsgKeymapUnavailable, nil)
				return nil, true
			}
			return m.persistKeymap(state.current().Action, nil, true), true
		}
		return nil, true
	}

	switch {
	case m.keys.matches(KeyContextKeymap, ActionKeymapClose, key):
		m.closeKeymapEditor()
	case m.keys.matches(KeyContextKeymap, ActionKeymapUp, key):
		state.selected--
	case m.keys.matches(KeyContextKeymap, ActionKeymapDown, key):
		state.selected++
	case m.keys.matches(KeyContextKeymap, ActionKeymapPageUp, key):
		state.selected -= m.keymapEditorPageHeight()
	case m.keys.matches(KeyContextKeymap, ActionKeymapPageDown, key):
		state.selected += m.keymapEditorPageHeight()
	case m.keys.matches(KeyContextKeymap, ActionKeymapTop, key):
		state.selected = 0
	case m.keys.matches(KeyContextKeymap, ActionKeymapBottom, key):
		state.selected = len(state.bindings) - 1
	case m.keys.matches(KeyContextKeymap, ActionKeymapEdit, key):
		state.mode = keymapChooseAction
		state.status = m.text(MsgKeymapChoose, nil)
	default:
		return nil, true
	}
	state.clamp(m.keymapEditorPageHeight())
	return nil, true
}

func (m *Model) beginKeymapCapture(add bool) {
	state := m.keymapEditor
	if state == nil {
		return
	}
	state.add = add
	state.mode = keymapCapture
	state.capture = nil
	state.quoted = false
	state.captureGen++
	state.status = m.text(MsgKeymapCaptureStart, MessageArgs{
		"cancel":  m.keys.label(KeyContextKeymapCapture, ActionKeymapCaptureCancel),
		"literal": keymapCaptureLiteralNext,
	})
}

const keymapCaptureLiteralNext = "ctrl+v"

func (m *Model) keymapCaptureKey(key string) tea.Cmd {
	state := m.keymapEditor
	if state == nil || state.mode != keymapCapture {
		return nil
	}
	quoted := state.quoted
	state.quoted = false
	if !quoted && terminalKeyIdentity(key) == keymapCaptureLiteralNext {
		state.quoted = true
		state.captureGen++
		state.status = m.text(MsgKeymapCaptureLiteral, MessageArgs{"literal": keymapCaptureLiteralNext})
		return m.keymapCaptureTimeoutCmd(state.captureGen)
	}
	if !quoted && m.keys.matches(KeyContextKeymapCapture, ActionKeymapCaptureCancel, key) {
		state.capture = nil
		state.quoted = false
		state.captureGen++
		state.mode = keymapChooseAction
		state.status = m.text(MsgKeymapCaptureCancelled, nil)
		return nil
	}
	if !quoted && len(state.capture) > 0 && m.keys.matches(KeyContextKeymapCapture, ActionKeymapCaptureCommit, key) {
		return m.applyKeymapCapture(strings.Join(state.capture, " "))
	}
	captured, err := normalizeSingleKeySpec(key)
	if err != nil {
		state.status = err.Error()
		return nil
	}
	if len(state.capture) == 0 && !reliableChordPrefix(terminalKeyIdentity(captured)) {
		return m.applyKeymapCapture(captured)
	}
	state.capture = append(state.capture, captured)
	if len(state.capture) >= 3 {
		return m.applyKeymapCapture(strings.Join(state.capture, " "))
	}
	state.captureGen++
	state.status = m.text(MsgKeymapCapturePending, MessageArgs{
		"chord":   strings.Join(state.capture, " "),
		"save":    m.keys.label(KeyContextKeymapCapture, ActionKeymapCaptureCommit),
		"cancel":  m.keys.label(KeyContextKeymapCapture, ActionKeymapCaptureCancel),
		"literal": keymapCaptureLiteralNext,
	})
	return m.keymapCaptureTimeoutCmd(state.captureGen)
}

func (m *Model) applyKeymapCapture(captured string) tea.Cmd {
	state := m.keymapEditor
	if state == nil {
		return nil
	}
	binding := state.current()
	keys := []string{captured}
	if state.add {
		keys = append(append([]string(nil), binding.Keys...), captured)
		keys = uniqueKeySpecs(keys)
	}
	candidate, err := m.keys.withOverride(KeyBindingOverride{
		Context: binding.Context, Action: binding.Action, Keys: keys,
	})
	if err != nil {
		state.capture = nil
		state.quoted = false
		state.captureGen++
		state.status = m.text(MsgKeymapCaptureRetry, MessageArgs{"error": err.Error()})
		return nil
	}
	state.capture = nil
	state.quoted = false
	state.captureGen++
	if m.keymapUpdater == nil {
		m.installRuntimeKeymap(candidate)
		state.bindings = m.keys.BindingDescriptors()
		state.mode = keymapBrowse
		state.status = m.text(MsgKeymapAppliedProcess, nil)
		return nil
	}
	return m.persistKeymap(binding.Action, keys, false)
}

func uniqueKeySpecs(keys []string) []string {
	seen := make(map[string]bool, len(keys))
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		identity := terminalKeyIdentity(key)
		if seen[identity] {
			continue
		}
		seen[identity] = true
		result = append(result, key)
	}
	return result
}

func (m *Model) handleKeymapCaptureTimeout(generation int) {
	state := m.keymapEditor
	if state == nil || state.mode != keymapCapture || generation != state.captureGen || (len(state.capture) == 0 && !state.quoted) {
		return
	}
	state.capture = nil
	state.quoted = false
	state.captureGen++
	state.mode = keymapChooseAction
	state.status = m.text(MsgKeymapCaptureTimeout, nil)
}

func (m *Model) persistKeymap(action KeyAction, keys []string, remove bool) tea.Cmd {
	state := m.keymapEditor
	if state == nil || m.keymapUpdater == nil {
		return nil
	}
	state.generation++
	generation := state.generation
	state.pending = true
	state.status = m.text(MsgKeymapSaving, nil)
	updater := m.keymapUpdater
	return func() tea.Msg {
		overrides, err := updater(string(action), append([]string(nil), keys...), remove)
		return keymapUpdatedMsg{generation: generation, overrides: overrides, err: err}
	}
}

func (m *Model) handleKeymapUpdated(msg keymapUpdatedMsg) {
	state := m.keymapEditor
	if state == nil || msg.generation != state.generation {
		return
	}
	state.pending = false
	if msg.err != nil {
		state.status = m.text(MsgKeymapNotChanged, MessageArgs{"error": msg.err.Error()})
		return
	}
	next, err := newRuntimeKeymap(msg.overrides)
	if err != nil {
		state.status = m.text(MsgKeymapSavedRejected, MessageArgs{"error": err.Error()})
		return
	}
	action := state.current().Action
	m.installRuntimeKeymap(next)
	state.bindings = m.keys.BindingDescriptors()
	state.selected = descriptorIndex(state.bindings, action)
	state.mode = keymapBrowse
	state.status = m.text(MsgKeymapSaved, nil)
	state.clamp(m.keymapEditorPageHeight())
}

func (m *Model) handleKeymapReload(msg KeymapReloadMsg) {
	if msg.WorkspaceRoot != "" && cleanWorkspaceRoot(msg.WorkspaceRoot) != cleanWorkspaceRoot(m.workspaceRoot) {
		return
	}
	if msg.Err != nil {
		m.push(m.text(MsgKeymapReloadRejected, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.Err.Error()}))
		return
	}
	next, err := newRuntimeKeymap(msg.Overrides)
	if err != nil {
		m.push(m.text(MsgKeymapReloadRejected, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}))
		return
	}
	if keymapDescriptorsEqual(m.keys.BindingDescriptors(), next.BindingDescriptors()) {
		return
	}
	m.installRuntimeKeymap(next)
	if state := m.keymapEditor; state != nil {
		action := state.current().Action
		state.bindings = m.keys.BindingDescriptors()
		state.selected = descriptorIndex(state.bindings, action)
		state.status = strings.TrimPrefix(m.text(MsgKeymapReloaded, nil), "- ")
		state.clamp(m.keymapEditorPageHeight())
	}
	m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgKeymapReloaded, nil)))
}

func keymapDescriptorsEqual(a, b []KeyBindingDescriptor) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Context != b[i].Context || a[i].Action != b[i].Action ||
			a[i].Description != b[i].Description || len(a[i].Keys) != len(b[i].Keys) {
			return false
		}
		for j := range a[i].Keys {
			if a[i].Keys[j] != b[i].Keys[j] {
				return false
			}
		}
	}
	return true
}

func (m *Model) installRuntimeKeymap(keys runtimeKeymap) {
	m.keys = keys
	installEditorKeymap(&m.input, keys)
	m.input.Placeholder = m.text(MsgPlaceholderInstruction, MessageArgs{
		"submit":  primaryKeyLabel(keys.keys(KeyContextComposer, ActionComposerSubmit)),
		"newline": primaryKeyLabel(keys.keys(KeyContextComposer, ActionComposerNewline)),
		"help":    primaryKeyLabel(keys.keys(KeyContextGlobal, ActionGlobalHelp)),
	})
	m.layout()
}

func (s *keymapEditorState) current() KeyBindingDescriptor {
	if len(s.bindings) == 0 {
		return KeyBindingDescriptor{}
	}
	s.selected = clampInt(s.selected, 0, len(s.bindings)-1)
	return s.bindings[s.selected]
}

func (s *keymapEditorState) clamp(page int) {
	if len(s.bindings) == 0 {
		s.selected, s.scroll = 0, 0
		return
	}
	s.selected = clampInt(s.selected, 0, len(s.bindings)-1)
	if s.selected < s.scroll {
		s.scroll = s.selected
	}
	if s.selected >= s.scroll+page {
		s.scroll = s.selected - page + 1
	}
	s.scroll = clampInt(s.scroll, 0, maxInt(len(s.bindings)-page, 0))
}

func descriptorIndex(bindings []KeyBindingDescriptor, action KeyAction) int {
	for i, binding := range bindings {
		if binding.Action == action {
			return i
		}
	}
	return 0
}

func (m *Model) keymapEditorPageHeight() int { return maxInt(m.height-8, 1) }

func (m *Model) keymapEditorView() string {
	state := m.keymapEditor
	if state == nil {
		return ""
	}
	width := maxInt(m.width-8, 20)
	lines := []string{m.th.Style(theme.RoleWarning).Render(m.text(MsgKeymapTitle, nil))}
	binding := state.current()
	switch state.mode {
	case keymapChooseAction:
		lines = append(lines, "", fitRenderedLine(fmt.Sprintf("%s  %s", binding.Action, strings.Join(binding.Keys, ", ")), width),
			fitRenderedLine(m.localizedKeyDescription(binding), width), "",
			m.text(MsgKeymapActionFooter, MessageArgs{
				"replace": m.keys.label(KeyContextKeymapAction, ActionKeymapActionReplace),
				"add":     m.keys.label(KeyContextKeymapAction, ActionKeymapActionAdd),
				"restore": m.keys.label(KeyContextKeymapAction, ActionKeymapActionRestore),
				"back":    m.keys.label(KeyContextKeymapAction, ActionKeymapActionBack),
			}))
	case keymapCapture:
		prompt := m.text(MsgKeymapPressKey, nil)
		if state.quoted {
			prompt = m.text(MsgKeymapCaptureLiteral, MessageArgs{"literal": keymapCaptureLiteralNext})
		} else if len(state.capture) > 0 {
			prompt = m.text(MsgKeymapPendingChord, MessageArgs{"chord": strings.Join(state.capture, " ")})
		}
		lines = append(lines, "", fitRenderedLine(string(binding.Action), width), "", fitRenderedLine(prompt, width),
			fitRenderedLine(m.text(MsgKeymapCaptureFooter, MessageArgs{
				"save":    m.keys.label(KeyContextKeymapCapture, ActionKeymapCaptureCommit),
				"cancel":  m.keys.label(KeyContextKeymapCapture, ActionKeymapCaptureCancel),
				"literal": keymapCaptureLiteralNext,
			}), width))
	default:
		page := m.keymapEditorPageHeight()
		state.clamp(page)
		end := minInt(state.scroll+page, len(state.bindings))
		lastContext := KeyContext("")
		for i := state.scroll; i < end; i++ {
			item := state.bindings[i]
			prefix := "  "
			if i == state.selected {
				prefix = "> "
			}
			context := ""
			if item.Context != lastContext {
				context = string(item.Context) + " · "
				lastContext = item.Context
			}
			line := fmt.Sprintf("%s%s%s  %s", prefix, context, strings.TrimPrefix(string(item.Action), string(item.Context)+"."), strings.Join(item.Keys, ", "))
			if i == state.selected {
				line = m.th.Style(theme.RoleTitle).Render(line)
			}
			lines = append(lines, fitRenderedLine(line, width))
		}
		lines = append(lines, "", m.text(MsgKeymapBrowseFooter, MessageArgs{
			"edit":  m.keys.label(KeyContextKeymap, ActionKeymapEdit),
			"up":    m.keys.label(KeyContextKeymap, ActionKeymapUp),
			"down":  m.keys.label(KeyContextKeymap, ActionKeymapDown),
			"close": m.keys.label(KeyContextKeymap, ActionKeymapClose),
		}))
	}
	if state.status != "" {
		lines = append(lines, "", fitRenderedLine(m.th.Style(theme.RoleMuted).Render(state.status), width))
	}
	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1)
	if color := m.th.Color(theme.RoleWarning); color != nil {
		style = style.BorderForeground(color)
	}
	return style.Render(strings.Join(lines, "\n"))
}
