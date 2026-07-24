package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

const operationalPagerID ui.ComponentID = "operational-pager"

type operationalPagerComponent struct {
	ui.Base
	model        *Model
	controls     map[ui.ComponentID]*operationalControl
	controlOrder []ui.Component
}

func newOperationalPagerComponent(model *Model) *operationalPagerComponent {
	return &operationalPagerComponent{
		Base: ui.Base{ComponentID: operationalPagerID}, model: model,
		controls: make(map[ui.ComponentID]*operationalControl),
	}
}

type operationalControl struct {
	ui.Base
	parent *operationalPagerComponent
	hit    ui.HitRegion
}

func (c *operationalControl) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: c.hit.Bounds.Width, Height: 1})
}
func (c *operationalControl) Layout(ui.Rect) {}
func (c *operationalControl) Render(ui.RenderContext) ui.Node {
	hit := c.hit
	hit.Owner = c.ComponentID
	return ui.Node{ID: c.ComponentID, Bounds: hit.Bounds, Focusable: true, Focused: c.Focused(), Hit: []ui.HitRegion{hit}}
}
func (c *operationalControl) Handle(event ui.Event) ui.Result {
	if event.Kind == ui.EventKey && (event.Key == "enter" || event.Key == " ") {
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: operationalPagerID, Name: c.hit.Action}}}
	}
	if event.Kind == ui.EventPointer {
		return c.parent.handleControlPointer(event.Pointer, c.hit)
	}
	return ui.Result{}
}

func (c *operationalPagerComponent) Components() []ui.Component { return c.controlOrder }

func (c *operationalPagerComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (c *operationalPagerComponent) Render(ui.RenderContext) ui.Node {
	state := c.model.transcriptPager
	if state == nil || state.operationalKind == "" || c.Bounds.Empty() {
		return ui.Node{ID: operationalPagerID}
	}
	content := c.model.transcriptPagerView(c.Bounds.Width, c.Bounds.Height)
	refresh, close := c.model.operationalActionLabels()
	refreshWidth := minInt(ansi.StringWidth(refresh), c.Bounds.Width)
	closeWidth := minInt(ansi.StringWidth(close), maxInt(c.Bounds.Width-refreshWidth-2, 0))
	footerY := c.Bounds.Y + c.Bounds.Height - 1
	hits := []ui.HitRegion{{
		ID: "operational-surface", Owner: operationalPagerID, Bounds: c.Bounds,
		Kind: ui.HitHover, Action: "surface",
	}}
	if refreshWidth > 0 {
		hits = append(hits, ui.HitRegion{
			ID: "operational-refresh", Owner: operationalPagerID,
			Bounds: ui.Rect{X: c.Bounds.X, Y: footerY, Width: refreshWidth, Height: 1},
			Z:      2, Kind: ui.HitActivate, Action: "refresh", Focusable: true,
		})
	}
	if closeWidth > 0 {
		hits = append(hits, ui.HitRegion{
			ID: "operational-close", Owner: operationalPagerID,
			Bounds: ui.Rect{X: c.Bounds.X + c.Bounds.Width - closeWidth, Y: footerY, Width: closeWidth, Height: 1},
			Z:      2, Kind: ui.HitActivate, Action: "close", Focusable: true,
		})
	}
	c.syncControls(hits[1:])
	children := make([]ui.Node, 0, len(c.controlOrder))
	for _, control := range c.controlOrder {
		children = append(children, control.Render(ui.RenderContext{}))
	}
	return ui.Node{
		ID: operationalPagerID, Bounds: c.Bounds, Content: content,
		Focusable: true, Focused: c.Focused(), Hit: hits[:1], Children: children,
	}
}

func (c *operationalPagerComponent) syncControls(hits []ui.HitRegion) {
	c.controlOrder = c.controlOrder[:0]
	for _, hit := range hits {
		id := ui.ComponentID("operational-control:" + string(hit.ID))
		control := c.controls[id]
		if control == nil {
			control = &operationalControl{Base: ui.Base{ComponentID: id}, parent: c}
			c.controls[id] = control
		}
		control.hit = hit
		c.controlOrder = append(c.controlOrder, control)
	}
}

func (c *operationalPagerComponent) handleControlPointer(event ui.PointerEvent, hit ui.HitRegion) ui.Result {
	state := c.model.transcriptPager
	if state == nil {
		return ui.Result{}
	}
	if event.Kind == ui.PointerLeave {
		state.hoveredAction = ""
		return ui.Result{Handled: true}
	}
	if event.Kind == ui.PointerMove {
		state.hoveredAction = hit.Action
		return ui.Result{Handled: true}
	}
	if event.Kind == ui.PointerClick {
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: operationalPagerID, Name: hit.Action}}}
	}
	return ui.Result{Handled: true}
}

func (c *operationalPagerComponent) Handle(event ui.Event) ui.Result {
	state := c.model.transcriptPager
	if state == nil || state.operationalKind == "" {
		return ui.Result{}
	}
	if event.Kind == ui.EventKey {
		return ui.Result{Handled: true, Actions: []ui.Action{{
			Source: operationalPagerID, Name: "operational-key",
			Data: componentKeyInput{Key: event.Key, Text: event.Text},
		}}}
	}
	if event.Kind == ui.EventPaste {
		return ui.Result{Handled: true}
	}
	if event.Kind != ui.EventPointer {
		return ui.Result{}
	}
	if event.Pointer.Kind == ui.PointerLeave {
		state.hoveredAction = ""
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind == ui.PointerWheel {
		state.scrollBy(event.Pointer.WheelDelta * 3)
		c.model.clampTranscriptPagerScroll(c.model.transcriptPagerLines())
		return ui.Result{Handled: true}
	}
	if event.Pointer.Hit == nil {
		return ui.Result{}
	}
	action := event.Pointer.Hit.Action
	if event.Pointer.Kind == ui.PointerMove {
		if action == "surface" {
			state.hoveredAction = ""
		} else {
			state.hoveredAction = action
		}
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind == ui.PointerClick && (action == "refresh" || action == "close") {
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: operationalPagerID, Name: action}}}
	}
	return ui.Result{Handled: true}
}

func (m *Model) operationalActionLabels() (string, string) {
	refresh := "[r] " + m.text(MsgSettingsActionRefresh, nil)
	closeKey := m.keys.label(KeyContextPager, ActionPagerClose)
	closeText := m.localizedKeyDescription(KeyBindingDescriptor{Action: ActionPagerClose, Description: "close"})
	return refresh, "[" + closeKey + "] " + closeText
}

func (m *Model) ensureOperationalFrame() ui.Frame {
	component := newOperationalPagerComponent(m)
	frame := m.componentRuntime.BeginFrame(component, ui.Rect{Width: maxInt(m.width, 1), Height: maxInt(m.height, 1)})
	m.componentFrame = frame
	m.reconcileFrameGraphics(frame)
	return frame
}

func joinOperationalFooter(refresh, close string, width int) string {
	gap := "  "
	if ansi.StringWidth(refresh)+ansi.StringWidth(gap)+ansi.StringWidth(close) > width {
		return fitRenderedLine(close, width)
	}
	return strings.Repeat(" ", maxInt(width-ansi.StringWidth(refresh)-ansi.StringWidth(gap)-ansi.StringWidth(close), 0)) + refresh + gap + close
}
