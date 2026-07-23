package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

const composerAttachmentsID ui.ComponentID = "composer-attachments"

type attachmentHit struct {
	Index int
	ID    string
}

// composerAttachmentsComponent is the interaction owner for atomic image
// chips. The legacy conversation shell still places its content, but pointer
// geometry and actions come only from this rendered node.
type composerAttachmentsComponent struct {
	ui.Base
	model *Model
}

func newComposerAttachmentsComponent(model *Model) *composerAttachmentsComponent {
	return &composerAttachmentsComponent{Base: ui.Base{ComponentID: composerAttachmentsID}, model: model}
}

func (c *composerAttachmentsComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: len(c.model.attachmentPanelLines())})
}

func (c *composerAttachmentsComponent) Render(ctx ui.RenderContext) ui.Node {
	hits := make([]ui.HitRegion, 0, len(c.model.attachments))
	for index, attachment := range c.model.attachments {
		row := 1 + index
		if row >= c.Bounds.Height {
			break
		}
		hits = append(hits, ui.HitRegion{
			ID: ui.HitID("attachment-chip:" + attachment.ID), Owner: composerAttachmentsID,
			Bounds: ui.Rect{X: c.Bounds.X, Y: c.Bounds.Y + row, Width: c.Bounds.Width, Height: 1},
			Kind:   ui.HitHover, Action: "attachment-chip", Data: attachmentHit{Index: index, ID: attachment.ID},
		})
	}
	node := ui.Node{ID: composerAttachmentsID, Bounds: c.Bounds, Hit: hits}
	if len(c.model.attachmentPreviewLines) > 0 {
		height := minInt(len(c.model.attachmentPreviewLines), maxInt(c.Bounds.Y, 1))
		previewBounds := ui.Rect{X: c.Bounds.X, Y: maxInt(c.Bounds.Y-height, 0), Width: c.Bounds.Width, Height: height}
		preview := ui.Node{
			ID: "composer-media-preview", Bounds: previewBounds, Z: 5,
			Content: strings.Join(c.model.attachmentPreviewLines[:height], "\n"),
		}
		if c.model.attachmentPreviewPixel {
			preview.Graphics = []ui.GraphicsPlacement{{
				Owner: ui.ComponentID(c.model.attachmentGraphicsOwner), ID: c.model.attachmentPreviewID, Bounds: previewBounds,
				Generation: ctx.FrameGeneration, TargetGeneration: ctx.TargetGeneration,
				Payload: c.model.attachmentGraphicsKey,
			}}
		}
		node.Children = append(node.Children, preview)
	}
	return node
}

func (c *composerAttachmentsComponent) Handle(event ui.Event) ui.Result {
	if event.Kind != ui.EventPointer {
		return ui.Result{}
	}
	if event.Pointer.Kind == ui.PointerLeave {
		c.restoreKeyboardPreview()
		return ui.Result{Handled: true}
	}
	if event.Pointer.Hit == nil || event.Pointer.Hit.Action != "attachment-chip" {
		return ui.Result{}
	}
	hit, ok := event.Pointer.Hit.Data.(attachmentHit)
	if !ok || hit.Index < 0 || hit.Index >= len(c.model.attachments) || c.model.attachments[hit.Index].ID != hit.ID {
		return ui.Result{Handled: true}
	}
	switch event.Pointer.Kind {
	case ui.PointerMove:
		c.model.attachmentHoverID = hit.ID
		c.model.syncAttachmentPreviewOwner()
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: composerAttachmentsID, Name: "preview", Data: hit.ID}}}
	case ui.PointerClick:
		c.model.attachmentFocus = hit.Index
		c.model.attachmentHoverID = hit.ID
		c.model.syncAttachmentPreviewOwner()
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: composerAttachmentsID, Name: "select", Data: hit.ID}}}
	default:
		return ui.Result{Handled: true}
	}
}

func (c *composerAttachmentsComponent) restoreKeyboardPreview() {
	c.model.attachmentHoverID = ""
	c.model.syncAttachmentPreviewOwner()
}

func (m *Model) ensureAttachmentFrame() ui.Frame {
	if len(m.attachments) == 0 || m.root.pasteLines == 0 {
		return ui.Frame{}
	}
	panelLines := m.attachmentPanelLines()
	allPasteLines := m.pastePanelLines()
	start := len(allPasteLines) - len(panelLines)
	if start < 0 || start >= m.root.pasteLines {
		return ui.Frame{}
	}
	visible := minInt(len(panelLines), m.root.pasteLines-start)
	frameTop := 0
	if m.root.framed {
		frameTop = 1
	}
	pasteY := m.root.inputY - frameTop - m.root.historyLines - m.root.pasteLines
	component := newComposerAttachmentsComponent(m)
	frame := m.componentRuntime.BeginFrame(component, ui.Rect{
		X: 0, Y: pasteY + start, Width: maxInt(m.width, 1), Height: maxInt(visible, 1),
	})
	m.componentFrame = frame
	m.reconcileFrameGraphics(frame)
	return frame
}

func (m *Model) dispatchAttachmentMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	if m.sessionPicker != nil || len(m.attachments) == 0 {
		return nil, false
	}
	frame := m.ensureAttachmentFrame()
	if frame.Generation == 0 {
		return nil, false
	}
	mouse := msg.Mouse()
	event := ui.PointerEvent{X: mouse.X, Y: mouse.Y, Button: int(mouse.Button)}
	switch typed := msg.(type) {
	case tea.MouseMotionMsg:
		event.Kind = ui.PointerMove
	case tea.MouseClickMsg:
		if typed.Button != tea.MouseLeft {
			return nil, false
		}
		event.Kind = ui.PointerClick
	case tea.MouseReleaseMsg:
		event.Kind = ui.PointerRelease
	default:
		return nil, false
	}
	result := m.componentRuntime.Dispatch(ui.Event{Kind: ui.EventPointer, Pointer: event})
	if result.Handled {
		m.layout()
	}
	return nil, result.Handled
}
