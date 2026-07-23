package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

const operationalPagerID ui.ComponentID = "operational-pager"

type operationalPagerComponent struct {
	ui.Base
	model *Model
}

func newOperationalPagerComponent(model *Model) *operationalPagerComponent {
	return &operationalPagerComponent{Base: ui.Base{ComponentID: operationalPagerID}, model: model}
}

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
	return ui.Node{
		ID: operationalPagerID, Bounds: c.Bounds, Content: content,
		Focusable: true, Focused: c.Focused(), Hit: hits,
	}
}

func (c *operationalPagerComponent) Handle(event ui.Event) ui.Result {
	state := c.model.transcriptPager
	if state == nil || state.operationalKind == "" || event.Kind != ui.EventPointer {
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

func (m *Model) dispatchOperationalMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	if m.transcriptPager == nil || m.transcriptPager.operationalKind == "" {
		return nil, false
	}
	m.ensureOperationalFrame()
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
		case "refresh":
			return m.refreshOperationalPager(), true
		case "close":
			return m.closeTranscriptPager(), true
		}
	}
	if result.Handled {
		m.layout()
	}
	return nil, result.Handled
}

func joinOperationalFooter(refresh, close string, width int) string {
	gap := "  "
	if ansi.StringWidth(refresh)+ansi.StringWidth(gap)+ansi.StringWidth(close) > width {
		return fitRenderedLine(close, width)
	}
	return strings.Repeat(" ", maxInt(width-ansi.StringWidth(refresh)-ansi.StringWidth(gap)-ansi.StringWidth(close), 0)) + refresh + gap + close
}
