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
	conversationScreenID     ui.ComponentID = "conversation"
	conversationBannerID     ui.ComponentID = "conversation-banner"
	conversationActiveWorkID ui.ComponentID = "conversation-active-work"
	conversationTranscriptID ui.ComponentID = "conversation-transcript"
	conversationSuggestID    ui.ComponentID = "conversation-suggestions"
	conversationQueueID      ui.ComponentID = "conversation-queue"
	conversationPasteID      ui.ComponentID = "conversation-paste"
	conversationHistoryID    ui.ComponentID = "conversation-history"
	conversationComposerID   ui.ComponentID = "conversation-composer"
	conversationStatusID     ui.ComponentID = "conversation-status"
	composerAttachmentsID    ui.ComponentID = "composer-attachments"
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

	Banner     string
	ActiveWork []string
	Suggest    []string
	Queue      []string
	Paste      []string
	History    string
	Composer   string
	Status     conversationStatusView
	TinySearch bool

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
	ID        ui.ComponentID
	Content   string
	Role      ui.SemanticRole
	StartLine int
	LineCount int
	Actions   []conversationTranscriptActionView
}

type conversationStatusSlotView struct {
	ID   ui.ComponentID
	Text string
	Role ui.SemanticRole
}

type conversationStatusView struct {
	Left  []conversationStatusSlotView
	Right []conversationStatusSlotView
}

type conversationTranscriptActionView struct {
	Name     string
	Label    string
	Shortcut string
	Data     transcriptComponentAction
}

type transcriptComponentAction struct {
	Key         string
	Name        string
	TaskID      string
	ArtifactIDs []string
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
	slots    map[ui.ComponentID]*conversationStatusSlot
	left     []*conversationStatusSlot
	right    []*conversationStatusSlot
	children []ui.Component
}

type conversationStatusSlot struct {
	ui.Base
	text   string
	prefix string
	role   ui.SemanticRole
}

func newConversationStatusComponent() *conversationStatusComponent {
	return &conversationStatusComponent{
		Base:  ui.Base{ComponentID: conversationStatusID},
		slots: make(map[ui.ComponentID]*conversationStatusSlot),
	}
}

func (c *conversationStatusComponent) Components() []ui.Component {
	return c.children
}
func (c *conversationStatusComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: 1})
}
func (c *conversationStatusComponent) Layout(bounds ui.Rect) {
	c.Bounds = bounds
	x := bounds.X
	for _, slot := range c.left {
		width := minInt(ansi.StringWidth(slot.prefix+slot.text), maxInt(bounds.X+bounds.Width-x, 0))
		slot.Layout(ui.Rect{X: x, Y: bounds.Y, Width: width, Height: minInt(bounds.Height, 1)})
		x += width
	}
	rightWidth := 0
	for _, slot := range c.right {
		rightWidth += ansi.StringWidth(slot.prefix + slot.text)
	}
	x = bounds.X + maxInt(bounds.Width-rightWidth, 0)
	for _, slot := range c.right {
		width := minInt(ansi.StringWidth(slot.prefix+slot.text), maxInt(bounds.X+bounds.Width-x, 0))
		slot.Layout(ui.Rect{X: x, Y: bounds.Y, Width: width, Height: minInt(bounds.Height, 1)})
		x += width
	}
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

func (c *conversationStatusComponent) Sync(view conversationStatusView) {
	c.left = c.syncSide(c.left[:0], view.Left)
	c.right = c.syncSide(c.right[:0], view.Right)
	c.children = c.children[:0]
	for _, slot := range c.left {
		c.children = append(c.children, slot)
	}
	for _, slot := range c.right {
		c.children = append(c.children, slot)
	}
}

func (c *conversationStatusComponent) syncSide(dst []*conversationStatusSlot, views []conversationStatusSlotView) []*conversationStatusSlot {
	for index, view := range views {
		slot := c.slots[view.ID]
		if slot == nil {
			slot = &conversationStatusSlot{Base: ui.Base{ComponentID: view.ID}}
			c.slots[view.ID] = slot
		}
		slot.text = view.Text
		slot.role = view.Role
		if index == 0 {
			slot.prefix = ""
		} else {
			slot.prefix = " · "
		}
		dst = append(dst, slot)
	}
	return dst
}

func (s *conversationStatusSlot) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: ansi.StringWidth(s.prefix + s.text), Height: 1})
}

func (s *conversationStatusSlot) Render(ui.RenderContext) ui.Node {
	return ui.Node{ID: s.ComponentID, Bounds: s.Bounds, Content: s.prefix + s.text, Role: s.role}
}

func (s *conversationStatusSlot) Handle(ui.Event) ui.Result { return ui.Result{} }

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
	lines       []string
	role        ui.SemanticRole
	startLine   int
	lineCount   int
	visibleFrom int
	actions     []conversationTranscriptActionView
}

func newConversationTranscriptCell(id ui.ComponentID) *conversationTranscriptCell {
	return &conversationTranscriptCell{Base: ui.Base{ComponentID: id}}
}

func (c *conversationTranscriptCell) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: c.lineCount})
}

func (c *conversationTranscriptCell) sync(view conversationTranscriptCellView) {
	c.lines = strings.Split(view.Content, "\n")
	c.role = view.Role
	c.startLine = view.StartLine
	c.lineCount = view.LineCount
	c.actions = append(c.actions[:0], view.Actions...)
}

func (c *conversationTranscriptCell) Render(ctx ui.RenderContext) ui.Node {
	visibleEnd := minInt(c.visibleFrom+c.Bounds.Height, len(c.lines))
	content := ""
	if c.visibleFrom >= 0 && c.visibleFrom < visibleEnd {
		content = strings.Join(c.lines[c.visibleFrom:visibleEnd], "\n")
	}
	hovered := c.hovered(ctx.Hovered)
	visibleActions := c.visibleActions()
	actionBar := transcriptActionBar(visibleActions)
	if (c.Focused() || hovered) && len(visibleActions) > 0 {
		content = overlayTranscriptActions(content, actionBar, c.Bounds.Width)
	}
	node := ui.Node{
		ID: c.ComponentID, Bounds: c.Bounds, Content: content, Role: c.role,
		ContentStyled: true, Focusable: len(c.actions) > 0,
		Focused: c.Focused(), Hovered: hovered,
	}
	if len(c.actions) > 0 && !c.Bounds.Empty() {
		node.Hit = append(node.Hit, ui.HitRegion{
			ID: c.hoverHitID(), Owner: c.ComponentID, Bounds: c.Bounds,
			Kind: ui.HitHover, Action: "transcript-focus", Focusable: true,
		})
		x := c.Bounds.X + c.Bounds.Width - ansi.StringWidth(actionBar)
		for _, action := range visibleActions {
			label := transcriptActionToken(action)
			width := ansi.StringWidth(label)
			hitAction := "transcript-action"
			if action.Data.Name == "toggle" {
				hitAction = "transcript-toggle"
			}
			node.Hit = append(node.Hit, ui.HitRegion{
				ID: c.actionHitID(action.Name), Owner: c.ComponentID,
				Bounds: ui.Rect{X: x, Y: c.Bounds.Y, Width: width, Height: 1},
				Kind:   ui.HitActivate, Action: hitAction, Data: action.Data,
				Focusable: true,
			})
			x += width + 1
		}
	}
	return node
}

func (c *conversationTranscriptCell) Handle(event ui.Event) ui.Result {
	if len(c.actions) == 0 {
		return ui.Result{}
	}
	if event.Kind == ui.EventKey {
		for index, action := range c.actions {
			if (index == 0 && (event.Key == "enter" || event.Key == " ")) || event.Key == action.Shortcut {
				return c.actionResult(action.Data)
			}
		}
		return ui.Result{}
	}
	if event.Kind == ui.EventPointer {
		if event.Pointer.Kind == ui.PointerLeave || event.Pointer.Kind == ui.PointerMove {
			return ui.Result{Handled: true}
		}
		if event.Pointer.Kind == ui.PointerClick && event.Pointer.Hit != nil {
			if event.Pointer.Hit.Action == "transcript-action" || event.Pointer.Hit.Action == "transcript-toggle" {
				if action, ok := event.Pointer.Hit.Data.(transcriptComponentAction); ok {
					return c.actionResult(action)
				}
			}
			return ui.Result{Handled: true}
		}
	}
	return ui.Result{}
}

func (c *conversationTranscriptCell) actionResult(action transcriptComponentAction) ui.Result {
	return ui.Result{Handled: true, Actions: []ui.Action{{Source: c.ComponentID, Name: "transcript-action", Data: action}}}
}

func (c *conversationTranscriptCell) hoverHitID() ui.HitID {
	return ui.HitID(string(c.ComponentID) + ":hover")
}

func (c *conversationTranscriptCell) actionHitID(name string) ui.HitID {
	return ui.HitID(string(c.ComponentID) + ":action:" + name)
}

func (c *conversationTranscriptCell) hovered(hit ui.HitID) bool {
	if hit == c.hoverHitID() {
		return true
	}
	for _, action := range c.actions {
		if hit == c.actionHitID(action.Name) {
			return true
		}
	}
	return false
}

func transcriptActionBar(actions []conversationTranscriptActionView) string {
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		parts = append(parts, transcriptActionToken(action))
	}
	return strings.Join(parts, " ")
}

func (c *conversationTranscriptCell) visibleActions() []conversationTranscriptActionView {
	if c.Bounds.Width <= 0 {
		return nil
	}
	visible := make([]conversationTranscriptActionView, 0, len(c.actions))
	used := 0
	for _, action := range c.actions {
		width := ansi.StringWidth(transcriptActionToken(action))
		if len(visible) > 0 {
			width++
		}
		if used+width > c.Bounds.Width {
			continue
		}
		visible = append(visible, action)
		used += width
	}
	return visible
}

func transcriptActionToken(action conversationTranscriptActionView) string {
	return "[" + action.Shortcut + " " + action.Label + "]"
}

func overlayTranscriptActions(content, actions string, width int) string {
	if content == "" || actions == "" || width <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	actionWidth := ansi.StringWidth(actions)
	if actionWidth >= width {
		lines[0] = fitRenderedLine(actions, width)
		return strings.Join(lines, "\n")
	}
	leftWidth := maxInt(width-actionWidth-1, 0)
	left := fitRenderedLine(lines[0], leftWidth)
	gap := width - ansi.StringWidth(left) - actionWidth
	lines[0] = left + strings.Repeat(" ", maxInt(gap, 1)) + actions
	return strings.Join(lines, "\n")
}

type conversationTranscriptTimeline struct {
	ui.Base
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

func (t *conversationTranscriptTimeline) Sync(top int, views []conversationTranscriptCellView) {
	t.top = maxInt(top, 0)
	t.children = t.children[:0]
	for _, view := range views {
		cell := t.cells[view.ID]
		if cell == nil {
			cell = newConversationTranscriptCell(view.ID)
			t.cells[view.ID] = cell
		}
		cell.sync(view)
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
			cell.visibleFrom = 0
			cell.Layout(ui.Rect{})
			continue
		}
		cell.Layout(ui.Rect{
			X: bounds.X, Y: bounds.Y + start - t.top,
			Width: bounds.Width, Height: end - start,
		})
		cell.visibleFrom = start - cell.startLine
	}
}

func (t *conversationTranscriptTimeline) Render(ctx ui.RenderContext) ui.Node {
	node := ui.Node{ID: conversationTranscriptID, Bounds: t.Bounds}
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
	s.transcript.Sync(state.TranscriptTop, state.TranscriptCells)
	s.suggestions.content = strings.Join(state.Suggest, "\n")
	s.queue.content = strings.Join(state.Queue, "\n")
	s.paste.content = strings.Join(state.Paste, "\n")
	s.history.content = state.History
	s.composer.content = state.Composer
	s.composer.cursor = state.ComposerCursor
	s.status.Sync(state.Status)
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
			content := m.dualPaneTranscriptView(l.width, maxInt(l.viewportHeight, 1))
			state.TranscriptCells = append(state.TranscriptCells, conversationTranscriptCellView{
				ID: "transcript-cell:dual-pane", Content: content, Role: ui.RoleText,
				LineCount: len(strings.Split(content, "\n")),
			})
		} else {
			state.TranscriptTop = m.vp.YOffset()
			line := 0
			for index := range m.tr.entries {
				entry := &m.tr.entries[index]
				id := ui.ComponentID("transcript-cell:" + entry.key)
				if entry.key == "" {
					id = ui.ComponentID("transcript-cell:plain:" + strconv.Itoa(index))
				}
				content := entry.rendered
				if l.width >= 3 {
					content = lipgloss.NewStyle().Width(l.width).Padding(0, 1).Render(content)
				}
				state.TranscriptCells = append(state.TranscriptCells, conversationTranscriptCellView{
					ID: id, Content: content,
					Role: presentationSemanticRole(entry.presentation), StartLine: line,
					LineCount: len(entry.lines),
					Actions:   m.transcriptComponentActions(entry),
				})
				line += len(entry.lines)
			}
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
		state.Status = m.conversationStatusView(l.width)
	}
	if m.historySearch != nil && l.historyLines == 0 {
		state.TinySearch = true
		state.Composer = m.historySearchPanelLine(l.width)
	}
	state.ComposerCursor = m.conversationCursorRequest(l)
	return state
}

func presentationSemanticRole(p *eventPresentation) ui.SemanticRole {
	if p == nil {
		return ui.RoleText
	}
	switch p.Status {
	case statusRunning:
		return ui.RoleInfo
	case statusSuccess:
		return ui.RoleSuccess
	case statusFailure:
		return ui.RoleError
	case statusNeedsAuth:
		return ui.RoleWarning
	default:
		if p.Kind == presentationAgent {
			return ui.RoleText
		}
		return ui.RoleMuted
	}
}

func (m *Model) transcriptComponentActions(entry *entry) []conversationTranscriptActionView {
	if entry == nil || entry.presentation == nil || entry.key == "" {
		return nil
	}
	p := entry.presentation
	base := transcriptComponentAction{Key: entry.key, TaskID: p.TaskID, ArtifactIDs: append([]string(nil), p.ArtifactIDs...)}
	var actions []conversationTranscriptActionView
	if p.Collapsible {
		name, label := "fold", m.text(MsgTranscriptFold, nil)
		if p.Collapsed {
			name, label = "expand", m.text(MsgTranscriptOpen, nil)
		}
		action := base
		action.Name = "toggle"
		actions = append(actions, conversationTranscriptActionView{Name: name, Label: label, Shortcut: "enter", Data: action})
	}
	inspect := base
	inspect.Name = "inspect"
	actions = append(actions, conversationTranscriptActionView{Name: "inspect", Label: m.text(MsgTranscriptInspect, nil), Shortcut: "i", Data: inspect})
	copyAction := base
	copyAction.Name = "copy"
	actions = append(actions, conversationTranscriptActionView{Name: "copy", Label: m.text(MsgTranscriptCopy, nil), Shortcut: "c", Data: copyAction})
	if len(p.ArtifactIDs) > 0 {
		open := base
		open.Name = "open"
		actions = append(actions, conversationTranscriptActionView{Name: "open", Label: m.text(MsgTranscriptOpen, nil), Shortcut: "o", Data: open})
	}
	if p.Kind == presentationTool && p.Status == statusRunning && p.TaskID != "" && p.TaskID == m.inFlightTaskID {
		cancel := base
		cancel.Name = "cancel"
		actions = append(actions, conversationTranscriptActionView{Name: "cancel", Label: m.text(MsgTranscriptCancel, nil), Shortcut: "x", Data: cancel})
	}
	return actions
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
