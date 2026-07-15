package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

type modelListModel struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Available        bool              `json:"available"`
	Reasoning        bool              `json:"reasoning"`
	ReasoningOptions []json.RawMessage `json:"reasoning_options"`
}

type modelListProvider struct {
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	Registered    bool             `json:"registered"`
	Available     bool             `json:"available"`
	AuthSource    string           `json:"auth_source"`
	DynamicModels bool             `json:"dynamic_models"`
	DefaultModel  string           `json:"default_model"`
	Models        []modelListModel `json:"models"`
}

type modelListResponse struct {
	DefaultModel string              `json:"default_model"`
	Providers    []modelListProvider `json:"providers"`
}

type modelPickerItem struct {
	ID       string
	Name     string
	Provider string
}

type modelPickerState struct {
	generation int
	loading    bool
	selected   int
	scroll     int
	items      []modelPickerItem
	status     string
}

type modelListMsg struct {
	generation int
	response   modelListResponse
	err        error
}

type modelPreferenceMsg struct {
	loaded   bool
	previous string
	model    string
	err      error
}

func loadSessionModel(call Caller, sessionID string) tea.Cmd {
	return func() tea.Msg {
		var out struct {
			NextModel string `json:"next_model"`
		}
		err := call.Call("session.model.get", map[string]any{"session_id": sessionID}, &out)
		return modelPreferenceMsg{loaded: true, model: out.NextModel, err: err}
	}
}

func (m *Model) persistSessionModel(previous, model string) tea.Cmd {
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return modelPreferenceMsg{previous: previous, model: model, err: fmt.Errorf("daemon not connected")}
		}
		var out struct {
			NextModel string `json:"next_model"`
		}
		err := call.Call("session.model.set", map[string]any{"session_id": sessionID, "model": model}, &out)
		return modelPreferenceMsg{previous: previous, model: model, err: err}
	}
}

func (m *Model) handleModelPreference(msg modelPreferenceMsg) {
	if msg.loaded {
		if msg.err == nil && !m.modelPinned {
			m.model = strings.TrimSpace(msg.model)
			m.layout()
		}
		return
	}
	if msg.err != nil {
		if m.model == msg.model {
			m.model = msg.previous
		}
		m.push(m.text(MsgModelPickerFailed, MessageArgs{"error": msg.err.Error()}))
		m.layout()
	}
}

func (m *Model) openModelPicker() tea.Cmd {
	m.closeSuggest()
	m.modelPickerGen++
	state := &modelPickerState{generation: m.modelPickerGen, loading: true, status: m.text(MsgModelPickerLoading, nil)}
	m.modelPicker = state
	m.layout()
	call, generation := m.call, state.generation
	return func() tea.Msg {
		if call == nil {
			return modelListMsg{generation: generation, err: fmt.Errorf("daemon not connected")}
		}
		var response modelListResponse
		err := call.Call("model.list", map[string]any{}, &response)
		return modelListMsg{generation: generation, response: response, err: err}
	}
}

func (m *Model) handleModelList(msg modelListMsg) {
	state := m.modelPicker
	if state == nil || state.generation != msg.generation {
		return
	}
	state.loading = false
	if msg.err != nil {
		state.status = m.text(MsgModelPickerFailed, MessageArgs{"error": msg.err.Error()})
		return
	}
	defaultID := msg.response.DefaultModel
	if defaultID == "" {
		defaultID = "default"
	}
	state.items = append(state.items, modelPickerItem{ID: defaultID, Name: m.text(MsgModelPickerDefault, nil)})
	for _, provider := range msg.response.Providers {
		if !provider.Registered || !provider.Available {
			continue
		}
		for _, model := range provider.Models {
			if model.Available {
				state.items = append(state.items, modelPickerItem{ID: model.ID, Name: model.Name, Provider: provider.Name})
			}
		}
		if provider.DynamicModels && len(provider.Models) == 0 && strings.TrimSpace(provider.DefaultModel) != "" {
			id := strings.TrimSpace(provider.DefaultModel)
			if !strings.Contains(id, "/") {
				id = provider.ID + "/" + id
			}
			state.items = append(state.items, modelPickerItem{ID: id, Name: provider.Name + " default", Provider: provider.Name})
		}
	}
	current := m.model
	if current == "" {
		current = "default"
	}
	for i := range state.items {
		if state.items[i].ID == current {
			state.selected = i
			break
		}
	}
	state.clamp(m.modelPickerPageHeight())
	state.status = m.text(MsgModelPickerHelp, nil)
}

func (m *Model) modelPickerKey(key string) (tea.Cmd, bool) {
	state := m.modelPicker
	if state == nil {
		return nil, false
	}
	switch key {
	case "esc":
		m.modelPicker = nil
		m.modelPinned = false
		m.layout()
		return m.resumeQueuedAfterTransient(), true
	case "r":
		if !state.loading && len(state.items) == 0 {
			state.generation++
			state.loading = true
			state.status = m.text(MsgModelPickerLoading, nil)
			call, generation := m.call, state.generation
			return func() tea.Msg {
				if call == nil {
					return modelListMsg{generation: generation, err: fmt.Errorf("daemon not connected")}
				}
				var response modelListResponse
				err := call.Call("model.list", map[string]any{}, &response)
				return modelListMsg{generation: generation, response: response, err: err}
			}, true
		}
	case "up", "k":
		if !state.loading && len(state.items) > 0 {
			state.selected = (state.selected - 1 + len(state.items)) % len(state.items)
		}
	case "down", "j":
		if !state.loading && len(state.items) > 0 {
			state.selected = (state.selected + 1) % len(state.items)
		}
	case "pgup":
		state.selected -= m.modelPickerPageHeight()
	case "pgdown":
		state.selected += m.modelPickerPageHeight()
	case "home":
		state.selected = 0
	case "end":
		state.selected = len(state.items) - 1
	case "enter":
		if state.loading || len(state.items) == 0 {
			return nil, true
		}
		selected := state.items[state.selected].ID
		previous := m.model
		if selected == "default" {
			m.model = ""
		} else {
			m.model = selected
		}
		m.modelPicker = nil
		m.push(m.text(MsgUpdateModelChanged, MessageArgs{"model": selected}))
		m.layout()
		return tea.Batch(m.resumeQueuedAfterTransient(), m.persistSessionModel(previous, m.model)), true
	}
	state.clamp(m.modelPickerPageHeight())
	return nil, true
}

func (s *modelPickerState) clamp(page int) {
	if len(s.items) == 0 {
		s.selected, s.scroll = 0, 0
		return
	}
	s.selected = maxInt(0, minInt(s.selected, len(s.items)-1))
	page = maxInt(page, 1)
	if s.selected < s.scroll {
		s.scroll = s.selected
	}
	if s.selected >= s.scroll+page {
		s.scroll = s.selected - page + 1
	}
	s.scroll = maxInt(0, minInt(s.scroll, maxInt(len(s.items)-page, 0)))
}

func (m *Model) modelPickerPageHeight() int { return maxInt(m.height-9, 1) }

func (m *Model) modelPickerView() string {
	state := m.modelPicker
	if state == nil {
		return ""
	}
	width := maxInt(m.width-4, 1)
	lines := []string{m.th.Style(theme.RoleTitle).Render(m.text(MsgModelPickerTitle, nil)), ""}
	if state.loading {
		lines = append(lines, state.status)
	} else {
		page := m.modelPickerPageHeight()
		state.clamp(page)
		end := minInt(state.scroll+page, len(state.items))
		for i := state.scroll; i < end; i++ {
			item := state.items[i]
			prefix := "  "
			if i == state.selected {
				prefix = "> "
			}
			label := item.ID
			if width >= 28 && strings.TrimSpace(item.Name) != "" {
				label += "  " + item.Name
			}
			line := fitRenderedLine(prefix+label, width)
			if i == state.selected {
				line = m.th.Style(theme.RoleTitle).Render(line)
			}
			lines = append(lines, line)
		}
		if len(state.items) > page {
			lines = append(lines, "  "+m.text(MsgModelPickerPage, MessageArgs{"start": state.scroll + 1, "end": end, "count": len(state.items)}))
		}
		if len(state.items) == 1 {
			lines = append(lines, fitRenderedLine(m.text(MsgModelPickerEmpty, nil), width))
		}
		lines = append(lines, "", fitRenderedLine(state.status, width))
	}
	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1)
	if color := m.th.Color(theme.RoleTitle); color != nil {
		style = style.BorderForeground(color)
	}
	return style.Render(strings.Join(lines, "\n"))
}
