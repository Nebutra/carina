package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

const sessionNavigatorID ui.ComponentID = "session-navigator"

type navigatorRowRef struct {
	Index int
	ID    string
}

type sessionNavigatorComponent struct {
	ui.Base
	model        *Model
	viewport     ui.Rect
	controls     map[ui.ComponentID]*navigatorControl
	controlOrder []ui.Component
}

func newSessionNavigatorComponent(model *Model) *sessionNavigatorComponent {
	return &sessionNavigatorComponent{
		Base:     ui.Base{ComponentID: sessionNavigatorID},
		model:    model,
		controls: make(map[ui.ComponentID]*navigatorControl),
	}
}

type navigatorControl struct {
	ui.Base
	parent *sessionNavigatorComponent
	hit    ui.HitRegion
}

func (c *navigatorControl) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: c.hit.Bounds.Width, Height: c.hit.Bounds.Height})
}
func (c *navigatorControl) Layout(ui.Rect) {}
func (c *navigatorControl) Render(ui.RenderContext) ui.Node {
	hit := c.hit
	hit.Owner = c.ComponentID
	return ui.Node{ID: c.ComponentID, Bounds: hit.Bounds, Focusable: true, Focused: c.Focused(), Hit: []ui.HitRegion{hit}}
}
func (c *navigatorControl) Handle(event ui.Event) ui.Result {
	state := c.parent.model.sessionPicker
	if state == nil {
		return ui.Result{}
	}
	if event.Kind == ui.EventKey && (event.Key == "enter" || event.Key == " ") {
		return c.parent.handlePointer(state, ui.PointerEvent{Kind: ui.PointerClick, Hit: &c.hit})
	}
	if event.Kind == ui.EventPointer {
		return c.parent.handlePointer(state, event.Pointer)
	}
	return ui.Result{}
}

func (c *sessionNavigatorComponent) Components() []ui.Component { return c.controlOrder }

func (c *sessionNavigatorComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (c *sessionNavigatorComponent) Layout(bounds ui.Rect) {
	c.viewport = bounds
}

func (c *sessionNavigatorComponent) Render(ctx ui.RenderContext) ui.Node {
	m, state := c.model, c.model.sessionPicker
	if state == nil || c.viewport.Empty() {
		return ui.Node{ID: sessionNavigatorID}
	}

	modalWidth := minInt(maxInt(c.viewport.Width, 1), 120)
	contentWidth := maxInt(modalWidth-4, 1)
	title := m.text(MsgSessionPickerTitle, nil)
	if state.scope == sessionScopeAll {
		title = m.text(MsgSessionWorkspaceTitle, nil)
	}
	lines := []string{m.th.Style(theme.RoleTitle).Render(fitRenderedLine(title, contentWidth))}

	currentLabel := m.text(MsgSessionNavigatorCurrent, nil)
	allLabel := m.text(MsgSessionNavigatorAll, nil)
	currentRaw, allRaw := "  "+currentLabel+"  ", "  "+allLabel+"  "
	currentRendered, allRendered := currentRaw, allRaw
	if state.scope == sessionScopeCurrent {
		currentRendered = m.th.Style(theme.RoleTitle).Render("[" + currentLabel + "]")
	} else {
		allRendered = m.th.Style(theme.RoleTitle).Render("[" + allLabel + "]")
	}
	tabs := currentRendered + "  " + allRendered
	lines = append(lines, fitRenderedLine(tabs, contentWidth))

	searchLabel := m.text(MsgSessionNavigatorSearch, nil)
	searchValue := state.query
	if searchValue == "" {
		searchValue = "/"
	}
	searchLine := searchLabel + ": " + searchValue
	if state.searching {
		searchLine = m.th.Style(theme.RoleTitle).Render(searchLine)
	} else {
		searchLine = m.th.Style(theme.RoleMuted).Render(searchLine)
	}
	lines = append(lines, fitRenderedLine(searchLine, contentWidth), "")

	rowLine := len(lines)
	page := maxInt(c.viewport.Height-14, 1)
	state.clamp(page)
	end := minInt(state.scroll+page, state.itemCount())
	rowRefs := make([]navigatorRowRef, 0, maxInt(end-state.scroll, 0))
	if state.loading {
		lines = append(lines, fitRenderedLine(state.status, contentWidth))
	} else if state.itemCount() == 0 {
		empty := state.status
		if strings.TrimSpace(state.query) != "" {
			empty = m.text(MsgSessionNavigatorNoMatches, nil)
		}
		lines = append(lines, fitRenderedLine(empty, contentWidth))
	} else if state.stage == sessionStageWorkspaces {
		indices := state.workspaceIndices()
		for visible := state.scroll; visible < end; visible++ {
			item := state.workspaces[indices[visible]]
			id := item.Root
			label := workspaceDisplayName(item)
			context := compactWorkspaceContext(item.Root, label)
			if context != "" {
				label += "  " + m.th.Style(theme.RoleMuted).Render(context)
			}
			if item.Current {
				label += "  " + m.th.Style(theme.RoleSuccess).Render(m.text(MsgSessionWorkspaceCurrent, nil))
			}
			if item.Error != "" {
				label += "  " + m.th.Style(theme.RoleError).Render(m.text(MsgSessionWorkspaceInvalid, nil))
			}
			lines = append(lines, c.renderRow(state, visible, id, label, contentWidth, item.Error != ""))
			rowRefs = append(rowRefs, navigatorRowRef{Index: visible, ID: id})
		}
	} else {
		indices := state.sessionIndices()
		for visible := state.scroll; visible < end; visible++ {
			item := state.items[indices[visible]]
			id := item.SessionID
			label := c.sessionRowLabel(item, contentWidth)
			lines = append(lines, c.renderRow(state, visible, id, label, contentWidth, false))
			rowRefs = append(rowRefs, navigatorRowRef{Index: visible, ID: id})
		}
		if len(indices) > 0 {
			lines = append(lines, "")
			lines = append(lines, m.sessionContinuityDetail(state.items[indices[state.selected]], contentWidth)...)
		}
	}
	lines = append(lines, "", fitRenderedLine(m.th.Style(theme.RoleMuted).Render(state.status), contentWidth))

	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1).Width(modalWidth)
	if color := m.th.Color(theme.RoleTitle); color != nil {
		style = style.BorderForeground(color)
	}
	content := style.Render(strings.Join(lines, "\n"))
	boxWidth, boxHeight := lipgloss.Width(content), lipgloss.Height(content)
	boxWidth = minInt(boxWidth, c.viewport.Width)
	boxHeight = minInt(boxHeight, c.viewport.Height)
	box := ui.Rect{
		X:     c.viewport.X + maxInt((c.viewport.Width-boxWidth)/2, 0),
		Y:     c.viewport.Y + maxInt((c.viewport.Height-boxHeight)/2, 0),
		Width: boxWidth, Height: boxHeight,
	}
	innerX, innerY := box.X+2, box.Y+1
	hits := []ui.HitRegion{{
		ID: "navigator-surface", Owner: sessionNavigatorID, Bounds: box,
		Kind: ui.HitHover, Action: "navigator-surface",
	}}
	currentWidth := lipgloss.Width(currentRendered)
	hits = append(hits,
		ui.HitRegion{ID: "navigator-scope-current", Owner: sessionNavigatorID,
			Bounds: ui.Rect{X: innerX, Y: innerY + 1, Width: currentWidth, Height: 1},
			Z:      2, Kind: ui.HitActivate, Action: "navigator-scope", Data: sessionScopeCurrent, Focusable: true},
		ui.HitRegion{ID: "navigator-scope-all", Owner: sessionNavigatorID,
			Bounds: ui.Rect{X: innerX + currentWidth + 2, Y: innerY + 1, Width: lipgloss.Width(allRendered), Height: 1},
			Z:      2, Kind: ui.HitActivate, Action: "navigator-scope", Data: sessionScopeAll, Focusable: true},
	)
	for offset, ref := range rowRefs {
		disabled := false
		if state.stage == sessionStageWorkspaces {
			indices := state.workspaceIndices()
			disabled = state.workspaces[indices[ref.Index]].Error != ""
		}
		hits = append(hits, ui.HitRegion{
			ID: ui.HitID("navigator-row:" + ref.ID), Owner: sessionNavigatorID,
			Bounds: ui.Rect{X: innerX, Y: innerY + rowLine + offset, Width: contentWidth, Height: 1},
			Z:      2, Kind: ui.HitActivate, Action: "navigator-row", Data: ref,
			Disabled: disabled, Focusable: true,
		})
	}

	var cursor *ui.CursorRequest
	if state.searching {
		cursorX := innerX + lipgloss.Width(searchLabel+": "+state.query)
		cursor = &ui.CursorRequest{Owner: sessionNavigatorID, X: minInt(cursorX, box.X+box.Width-2), Y: innerY + 2, Visible: true}
	}
	controlHits := append([]ui.HitRegion(nil), hits[1:]...)
	c.syncControls(controlHits)
	children := make([]ui.Node, 0, len(c.controlOrder))
	for _, control := range c.controlOrder {
		children = append(children, control.Render(ui.RenderContext{}))
	}
	return ui.Node{
		ID: sessionNavigatorID, Bounds: box, Z: 10, Content: content,
		Focusable: true, Focused: c.Focused(), Hit: hits[:1], Cursor: cursor, Children: children,
	}
}

func (c *sessionNavigatorComponent) syncControls(hits []ui.HitRegion) {
	c.controlOrder = c.controlOrder[:0]
	for _, hit := range hits {
		id := ui.ComponentID("navigator-control:" + string(hit.ID))
		control := c.controls[id]
		if control == nil {
			control = &navigatorControl{Base: ui.Base{ComponentID: id}, parent: c}
			c.controls[id] = control
		}
		control.hit = hit
		c.controlOrder = append(c.controlOrder, control)
	}
}

func (c *sessionNavigatorComponent) renderRow(state *sessionPickerState, index int, id, label string, width int, disabled bool) string {
	prefix := "  "
	if index == state.selected {
		prefix = "> "
	} else if id != "" && id == state.hoveredID {
		prefix = "· "
	}
	line := fitRenderedLine(prefix+label, width)
	switch {
	case disabled:
		return c.model.th.Style(theme.RoleMuted).Render(line)
	case index == state.selected:
		return c.model.th.Style(theme.RoleTitle).Render(line)
	case id != "" && id == state.hoveredID:
		return c.model.th.Style(theme.RoleInfo).Render(line)
	default:
		return line
	}
}

func (c *sessionNavigatorComponent) sessionRowLabel(item sessionListItem, width int) string {
	m := c.model
	name := item.Name
	if name == "" {
		name = shortID(item.SessionID)
	}
	parts := []string{name, m.th.Style(theme.RoleMuted).Render(m.sessionStatusLabel(item.Status))}
	workspace := filepathBase(item.WorkspaceRoot)
	if workspace != "" {
		parts = append(parts, m.th.Style(theme.RoleMuted).Render(workspace))
	}
	if age := m.sessionAge(item.CreatedAt); age != "" {
		parts = append(parts, m.th.Style(theme.RoleMuted).Render(age))
	}
	if item.TaskStatus != "" {
		parts = append(parts, m.taskStatusText(normalizeTaskStatus(item.TaskStatus)))
	}
	if width >= 40 && item.ParentID != "" {
		lineage := m.text(MsgSessionPickerForkOf, MessageArgs{"parent": item.ParentID})
		if item.ForkedFromTaskID != "" {
			lineage += " " + m.text(MsgSessionPickerForkTask, MessageArgs{"task": item.ForkedFromTaskID})
		}
		parts = append(parts, m.th.Style(theme.RoleMuted).Render(lineage))
	}
	return strings.Join(parts, "  ")
}

func filepathBase(root string) string {
	base := workspaceDisplayName(WorkspaceListItem{Root: root})
	if base == "." || base == "/" || base == root && strings.TrimSpace(root) == "" {
		return ""
	}
	return base
}

func compactWorkspaceContext(root, name string) string {
	clean := strings.TrimSpace(root)
	if clean == "" || clean == name {
		return ""
	}
	parts := strings.FieldsFunc(clean, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) <= 2 {
		return clean
	}
	return "…/" + strings.Join(parts[len(parts)-2:], "/")
}

func (c *sessionNavigatorComponent) Handle(event ui.Event) ui.Result {
	state := c.model.sessionPicker
	if state == nil {
		return ui.Result{}
	}
	if event.Kind == ui.EventPointer {
		return c.handlePointer(state, event.Pointer)
	}
	if event.Kind == ui.EventPaste {
		if state.searching {
			state.query += event.Text
			state.resetFilterSelection()
		}
		return ui.Result{Handled: true}
	}
	if event.Kind != ui.EventKey {
		return ui.Result{}
	}
	key := event.Key
	if state.searching {
		switch key {
		case "esc":
			state.searching = false
			return ui.Result{Handled: true}
		case "backspace":
			state.query = trimLastRune(state.query)
			state.resetFilterSelection()
			return ui.Result{Handled: true}
		case "ctrl+u":
			state.query = ""
			state.resetFilterSelection()
			return ui.Result{Handled: true}
		}
		if isNavigatorTextKey(key) {
			state.query += key
			state.resetFilterSelection()
			return ui.Result{Handled: true}
		}
	}

	switch key {
	case "/", "ctrl+f":
		state.searching = true
		return ui.Result{Handled: true}
	case "up", "k":
		state.selected--
		state.clamp(maxInt(c.viewport.Height-14, 1))
		return ui.Result{Handled: true}
	case "down", "j":
		state.selected++
		state.clamp(maxInt(c.viewport.Height-14, 1))
		return ui.Result{Handled: true}
	case "enter":
		return navigatorAction("activate")
	case "esc":
		return navigatorAction("back")
	case "tab":
		return navigatorAction("toggle-scope")
	case "r":
		return navigatorAction("retry")
	case "b":
		return navigatorAction("rollback")
	default:
		return ui.Result{Handled: true}
	}
}

func (c *sessionNavigatorComponent) handlePointer(state *sessionPickerState, event ui.PointerEvent) ui.Result {
	if event.Kind == ui.PointerLeave {
		state.hoveredID = ""
		return ui.Result{Handled: true}
	}
	if event.Kind == ui.PointerWheel {
		state.selected += event.WheelDelta
		state.clamp(maxInt(c.viewport.Height-14, 1))
		return ui.Result{Handled: true}
	}
	if event.Hit == nil {
		return ui.Result{}
	}
	if event.Hit.Action == "navigator-surface" {
		if event.Kind == ui.PointerMove {
			state.hoveredID = ""
		}
		return ui.Result{Handled: true}
	}
	if event.Hit.Action == "navigator-scope" && event.Kind == ui.PointerClick {
		scope, ok := event.Hit.Data.(sessionPickerScope)
		if !ok || scope == state.scope {
			return ui.Result{Handled: true}
		}
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: sessionNavigatorID, Name: "scope", Data: scope}}}
	}
	if event.Hit.Action != "navigator-row" {
		return ui.Result{Handled: true}
	}
	ref, ok := event.Hit.Data.(navigatorRowRef)
	if !ok {
		return ui.Result{Handled: true}
	}
	if event.Kind == ui.PointerMove {
		state.hoveredID = ref.ID
		return ui.Result{Handled: true}
	}
	if event.Kind == ui.PointerClick {
		activate := state.selected == ref.Index
		state.selected = ref.Index
		state.clamp(maxInt(c.viewport.Height-14, 1))
		if activate {
			return navigatorAction("activate")
		}
		return ui.Result{Handled: true}
	}
	return ui.Result{Handled: true}
}

func navigatorAction(name string) ui.Result {
	return ui.Result{Handled: true, Actions: []ui.Action{{Source: sessionNavigatorID, Name: name}}}
}

func trimLastRune(value string) string {
	_, size := utf8.DecodeLastRuneInString(value)
	if size == 0 {
		return ""
	}
	return value[:len(value)-size]
}

func isNavigatorTextKey(key string) bool {
	if key == "" || strings.Contains(key, "+") {
		return false
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (m *Model) ensureNavigatorFrame() ui.Frame {
	if m.componentRuntime == nil {
		m.componentRuntime = ui.NewRuntime()
	}
	component := newSessionNavigatorComponent(m)
	frame := m.componentRuntime.BeginFrame(component, ui.Rect{Width: maxInt(m.width, 1), Height: maxInt(m.height, 1)})
	m.componentFrame = frame
	m.reconcileFrameGraphics(frame)
	return frame
}

func (m *Model) dispatchNavigatorKey(key string) (tea.Cmd, bool) {
	if m.sessionPicker == nil {
		return nil, false
	}
	m.ensureNavigatorFrame()
	result := m.componentRuntime.Dispatch(ui.Event{Kind: ui.EventKey, Key: key})
	return m.applyNavigatorResult(result), result.Handled
}

func (m *Model) applyNavigatorResult(result ui.Result) tea.Cmd {
	var commands []tea.Cmd
	for _, action := range result.Actions {
		switch action.Name {
		case "activate":
			cmd, _ := m.sessionPickerKey("enter")
			commands = append(commands, cmd)
		case "back":
			cmd, _ := m.sessionPickerKey("esc")
			commands = append(commands, cmd)
		case "toggle-scope":
			cmd, _ := m.sessionPickerKey("tab")
			commands = append(commands, cmd)
		case "retry":
			cmd, _ := m.sessionPickerKey("r")
			commands = append(commands, cmd)
		case "rollback":
			cmd, _ := m.sessionPickerKey("b")
			commands = append(commands, cmd)
		case "scope":
			scope, ok := action.Data.(sessionPickerScope)
			if !ok || m.sessionPicker == nil || scope == m.sessionPicker.scope {
				continue
			}
			if scope == sessionScopeAll {
				commands = append(commands, m.openWorkspacePicker())
			} else {
				commands = append(commands, m.openSessionPicker())
			}
		}
	}
	return tea.Batch(commands...)
}
