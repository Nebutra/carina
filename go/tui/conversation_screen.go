package tui

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

const (
	conversationScreenID      ui.ComponentID = "conversation"
	conversationBannerID      ui.ComponentID = "conversation-banner"
	conversationActiveWorkID  ui.ComponentID = "conversation-active-work"
	conversationTranscriptID  ui.ComponentID = "conversation-transcript"
	conversationSuggestID     ui.ComponentID = "conversation-suggestions"
	conversationQueueID       ui.ComponentID = "conversation-queue"
	conversationPasteID       ui.ComponentID = "conversation-paste"
	conversationHistoryID     ui.ComponentID = "conversation-history"
	conversationComposerID    ui.ComponentID = "conversation-composer"
	conversationStatusID      ui.ComponentID = "conversation-status"
	conversationStatusLeftID  ui.ComponentID = "conversation-status-context"
	conversationStatusRightID ui.ComponentID = "conversation-status-actions"
	composerAttachmentsID     ui.ComponentID = "composer-attachments"
)

type conversationAttachmentView struct {
	ID    string
	Index int
}

type attachmentHit struct {
	Index int
	ID    string
}

type conversationViewState struct {
	Layout rootLayout

	Banner      string
	ActiveWork  []string
	Transcript  string
	Suggest     []string
	Queue       []string
	Paste       []string
	History     string
	Composer    string
	StatusLeft  string
	StatusRight string
	TinySearch  bool

	Attachments     []conversationAttachmentView
	AttachmentLines []string
	PreviewLines    []string
	PreviewPixel    bool
	PreviewOwner    ui.ComponentID
	PreviewID       string
	PreviewGraphics string
	ComposerCursor  *ui.CursorRequest
	TranscriptCells []conversationTranscriptCellView
	TranscriptTop   int
}

type conversationTranscriptCellView struct {
	ID          ui.ComponentID
	Key         string
	StartLine   int
	LineCount   int
	Collapsible bool
}

type conversationRegion struct {
	ui.Base
	content   string
	focusable bool
	cursor    *ui.CursorRequest
}

type conversationComposerComponent struct {
	ui.Base
	content string
	cursor  *ui.CursorRequest
}

type conversationStatusComponent struct {
	ui.Base
	left  *conversationRegion
	right *conversationRegion
}

func newConversationStatusComponent() *conversationStatusComponent {
	return &conversationStatusComponent{
		Base:  ui.Base{ComponentID: conversationStatusID},
		left:  newConversationRegion(conversationStatusLeftID),
		right: newConversationRegion(conversationStatusRightID),
	}
}

func (c *conversationStatusComponent) Components() []ui.Component {
	return []ui.Component{c.left, c.right}
}
func (c *conversationStatusComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: 1})
}
func (c *conversationStatusComponent) Layout(bounds ui.Rect) {
	c.Bounds = bounds
	leftWidth := minInt(ansi.StringWidth(c.left.content), bounds.Width)
	rightWidth := minInt(ansi.StringWidth(c.right.content), maxInt(bounds.Width-leftWidth, 0))
	c.left.Layout(ui.Rect{X: bounds.X, Y: bounds.Y, Width: leftWidth, Height: minInt(bounds.Height, 1)})
	c.right.Layout(ui.Rect{X: bounds.X + bounds.Width - rightWidth, Y: bounds.Y, Width: rightWidth, Height: minInt(bounds.Height, 1)})
}
func (c *conversationStatusComponent) Render(ctx ui.RenderContext) ui.Node {
	node := ui.Node{ID: conversationStatusID, Bounds: c.Bounds}
	for _, child := range c.Components() {
		if rendered := child.Render(ctx); !rendered.Bounds.Empty() && rendered.Content != "" {
			node.Children = append(node.Children, rendered)
		}
	}
	return node
}
func (c *conversationStatusComponent) Handle(ui.Event) ui.Result { return ui.Result{} }

func newConversationComposerComponent() *conversationComposerComponent {
	return &conversationComposerComponent{Base: ui.Base{ComponentID: conversationComposerID}}
}

func (c *conversationComposerComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (c *conversationComposerComponent) Render(ui.RenderContext) ui.Node {
	node := ui.Node{
		ID: c.ComponentID, Bounds: c.Bounds, Content: c.content,
		Focusable: true, Focused: c.Focused(), Cursor: c.cursor,
	}
	if !c.Bounds.Empty() {
		node.Hit = []ui.HitRegion{{
			ID: "conversation-composer:focus", Owner: conversationComposerID,
			Bounds: c.Bounds, Kind: ui.HitFocus, Action: "focus", Focusable: true,
		}}
	}
	return node
}

func (c *conversationComposerComponent) Handle(event ui.Event) ui.Result {
	switch event.Kind {
	case ui.EventKey:
		return ui.Result{Handled: true, Actions: []ui.Action{{
			Source: conversationComposerID, Name: "composer-key",
			Data: componentKeyInput{Key: event.Key, Text: event.Text},
		}}}
	case ui.EventPaste:
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: conversationComposerID, Name: "composer-paste", Data: event.Text}}}
	case ui.EventPointer:
		if event.Pointer.Hit != nil && event.Pointer.Hit.Action == "focus" {
			return ui.Result{Handled: true}
		}
	}
	return ui.Result{}
}

type conversationTranscriptCell struct {
	ui.Base
	key         string
	startLine   int
	lineCount   int
	collapsible bool
}

func newConversationTranscriptCell(id ui.ComponentID) *conversationTranscriptCell {
	return &conversationTranscriptCell{Base: ui.Base{ComponentID: id}}
}

func (c *conversationTranscriptCell) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: c.lineCount})
}

func (c *conversationTranscriptCell) Render(ui.RenderContext) ui.Node {
	node := ui.Node{
		ID: c.ComponentID, Bounds: c.Bounds, Focusable: c.collapsible,
		Focused: c.Focused(),
	}
	if c.collapsible && !c.Bounds.Empty() {
		node.Hit = []ui.HitRegion{{
			ID: ui.HitID(string(c.ComponentID) + ":toggle"), Owner: c.ComponentID,
			Bounds: c.Bounds, Kind: ui.HitActivate, Action: "transcript-toggle",
			Data: c.key, Focusable: true,
		}}
	}
	return node
}

func (c *conversationTranscriptCell) Handle(event ui.Event) ui.Result {
	if !c.collapsible {
		return ui.Result{}
	}
	if event.Kind == ui.EventKey && (event.Key == "enter" || event.Key == " ") {
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: c.ComponentID, Name: "transcript-toggle", Data: c.key}}}
	}
	if event.Kind == ui.EventPointer && event.Pointer.Kind == ui.PointerClick && event.Pointer.Hit != nil && event.Pointer.Hit.Action == "transcript-toggle" {
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: c.ComponentID, Name: "transcript-toggle", Data: c.key}}}
	}
	return ui.Result{}
}

type conversationTranscriptTimeline struct {
	ui.Base
	content  string
	top      int
	cells    map[ui.ComponentID]*conversationTranscriptCell
	children []ui.Component
}

func newConversationTranscriptTimeline() *conversationTranscriptTimeline {
	return &conversationTranscriptTimeline{
		Base:  ui.Base{ComponentID: conversationTranscriptID},
		cells: make(map[ui.ComponentID]*conversationTranscriptCell),
	}
}

func (t *conversationTranscriptTimeline) Components() []ui.Component { return t.children }

func (t *conversationTranscriptTimeline) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (t *conversationTranscriptTimeline) Sync(content string, top int, views []conversationTranscriptCellView) {
	t.content = content
	t.top = maxInt(top, 0)
	t.children = t.children[:0]
	for _, view := range views {
		cell := t.cells[view.ID]
		if cell == nil {
			cell = newConversationTranscriptCell(view.ID)
			t.cells[view.ID] = cell
		}
		cell.key = view.Key
		cell.startLine = view.StartLine
		cell.lineCount = view.LineCount
		cell.collapsible = view.Collapsible
		t.children = append(t.children, cell)
	}
}

func (t *conversationTranscriptTimeline) Layout(bounds ui.Rect) {
	t.Bounds = bounds
	visibleEnd := t.top + bounds.Height
	for _, component := range t.children {
		cell := component.(*conversationTranscriptCell)
		start := maxInt(cell.startLine, t.top)
		end := minInt(cell.startLine+cell.lineCount, visibleEnd)
		if start >= end || bounds.Empty() {
			cell.Layout(ui.Rect{})
			continue
		}
		cell.Layout(ui.Rect{
			X: bounds.X, Y: bounds.Y + start - t.top,
			Width: bounds.Width, Height: end - start,
		})
	}
}

func (t *conversationTranscriptTimeline) Render(ctx ui.RenderContext) ui.Node {
	node := ui.Node{ID: conversationTranscriptID, Bounds: t.Bounds, Content: t.content}
	if !t.Bounds.Empty() {
		node.Hit = []ui.HitRegion{{
			ID: "conversation-transcript:scroll", Owner: conversationTranscriptID,
			Bounds: t.Bounds, Kind: ui.HitScroll, Action: "scroll-transcript",
		}}
	}
	for _, child := range t.children {
		if rendered := child.Render(ctx); !rendered.Bounds.Empty() {
			node.Children = append(node.Children, rendered)
		}
	}
	return node
}

func (t *conversationTranscriptTimeline) Handle(event ui.Event) ui.Result {
	if event.Kind == ui.EventPointer && event.Pointer.Kind == ui.PointerWheel {
		return ui.Result{Handled: true, Actions: []ui.Action{{
			Source: conversationTranscriptID, Name: "scroll-transcript", Data: event.Pointer.WheelDelta,
		}}}
	}
	return ui.Result{}
}

func newConversationRegion(id ui.ComponentID) *conversationRegion {
	return &conversationRegion{Base: ui.Base{ComponentID: id}}
}

func (r *conversationRegion) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (r *conversationRegion) Render(ui.RenderContext) ui.Node {
	node := ui.Node{
		ID: r.ComponentID, Bounds: r.Bounds, Content: r.content,
		Focusable: r.focusable, Focused: r.Focused(), Cursor: r.cursor,
	}
	if r.focusable && !r.Bounds.Empty() {
		node.Hit = append(node.Hit, ui.HitRegion{
			ID: ui.HitID(string(r.ComponentID) + ":focus"), Owner: r.ComponentID,
			Bounds: r.Bounds, Kind: ui.HitFocus, Action: "focus", Focusable: true,
		})
	}
	return node
}

func (r *conversationRegion) Handle(event ui.Event) ui.Result {
	if event.Kind == ui.EventPointer && event.Pointer.Hit != nil && event.Pointer.Hit.Action == "focus" {
		return ui.Result{Handled: true}
	}
	return ui.Result{}
}

type conversationAttachmentsComponent struct {
	ui.Base
	items           []conversationAttachmentView
	lines           []string
	previewLines    []string
	previewPixel    bool
	previewOwner    ui.ComponentID
	previewID       string
	previewGraphics string
}

func newConversationAttachmentsComponent() *conversationAttachmentsComponent {
	return &conversationAttachmentsComponent{Base: ui.Base{ComponentID: composerAttachmentsID}}
}

func (c *conversationAttachmentsComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: len(c.lines)})
}

func (c *conversationAttachmentsComponent) Render(ctx ui.RenderContext) ui.Node {
	hits := make([]ui.HitRegion, 0, len(c.items))
	for row, item := range c.items {
		y := row + 1
		if y >= c.Bounds.Height {
			break
		}
		hits = append(hits, ui.HitRegion{
			ID: ui.HitID("attachment-chip:" + item.ID), Owner: composerAttachmentsID,
			Bounds: ui.Rect{X: c.Bounds.X, Y: c.Bounds.Y + y, Width: c.Bounds.Width, Height: 1},
			Kind:   ui.HitHover, Action: "attachment-chip",
			Data: attachmentHit{Index: item.Index, ID: item.ID}, Focusable: true,
		})
	}
	node := ui.Node{
		ID: composerAttachmentsID, Bounds: c.Bounds,
		Content: strings.Join(c.lines, "\n"), Hit: hits,
	}
	if len(c.previewLines) == 0 {
		return node
	}
	height := minInt(len(c.previewLines), maxInt(c.Bounds.Y, 1))
	previewBounds := ui.Rect{
		X: c.Bounds.X, Y: maxInt(c.Bounds.Y-height, 0),
		Width: c.Bounds.Width, Height: height,
	}
	preview := ui.Node{
		ID: "composer-media-preview", Bounds: previewBounds, Z: 5,
		Content: strings.Join(c.previewLines[:height], "\n"),
	}
	if c.previewPixel {
		preview.Graphics = []ui.GraphicsPlacement{{
			Owner: c.previewOwner, ID: c.previewID, Bounds: previewBounds,
			Generation: ctx.FrameGeneration, TargetGeneration: ctx.TargetGeneration,
			Payload: c.previewGraphics,
		}}
	}
	node.Children = append(node.Children, preview)
	return node
}

func (c *conversationAttachmentsComponent) Handle(event ui.Event) ui.Result {
	if event.Kind != ui.EventPointer {
		return ui.Result{}
	}
	if event.Pointer.Kind == ui.PointerLeave {
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: composerAttachmentsID, Name: "attachment-leave"}}}
	}
	if event.Pointer.Hit == nil || event.Pointer.Hit.Action != "attachment-chip" {
		return ui.Result{}
	}
	hit, ok := event.Pointer.Hit.Data.(attachmentHit)
	if !ok {
		return ui.Result{Handled: true}
	}
	switch event.Pointer.Kind {
	case ui.PointerMove:
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: composerAttachmentsID, Name: "attachment-preview", Data: hit}}}
	case ui.PointerClick:
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: composerAttachmentsID, Name: "attachment-select", Data: hit}}}
	default:
		return ui.Result{Handled: true}
	}
}

type conversationScreen struct {
	ui.Base
	state conversationViewState

	banner      *conversationRegion
	activeWork  *conversationRegion
	transcript  *conversationTranscriptTimeline
	suggestions *conversationRegion
	queue       *conversationRegion
	paste       *conversationRegion
	attachments *conversationAttachmentsComponent
	history     *conversationRegion
	composer    *conversationComposerComponent
	status      *conversationStatusComponent
	children    []ui.Component
}

func newConversationScreen() *conversationScreen {
	screen := &conversationScreen{
		Base:        ui.Base{ComponentID: conversationScreenID},
		banner:      newConversationRegion(conversationBannerID),
		activeWork:  newConversationRegion(conversationActiveWorkID),
		transcript:  newConversationTranscriptTimeline(),
		suggestions: newConversationRegion(conversationSuggestID),
		queue:       newConversationRegion(conversationQueueID),
		paste:       newConversationRegion(conversationPasteID),
		attachments: newConversationAttachmentsComponent(),
		history:     newConversationRegion(conversationHistoryID),
		composer:    newConversationComposerComponent(),
		status:      newConversationStatusComponent(),
	}
	screen.children = []ui.Component{
		screen.composer, screen.banner, screen.activeWork, screen.transcript, screen.suggestions,
		screen.queue, screen.paste, screen.attachments, screen.history,
		screen.status,
	}
	return screen
}

func (s *conversationScreen) Components() []ui.Component { return s.children }

func (s *conversationScreen) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (s *conversationScreen) Sync(state conversationViewState) {
	s.state = state
	s.banner.content = state.Banner
	s.activeWork.content = strings.Join(state.ActiveWork, "\n")
	s.transcript.Sync(state.Transcript, state.TranscriptTop, state.TranscriptCells)
	s.suggestions.content = strings.Join(state.Suggest, "\n")
	s.queue.content = strings.Join(state.Queue, "\n")
	s.paste.content = strings.Join(state.Paste, "\n")
	s.history.content = state.History
	s.composer.content = state.Composer
	s.composer.cursor = state.ComposerCursor
	s.status.left.content = state.StatusLeft
	s.status.right.content = state.StatusRight
	s.attachments.items = append(s.attachments.items[:0], state.Attachments...)
	s.attachments.lines = append(s.attachments.lines[:0], state.AttachmentLines...)
	s.attachments.previewLines = append(s.attachments.previewLines[:0], state.PreviewLines...)
	s.attachments.previewPixel = state.PreviewPixel
	s.attachments.previewOwner = state.PreviewOwner
	s.attachments.previewID = state.PreviewID
	s.attachments.previewGraphics = state.PreviewGraphics
}

func (s *conversationScreen) Layout(bounds ui.Rect) {
	s.Bounds = bounds
	zeroConversationBounds(s.children)
	if bounds.Empty() {
		return
	}
	if s.state.TinySearch {
		s.composer.Layout(bounds)
		return
	}

	l := s.state.Layout
	y := bounds.Y
	place := func(component ui.Component, height int) {
		if height <= 0 {
			return
		}
		height = minInt(height, maxInt(bounds.Y+bounds.Height-y, 0))
		component.Layout(ui.Rect{X: bounds.X, Y: y, Width: bounds.Width, Height: height})
		y += height
	}
	if l.showBanner {
		place(s.banner, 1)
	}
	place(s.activeWork, l.taskLines)
	if l.showTranscript {
		place(s.transcript, l.viewportHeight)
	}
	place(s.suggestions, l.suggestLines)
	place(s.queue, l.queueLines)
	place(s.paste, len(s.state.Paste))
	place(s.attachments, len(s.state.AttachmentLines))
	place(s.history, l.historyLines)

	composerHeight := l.inputHeight
	if l.framed {
		composerHeight += 2
	}
	place(s.composer, composerHeight)
	if l.showStatus {
		place(s.status, 1)
	}
}

func zeroConversationBounds(components []ui.Component) {
	for _, component := range components {
		component.Layout(ui.Rect{})
	}
}

func (s *conversationScreen) Render(ctx ui.RenderContext) ui.Node {
	node := ui.Node{ID: conversationScreenID, Bounds: s.Bounds}
	for _, child := range s.children {
		if rendered := child.Render(ctx); !rendered.Bounds.Empty() || rendered.Content != "" {
			node.Children = append(node.Children, rendered)
		}
	}
	return node
}

func (s *conversationScreen) Handle(event ui.Event) ui.Result {
	switch event.Kind {
	case ui.EventKey:
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: conversationScreenID, Name: "conversation-key", Data: event.Key}}}
	case ui.EventPaste:
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: conversationScreenID, Name: "conversation-paste", Data: event.Text}}}
	case ui.EventResize:
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: conversationScreenID, Name: "conversation-resize", Data: ui.Size{Width: event.Width, Height: event.Height}}}}
	case ui.EventFocus:
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: conversationScreenID, Name: "conversation-focus"}}}
	case ui.EventBlur:
		return ui.Result{Handled: true, Actions: []ui.Action{{Source: conversationScreenID, Name: "conversation-blur"}}}
	default:
		return ui.Result{}
	}
}

func (m *Model) conversationViewState() conversationViewState {
	l := m.root
	state := conversationViewState{Layout: l}
	if l.showBanner {
		state.Banner = fitRenderedLine(m.th.Style(theme.RoleWarning).Render(m.banner()), l.width)
	}
	if l.taskLines > 0 {
		lines := m.taskTreeLines()
		state.ActiveWork = append([]string(nil), lines[:minInt(l.taskLines, len(lines))]...)
	}
	if l.showTranscript {
		if m.sidePaneActive() {
			state.Transcript = m.dualPaneTranscriptView(l.width, maxInt(l.viewportHeight, 1))
		} else {
			state.Transcript = m.vp.View()
			if l.width >= 3 {
				state.Transcript = lipgloss.NewStyle().Width(l.width).Padding(0, 1).Render(state.Transcript)
			}
		}
		state.TranscriptTop = m.vp.YOffset()
		line := 0
		for index := range m.tr.entries {
			entry := &m.tr.entries[index]
			id := ui.ComponentID("transcript-cell:" + entry.key)
			if entry.key == "" {
				id = ui.ComponentID("transcript-cell:plain:" + strconv.Itoa(index))
			}
			collapsible := entry.presentation != nil && entry.presentation.Collapsible
			state.TranscriptCells = append(state.TranscriptCells, conversationTranscriptCellView{
				ID: id, Key: entry.key, StartLine: line,
				LineCount: len(entry.lines), Collapsible: collapsible,
			})
			line += len(entry.lines)
		}
	}
	state.Suggest = append([]string(nil), m.visibleSuggestPanelLines(l.suggestLines)...)
	if l.queueLines > 0 {
		lines := m.queuePanelLines()
		state.Queue = append([]string(nil), lines[:minInt(l.queueLines, len(lines))]...)
	}

	allPaste := m.pastePanelLines()
	visiblePaste := allPaste[:minInt(l.pasteLines, len(allPaste))]
	attachmentLines := m.attachmentPanelLines()
	attachmentStart := len(allPaste) - len(attachmentLines)
	pasteEnd := minInt(len(visiblePaste), maxInt(attachmentStart, 0))
	state.Paste = append([]string(nil), visiblePaste[:pasteEnd]...)
	if len(visiblePaste) > pasteEnd {
		state.AttachmentLines = append([]string(nil), visiblePaste[pasteEnd:]...)
		visibleItems := maxInt(len(state.AttachmentLines)-1, 0)
		for index := 0; index < minInt(visibleItems, len(m.attachments)); index++ {
			state.Attachments = append(state.Attachments, conversationAttachmentView{ID: m.attachments[index].ID, Index: index})
		}
		state.PreviewLines = append([]string(nil), m.attachmentPreviewLines...)
		state.PreviewPixel = m.attachmentPreviewPixel
		state.PreviewOwner = ui.ComponentID(m.attachmentGraphicsOwner)
		state.PreviewID = m.attachmentPreviewID
		state.PreviewGraphics = m.attachmentGraphicsKey
	}

	if l.historyLines > 0 {
		state.History = m.historySearchPanelLine(l.width)
	}
	frame := m.borderStyle(lipgloss.RoundedBorder()).Width(l.width)
	if l.framed {
		state.Composer = frame.Render(m.input.View())
	} else {
		state.Composer = m.input.View()
	}
	if l.showStatus {
		projection := m.statusFooterProjection(l.width)
		state.StatusLeft, state.StatusRight = projection.Left, projection.Right
	}
	if m.historySearch != nil && l.historyLines == 0 {
		state.TinySearch = true
		state.Composer = m.historySearchPanelLine(l.width)
	}
	state.ComposerCursor = m.conversationCursorRequest(l)
	return state
}

func (m *Model) conversationCursorRequest(l rootLayout) *ui.CursorRequest {
	if m.editor != nil || m.width <= 0 || m.height <= 0 {
		return nil
	}
	if m.historySearch != nil {
		y := 0
		if l.historyLines > 0 {
			y = l.historyY
		}
		return &ui.CursorRequest{
			Owner: conversationComposerID,
			X:     clampInt(m.historySearchCursorX(l.width), 0, l.width-1),
			Y:     clampInt(y, 0, l.height-1), Visible: true,
		}
	}
	cursor := m.input.Cursor()
	if cursor == nil {
		return nil
	}
	return &ui.CursorRequest{
		Owner: conversationComposerID,
		X:     clampInt(cursor.Position.X+l.inputX, 0, l.width-1),
		Y:     clampInt(cursor.Position.Y+l.inputY, 0, l.height-1), Visible: true,
	}
}

func (m *Model) ensureConversationFrame() ui.Frame {
	if m.conversationScreen == nil {
		m.conversationScreen = newConversationScreen()
	}
	m.conversationScreen.Sync(m.conversationViewState())
	if current := m.componentRuntime.Screens.Current(); current.ID != ui.ScreenConversation || current.Root != conversationScreenID {
		m.componentRuntime.Screens.Transition(ui.ScreenConversation, conversationScreenID, m.componentRuntime.Focus.Snapshot(), nil)
	}
	frame := m.componentRuntime.BeginFrame(m.conversationScreen, ui.Rect{Width: maxInt(m.width, 1), Height: maxInt(m.height, 1)})
	m.componentFrame = frame
	m.reconcileFrameGraphics(frame)
	return frame
}
