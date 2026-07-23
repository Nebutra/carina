package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

const governanceOverlayID ui.ComponentID = "governance-overlay"

type governanceComponent struct {
	ui.Base
	model *Model
}

func newGovernanceComponent(model *Model) *governanceComponent {
	return &governanceComponent{Base: ui.Base{ComponentID: governanceOverlayID}, model: model}
}

func (c *governanceComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (c *governanceComponent) Render(ui.RenderContext) ui.Node {
	if c.Bounds.Empty() || (c.model.question == nil && c.model.approval == nil) {
		return ui.Node{ID: governanceOverlayID}
	}
	content := c.model.overlayView()
	if c.model.question != nil {
		content = c.model.questionOverlayView()
	}
	content = fitViewBlock(content, c.Bounds.Width, c.Bounds.Height, true)
	boxWidth := minInt(lipgloss.Width(content), c.Bounds.Width)
	boxHeight := minInt(lipgloss.Height(content), c.Bounds.Height)
	box := ui.Rect{
		X:     c.Bounds.X + maxInt((c.Bounds.Width-boxWidth)/2, 0),
		Y:     c.Bounds.Y + maxInt((c.Bounds.Height-boxHeight)/2, 0),
		Width: boxWidth, Height: boxHeight,
	}
	hits := []ui.HitRegion{{
		ID: "governance-surface", Owner: governanceOverlayID, Bounds: box,
		Kind: ui.HitHover, Action: "surface",
	}}
	if c.model.question != nil {
		hits = append(hits, c.questionHits(box)...)
	} else {
		hits = append(hits, c.approvalHits(box)...)
	}
	return ui.Node{
		ID: governanceOverlayID, Bounds: box, Z: 20, Content: content,
		Focusable: true, Focused: c.Focused(), Hit: hits,
	}
}

func (c *governanceComponent) questionHits(box ui.Rect) []ui.HitRegion {
	q := c.model.question
	if q == nil || q.Resolving || box.Height < 3 {
		return nil
	}
	innerX, innerY := box.X+2, box.Y+1
	contentWidth := maxInt(box.Width-4, 1)
	if len(q.Options) == 0 {
		return []ui.HitRegion{{
			ID: "question-answer", Owner: governanceOverlayID,
			Bounds: ui.Rect{X: innerX, Y: box.Y + box.Height - 2, Width: contentWidth, Height: 1},
			Z:      2, Kind: ui.HitActivate, Action: "question-answer", Focusable: true,
		}}
	}
	line := len(wrappedOverlayLines(q.Prompt, c.model.questionContentWidth())) + 1
	viewportEnd := q.Scroll + c.model.questionViewportHeight()
	var hits []ui.HitRegion
	for index, option := range q.Options {
		number := "    "
		if index < 9 {
			number = fmt.Sprintf("[%d] ", index+1)
		}
		value := "  " + number + option.Label
		if option.Description != "" {
			value += " - " + option.Description
		}
		height := len(wrappedOverlayLines(value, c.model.questionContentWidth()))
		start, end := line, line+height
		line = end
		if end <= q.Scroll || start >= viewportEnd {
			continue
		}
		visibleStart := maxInt(start, q.Scroll)
		visibleEnd := minInt(end, viewportEnd)
		hits = append(hits, ui.HitRegion{
			ID: ui.HitID(fmt.Sprintf("question-option:%d", index)), Owner: governanceOverlayID,
			Bounds: ui.Rect{X: innerX, Y: innerY + 2 + visibleStart - q.Scroll, Width: contentWidth, Height: visibleEnd - visibleStart},
			Z:      2, Kind: ui.HitActivate, Action: "question-option", Data: index, Focusable: true,
		})
	}
	return hits
}

func (c *governanceComponent) approvalHits(box ui.Rect) []ui.HitRegion {
	ap := c.model.approval
	if ap == nil || ap.Resolving || box.Height < 3 {
		return nil
	}
	actions := []string{"approval-once", "approval-session", "approval-project", "approval-deny"}
	if c.model.approvalContentWidth() < 34 {
		actions = []string{"approval-once", "approval-deny"}
	}
	innerX := box.X + 2
	width := maxInt(box.Width-4, 1)
	cellWidth := maxInt(width/len(actions), 1)
	footerY := box.Y + box.Height - 2
	hits := make([]ui.HitRegion, 0, len(actions))
	for index, action := range actions {
		x := innerX + index*cellWidth
		w := cellWidth
		if index == len(actions)-1 {
			w = maxInt(innerX+width-x, 1)
		}
		hits = append(hits, ui.HitRegion{
			ID: ui.HitID(action), Owner: governanceOverlayID,
			Bounds: ui.Rect{X: x, Y: footerY, Width: w, Height: 1},
			Z:      2, Kind: ui.HitActivate, Action: action, Focusable: true,
		})
	}
	return hits
}

func (c *governanceComponent) Handle(event ui.Event) ui.Result {
	if event.Kind != ui.EventPointer {
		return ui.Result{}
	}
	if event.Pointer.Kind == ui.PointerLeave {
		c.clearHover()
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind == ui.PointerWheel {
		if c.model.question != nil {
			c.model.question.Scroll += event.Pointer.WheelDelta * 3
			c.model.clampQuestionScroll()
		} else if c.model.approval != nil {
			c.model.approval.Scroll += event.Pointer.WheelDelta * 3
			c.model.clampApprovalScroll()
		}
		return ui.Result{Handled: true}
	}
	if event.Pointer.Hit == nil {
		return ui.Result{}
	}
	action := event.Pointer.Hit.Action
	if event.Pointer.Kind == ui.PointerMove {
		c.clearHover()
		if c.model.question != nil && action == "question-option" {
			if index, ok := event.Pointer.Hit.Data.(int); ok {
				c.model.question.Hovered = index
			}
		} else if c.model.approval != nil && action != "surface" {
			c.model.approval.HoveredAction = action
		}
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind != ui.PointerClick {
		return ui.Result{Handled: true}
	}
	if action == "question-option" {
		index, ok := event.Pointer.Hit.Data.(int)
		if !ok || c.model.question == nil {
			return ui.Result{Handled: true}
		}
		activate := c.model.question.Selected == index
		c.model.question.Selected = index
		c.model.ensureQuestionSelectionVisible()
		if !activate {
			return ui.Result{Handled: true}
		}
	}
	return ui.Result{Handled: true, Actions: []ui.Action{{Source: governanceOverlayID, Name: action, Data: event.Pointer.Hit.Data}}}
}

func (c *governanceComponent) clearHover() {
	if c.model.question != nil {
		c.model.question.Hovered = -1
	}
	if c.model.approval != nil {
		c.model.approval.HoveredAction = ""
	}
}

func (m *Model) ensureGovernanceFrame() ui.Frame {
	component := newGovernanceComponent(m)
	top, ok := m.componentRuntime.Overlays.Top()
	if !ok || top.ID != governanceOverlayID {
		// Push before rendering so nested governance preserves the primary
		// overlay (or composer) as the focus owner to restore.
		m.componentRuntime.PushOverlay(governanceOverlayID, governanceOverlayID, true)
	}
	frame := m.componentRuntime.BeginFrame(component, ui.Rect{Width: maxInt(m.width, 1), Height: maxInt(m.height, 1)})
	m.componentFrame = frame
	m.reconcileFrameGraphics(frame)
	return frame
}

func (m *Model) teardownGovernanceFrame() {
	if top, ok := m.componentRuntime.Overlays.Top(); ok && top.ID == governanceOverlayID {
		m.componentRuntime.PopOverlay()
		m.componentRuntime.Unmount(governanceOverlayID)
	}
}

func (m *Model) dispatchGovernanceMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	if m.question == nil && m.approval == nil {
		return nil, false
	}
	m.ensureGovernanceFrame()
	mouse := msg.Mouse()
	event := ui.PointerEvent{X: mouse.X, Y: mouse.Y, Button: int(mouse.Button)}
	switch typed := msg.(type) {
	case tea.MouseMotionMsg:
		event.Kind = ui.PointerMove
	case tea.MouseClickMsg:
		if typed.Button != tea.MouseLeft {
			return nil, true
		}
		event.Kind = ui.PointerClick
	case tea.MouseReleaseMsg:
		event.Kind = ui.PointerRelease
	case tea.MouseWheelMsg:
		event.Kind = ui.PointerWheel
		if typed.Button == tea.MouseWheelUp {
			event.WheelDelta = -1
		} else if typed.Button == tea.MouseWheelDown {
			event.WheelDelta = 1
		} else {
			return nil, true
		}
	default:
		return nil, false
	}
	result := m.componentRuntime.Dispatch(ui.Event{Kind: ui.EventPointer, Pointer: event})
	for _, action := range result.Actions {
		switch action.Name {
		case "approval-once":
			return m.resolveApproval("once", true), true
		case "approval-session":
			return m.resolveApproval("session", true), true
		case "approval-project":
			return m.resolveApproval("project", true), true
		case "approval-deny":
			return m.resolveApproval("deny", false), true
		case "question-option":
			index, _ := action.Data.(int)
			return m.answerQuestion(index), true
		case "question-answer":
			return m.answerQuestionText(), true
		}
	}
	if result.Handled {
		m.layout()
	}
	return nil, result.Handled
}
