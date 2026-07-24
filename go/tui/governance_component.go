package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

const governanceOverlayID ui.ComponentID = "governance-overlay"

type governanceComponent struct {
	ui.Base
	model        *Model
	content      string
	box          ui.Rect
	controls     map[ui.ComponentID]*governanceControl
	controlOrder []ui.Component
}

func newGovernanceComponent(model *Model) *governanceComponent {
	return &governanceComponent{
		Base: ui.Base{ComponentID: governanceOverlayID}, model: model,
		controls: make(map[ui.ComponentID]*governanceControl),
	}
}

type governanceControl struct {
	ui.Base
	parent *governanceComponent
	hit    ui.HitRegion
}

func (c *governanceControl) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: c.hit.Bounds.Width, Height: c.hit.Bounds.Height})
}

func (c *governanceControl) Layout(ui.Rect) {}

func (c *governanceControl) Render(ui.RenderContext) ui.Node {
	hit := c.hit
	hit.Owner = c.ComponentID
	hovered := c.parent.isHovered(c.hit)
	return ui.Node{
		ID: c.ComponentID, Bounds: c.hit.Bounds, Role: map[bool]ui.SemanticRole{true: ui.RoleHovered, false: ui.RoleText}[hovered],
		Focusable: true, Focused: c.Focused(), Hovered: hovered, Hit: []ui.HitRegion{hit},
	}
}

func (c *governanceControl) Handle(event ui.Event) ui.Result {
	if event.Kind == ui.EventKey && (event.Key == "enter" || event.Key == " ") {
		return c.parent.activate(c.hit)
	}
	if event.Kind != ui.EventPointer {
		return ui.Result{}
	}
	if event.Pointer.Kind == ui.PointerLeave {
		c.parent.clearHover()
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind == ui.PointerMove {
		c.parent.setHover(c.hit)
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind == ui.PointerClick {
		return c.parent.activate(c.hit)
	}
	return ui.Result{Handled: true}
}

func (c *governanceComponent) Components() []ui.Component { return c.controlOrder }

func (c *governanceComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (c *governanceComponent) Layout(bounds ui.Rect) {
	c.Bounds = bounds
	c.syncSurface()
}

func (c *governanceComponent) Render(ui.RenderContext) ui.Node {
	c.syncSurface()
	if c.Bounds.Empty() || (c.model.question == nil && c.model.approval == nil) {
		return ui.Node{ID: governanceOverlayID}
	}
	content, box := c.content, c.box
	hits := []ui.HitRegion{{
		ID: "governance-surface", Owner: governanceOverlayID, Bounds: box,
		Kind: ui.HitHover, Action: "surface",
	}}
	children := make([]ui.Node, 0, len(c.controlOrder))
	for _, control := range c.controlOrder {
		children = append(children, control.Render(ui.RenderContext{}))
	}
	return ui.Node{
		ID: governanceOverlayID, Bounds: box, Z: 20, Content: content,
		Focusable: true, Focused: c.Focused(), Hit: hits, Children: children,
	}
}

func (c *governanceComponent) syncSurface() {
	if c.Bounds.Empty() || (c.model.question == nil && c.model.approval == nil) {
		c.content, c.box = "", ui.Rect{}
		c.controlOrder = c.controlOrder[:0]
		return
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
	c.content, c.box = content, box
	hits := c.approvalHits(box)
	if c.model.question != nil {
		hits = c.questionHits(box)
	}
	c.controlOrder = c.controlOrder[:0]
	for _, hit := range hits {
		id := ui.ComponentID("governance-control:" + string(hit.ID))
		control := c.controls[id]
		if control == nil {
			control = &governanceControl{Base: ui.Base{ComponentID: id}, parent: c}
			c.controls[id] = control
		}
		control.hit = hit
		c.controlOrder = append(c.controlOrder, control)
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
	if event.Kind == ui.EventKey {
		return ui.Result{Handled: true, Actions: []ui.Action{{
			Source: governanceOverlayID, Name: "governance-key",
			Data: componentKeyInput{Key: event.Key, Text: event.Text},
		}}}
	}
	if event.Kind == ui.EventPaste {
		if c.model.question != nil && len(c.model.question.Options) == 0 && !c.model.question.Resolving {
			return ui.Result{Handled: true, Actions: []ui.Action{{Source: governanceOverlayID, Name: "question-paste", Data: event.Text}}}
		}
		return ui.Result{Handled: true}
	}
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
		if action == "surface" {
			c.clearHover()
		}
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind != ui.PointerClick {
		return ui.Result{Handled: true}
	}
	return c.activate(*event.Pointer.Hit)
}

func (c *governanceComponent) activate(hit ui.HitRegion) ui.Result {
	if hit.Action == "question-option" {
		index, ok := hit.Data.(int)
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
	return ui.Result{Handled: true, Actions: []ui.Action{{Source: governanceOverlayID, Name: hit.Action, Data: hit.Data}}}
}

func (c *governanceComponent) setHover(hit ui.HitRegion) {
	c.clearHover()
	if c.model.question != nil && hit.Action == "question-option" {
		if index, ok := hit.Data.(int); ok {
			c.model.question.Hovered = index
		}
	} else if c.model.approval != nil && hit.Action != "surface" {
		c.model.approval.HoveredAction = hit.Action
	}
}

func (c *governanceComponent) isHovered(hit ui.HitRegion) bool {
	if c.model.question != nil && hit.Action == "question-option" {
		index, _ := hit.Data.(int)
		return c.model.question.Hovered == index
	}
	return c.model.approval != nil && c.model.approval.HoveredAction == hit.Action
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
