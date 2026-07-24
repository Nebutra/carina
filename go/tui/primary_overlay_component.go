package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

const primaryOverlayID ui.ComponentID = "primary-overlay"

type primaryOverlayKind string

const (
	primaryOverlayNone       primaryOverlayKind = ""
	primaryOverlayPlan       primaryOverlayKind = "plan-review"
	primaryOverlayCheckpoint primaryOverlayKind = "checkpoint-picker"
	primaryOverlayModel      primaryOverlayKind = "model-picker"
	primaryOverlayKeymap     primaryOverlayKind = "keymap-editor"
	primaryOverlaySettings   primaryOverlayKind = "settings"
	primaryOverlayHelp       primaryOverlayKind = "help"
	primaryOverlayTranscript primaryOverlayKind = "transcript-pager"
)

type primaryOverlayKeyAction struct {
	Kind primaryOverlayKind
	Key  string
}

type primaryOverlayComponent struct {
	ui.Base
	model        *Model
	kind         primaryOverlayKind
	hovered      ui.HitID
	content      string
	box          ui.Rect
	controls     map[ui.ComponentID]*primaryOverlayControl
	controlOrder []ui.Component
}

func newPrimaryOverlayComponent(model *Model) *primaryOverlayComponent {
	return &primaryOverlayComponent{
		Base: ui.Base{ComponentID: primaryOverlayID}, model: model,
		controls: make(map[ui.ComponentID]*primaryOverlayControl),
	}
}

type primaryOverlayControl struct {
	ui.Base
	parent    *primaryOverlayComponent
	hit       ui.HitRegion
	rowBounds ui.Rect
	rowText   string
}

func (c *primaryOverlayControl) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: c.rowBounds.Width, Height: 1})
}

func (c *primaryOverlayControl) Layout(ui.Rect) {}

func (c *primaryOverlayControl) Render(ui.RenderContext) ui.Node {
	hovered := c.parent.hovered == c.hit.ID
	content := ""
	if hovered {
		content = c.parent.model.th.Style(theme.RoleTitle).Render(fitRenderedLine(c.rowText, c.rowBounds.Width))
	}
	hit := c.hit
	hit.Owner = c.ComponentID
	return ui.Node{
		ID: c.ComponentID, Bounds: c.rowBounds, Content: content,
		Role:      map[bool]ui.SemanticRole{true: ui.RoleHovered, false: ui.RoleText}[hovered],
		Focusable: true, Focused: c.Focused(), Hovered: hovered, Hit: []ui.HitRegion{hit},
	}
}

func (c *primaryOverlayControl) Handle(event ui.Event) ui.Result {
	if event.Kind == ui.EventKey && (event.Key == "enter" || event.Key == " ") {
		return c.parent.activate(c.parent.kind, c.hit)
	}
	if event.Kind != ui.EventPointer {
		return ui.Result{}
	}
	if event.Pointer.Kind == ui.PointerLeave {
		c.parent.hovered = ""
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind == ui.PointerMove {
		c.parent.hovered = c.hit.ID
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind == ui.PointerClick {
		return c.parent.activate(c.parent.kind, c.hit)
	}
	return ui.Result{Handled: true}
}

func (c *primaryOverlayComponent) Components() []ui.Component { return c.controlOrder }

func (c *primaryOverlayComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (c *primaryOverlayComponent) Layout(bounds ui.Rect) {
	c.Bounds = bounds
	c.syncSurface()
}

func (c *primaryOverlayComponent) Render(ui.RenderContext) ui.Node {
	c.syncSurface()
	kind := c.kind
	if kind == primaryOverlayNone || c.Bounds.Empty() {
		return ui.Node{ID: primaryOverlayID}
	}
	content, box := c.content, c.box
	hits := []ui.HitRegion{{
		ID: "primary-overlay-surface", Owner: primaryOverlayID, Bounds: c.Bounds,
		Kind: ui.HitHover, Action: "surface",
	}}
	children := make([]ui.Node, 0, len(c.controlOrder))
	for _, control := range c.controlOrder {
		children = append(children, control.Render(ui.RenderContext{}))
	}
	return ui.Node{
		ID: primaryOverlayID, Bounds: box, Z: 20, Content: content,
		Focusable: true, Focused: c.Focused(), Hovered: c.hovered != "", Hit: hits, Children: children,
	}
}

func (c *primaryOverlayComponent) syncSurface() {
	kind := c.model.activePrimaryOverlayKind()
	if kind == primaryOverlayNone || c.Bounds.Empty() {
		c.kind, c.content, c.box = kind, "", ui.Rect{}
		c.controlOrder = c.controlOrder[:0]
		return
	}
	content := c.contentFor(kind)
	box := c.Bounds
	if kind != primaryOverlayTranscript {
		content = fitViewBlock(content, c.Bounds.Width, c.Bounds.Height, true)
		boxWidth := minInt(lipgloss.Width(content), c.Bounds.Width)
		boxHeight := minInt(lipgloss.Height(content), c.Bounds.Height)
		box = ui.Rect{
			X:     c.Bounds.X + maxInt((c.Bounds.Width-boxWidth)/2, 0),
			Y:     c.Bounds.Y + maxInt((c.Bounds.Height-boxHeight)/2, 0),
			Width: boxWidth, Height: boxHeight,
		}
	}
	c.kind, c.content, c.box = kind, content, box
	c.syncControls(c.typedHits(kind, box), content, box)
}

func (c *primaryOverlayComponent) syncControls(hits []ui.HitRegion, content string, box ui.Rect) {
	c.controlOrder = c.controlOrder[:0]
	lines := strings.Split(ansi.Strip(content), "\n")
	for _, hit := range hits {
		id := ui.ComponentID("primary-control:" + string(hit.ID))
		control := c.controls[id]
		if control == nil {
			control = &primaryOverlayControl{Base: ui.Base{ComponentID: id}, parent: c}
			c.controls[id] = control
		}
		control.hit = hit
		control.rowBounds = ui.Rect{X: box.X, Y: hit.Bounds.Y, Width: box.Width, Height: 1}
		line := hit.Bounds.Y - box.Y
		control.rowText = ""
		if line >= 0 && line < len(lines) {
			control.rowText = lines[line]
		}
		c.controlOrder = append(c.controlOrder, control)
	}
}

func (c *primaryOverlayComponent) contentFor(kind primaryOverlayKind) string {
	switch kind {
	case primaryOverlayPlan:
		return c.model.planReviewOverlayView()
	case primaryOverlayCheckpoint:
		return c.model.checkpointPickerView()
	case primaryOverlayModel:
		return c.model.modelPickerView()
	case primaryOverlayKeymap:
		return c.model.keymapEditorView()
	case primaryOverlaySettings:
		return c.model.settingsOverlayView()
	case primaryOverlayHelp:
		return c.model.helpOverlayView()
	case primaryOverlayTranscript:
		return c.model.transcriptPagerView(c.Bounds.Width, c.Bounds.Height)
	default:
		return ""
	}
}

func (c *primaryOverlayComponent) Handle(event ui.Event) ui.Result {
	kind := c.model.activePrimaryOverlayKind()
	if kind == primaryOverlayNone || kind != c.kind {
		return ui.Result{}
	}
	if event.Kind == ui.EventKey {
		return c.keyResult(kind, event.Key)
	}
	if event.Kind == ui.EventPaste {
		return ui.Result{Handled: true}
	}
	if event.Kind != ui.EventPointer {
		return ui.Result{}
	}
	if event.Pointer.Kind == ui.PointerLeave {
		c.hovered = ""
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind == ui.PointerWheel {
		return c.wheelResult(kind, event.Pointer.WheelDelta)
	}
	if event.Pointer.Hit == nil {
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind == ui.PointerMove {
		if event.Pointer.Hit.Action == "surface" {
			c.hovered = ""
		} else {
			c.hovered = event.Pointer.Hit.ID
		}
		return ui.Result{Handled: true}
	}
	if event.Pointer.Kind != ui.PointerClick {
		return ui.Result{Handled: true}
	}
	return c.activate(kind, *event.Pointer.Hit)
}

func (c *primaryOverlayComponent) keyResult(kind primaryOverlayKind, key string) ui.Result {
	return ui.Result{Handled: true, Actions: []ui.Action{{
		Source: primaryOverlayID, Name: "key", Data: primaryOverlayKeyAction{Kind: kind, Key: key},
	}}}
}

func (c *primaryOverlayComponent) wheelResult(kind primaryOverlayKind, delta int) ui.Result {
	if delta == 0 {
		return ui.Result{Handled: true}
	}
	key := "down"
	if delta < 0 {
		key = "up"
	}
	switch kind {
	case primaryOverlayCheckpoint:
		state := c.model.checkpointPicker
		if state == nil || state.preview != nil || state.restored != nil || state.loading || state.restoring || state.resuming {
			return ui.Result{Handled: true}
		}
		if delta < 0 {
			key = c.model.firstBoundKey(KeyContextCheckpointList, ActionCheckpointListUp, key)
		} else {
			key = c.model.firstBoundKey(KeyContextCheckpointList, ActionCheckpointListDown, key)
		}
	case primaryOverlayKeymap:
		state := c.model.keymapEditor
		if state == nil || state.mode != keymapBrowse || state.pending {
			return ui.Result{Handled: true}
		}
		if delta < 0 {
			key = c.model.firstBoundKey(KeyContextKeymap, ActionKeymapUp, key)
		} else {
			key = c.model.firstBoundKey(KeyContextKeymap, ActionKeymapDown, key)
		}
	case primaryOverlaySettings, primaryOverlayHelp, primaryOverlayTranscript:
		if delta < 0 {
			key = c.model.firstBoundKey(KeyContextPager, ActionPagerUp, key)
		} else {
			key = c.model.firstBoundKey(KeyContextPager, ActionPagerDown, key)
		}
	}
	actions := make([]ui.Action, 0, 3)
	for range 3 {
		actions = append(actions, ui.Action{Source: primaryOverlayID, Name: "key", Data: primaryOverlayKeyAction{Kind: kind, Key: key}})
	}
	return ui.Result{Handled: true, Actions: actions}
}

func (c *primaryOverlayComponent) activate(kind primaryOverlayKind, hit ui.HitRegion) ui.Result {
	switch hit.Action {
	case "plan-row":
		if index, ok := hit.Data.(int); ok && c.model.planReview != nil {
			c.model.planReview.Cursor = index
			c.model.ensurePlanReviewCursorVisible()
		}
		return ui.Result{Handled: true}
	case "checkpoint-row":
		if index, ok := hit.Data.(int); ok && c.model.checkpointPicker != nil {
			c.model.checkpointPicker.selected = index
			key := c.model.firstBoundKey(KeyContextCheckpointList, ActionCheckpointListPreview, "enter")
			return c.keyResult(kind, key)
		}
	case "model-row":
		if index, ok := hit.Data.(int); ok && c.model.modelPicker != nil {
			c.model.modelPicker.selected = index
			return c.keyResult(kind, "enter")
		}
	case "model-effort":
		if index, ok := hit.Data.(int); ok && c.model.modelPicker != nil {
			c.model.modelPicker.selected = index
			return c.keyResult(kind, "e")
		}
	case "keymap-row":
		if index, ok := hit.Data.(int); ok && c.model.keymapEditor != nil {
			c.model.keymapEditor.selected = index
			key := c.model.firstBoundKey(KeyContextKeymap, ActionKeymapEdit, "enter")
			return c.keyResult(kind, key)
		}
	case "settings-tab":
		if tab, ok := hit.Data.(settingsTab); ok && c.model.settings != nil {
			c.model.settings.tab = tab
			c.model.settings.cursor = 0
		}
		return ui.Result{Handled: true}
	case "settings-row":
		if index, ok := hit.Data.(int); ok && c.model.settings != nil {
			c.model.settings.cursor = index
			return c.keyResult(kind, "enter")
		}
	case "key":
		if key, ok := hit.Data.(string); ok {
			return c.keyResult(kind, key)
		}
	}
	return ui.Result{Handled: true}
}

func (c *primaryOverlayComponent) typedHits(kind primaryOverlayKind, box ui.Rect) []ui.HitRegion {
	switch kind {
	case primaryOverlayPlan:
		return c.planHits(box)
	case primaryOverlayCheckpoint:
		return c.checkpointHits(box)
	case primaryOverlayModel:
		return c.modelHits(box)
	case primaryOverlayKeymap:
		return c.keymapHits(box)
	case primaryOverlaySettings:
		return c.settingsHits(box)
	case primaryOverlayHelp:
		return footerControlHits(innerFooterRow(box, 0), []surfaceHitAction{{ID: "help-close", Key: c.model.firstBoundKey(KeyContextPager, ActionPagerClose, "esc")}})
	case primaryOverlayTranscript:
		return footerControlHits(ui.Rect{X: box.X, Y: box.Y + maxInt(box.Height-1, 0), Width: box.Width, Height: 1}, []surfaceHitAction{{ID: "transcript-close", Key: c.model.firstBoundKey(KeyContextPager, ActionPagerClose, "esc")}})
	default:
		return nil
	}
}

func (c *primaryOverlayComponent) planHits(box ui.Rect) []ui.HitRegion {
	state := c.model.planReview
	if state == nil {
		return nil
	}
	var hits []ui.HitRegion
	end := minInt(state.Scroll+c.model.planReviewViewportHeight(), len(state.Body))
	line := 3
	for index := state.Scroll; index < end; index++ {
		hits = append(hits, rowControlHit(framedContentRow(box, line), "plan-row", index, 1))
		wrapped := ansi.Hardwrap(state.Body[index], maxInt(maxInt(c.model.width-4, 20)-6, 8), true)
		line += maxInt(len(strings.Split(wrapped, "\n")), 1)
	}
	if state.Busy || state.CommentMode {
		return hits
	}
	extra := 0
	if len(state.Comments) > 0 {
		extra++
	}
	if state.Error != "" {
		extra++
	}
	return append(hits, footerControlHits(innerFooterRow(box, extra), []surfaceHitAction{
		{ID: "plan-approve", Key: "a"}, {ID: "plan-revise", Key: "s"},
		{ID: "plan-comment", Key: "c"}, {ID: "plan-mark", Key: "m"},
		{ID: "plan-quit", Key: "q"}, {ID: "plan-close", Key: "esc"},
	})...)
}

func (c *primaryOverlayComponent) checkpointHits(box ui.Rect) []ui.HitRegion {
	state := c.model.checkpointPicker
	if state == nil || state.loading || state.restoring || state.resuming {
		return nil
	}
	footerRow := innerFooterRow(box, map[bool]int{true: 2, false: 0}[state.status != ""])
	if state.restored != nil {
		return footerControlHits(footerRow, []surfaceHitAction{
			{ID: "checkpoint-resume", Key: c.model.firstBoundKey(KeyContextCheckpointRestored, ActionCheckpointRestoredResume, "enter")},
			{ID: "checkpoint-close", Key: c.model.firstBoundKey(KeyContextCheckpointRestored, ActionCheckpointRestoredClose, "esc")},
		})
	}
	if state.preview != nil {
		if state.restoreError != "" {
			return footerControlHits(footerRow, []surfaceHitAction{
				{ID: "checkpoint-retry", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewRetry, "enter")},
				{ID: "checkpoint-back", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewBack, "esc")},
				{ID: "checkpoint-close", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewClose, "esc")},
			})
		}
		return footerControlHits(footerRow, []surfaceHitAction{
			{ID: "checkpoint-arm", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewArm, "r")},
			{ID: "checkpoint-confirm", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewConfirm, "enter")},
			{ID: "checkpoint-back", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewBack, "backspace")},
			{ID: "checkpoint-close", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewClose, "esc")},
		})
	}
	var hits []ui.HitRegion
	end := minInt(state.scroll+c.model.checkpointPickerPageHeight(), len(state.items))
	for index := state.scroll; index < end; index++ {
		hits = append(hits, rowControlHit(framedContentRow(box, 1+index-state.scroll), "checkpoint-row", index, 1))
	}
	return append(hits, footerControlHits(footerRow, []surfaceHitAction{
		{ID: "checkpoint-preview", Key: c.model.firstBoundKey(KeyContextCheckpointList, ActionCheckpointListPreview, "enter")},
		{ID: "checkpoint-close", Key: c.model.firstBoundKey(KeyContextCheckpointList, ActionCheckpointListClose, "esc")},
	})...)
}

func (c *primaryOverlayComponent) modelHits(box ui.Rect) []ui.HitRegion {
	state := c.model.modelPicker
	if state == nil || state.loading {
		return nil
	}
	var hits []ui.HitRegion
	end := minInt(state.scroll+c.model.modelPickerPageHeight(), len(state.items))
	for index := state.scroll; index < end; index++ {
		item := state.items[index]
		row := framedContentRow(box, 2+index-state.scroll)
		hits = append(hits, rowControlHit(row, "model-row", index, 1))
		effort := item.ReasoningEffort
		if effort == "" {
			effort = item.DefaultReasoningEffort
		}
		if effort != "" {
			label := "[effort: " + effort + "]"
			x := row.X + minInt(ansi.StringWidth("  "+item.ID+" "), maxInt(row.Width-1, 0))
			hits = append(hits, ui.HitRegion{
				ID: ui.HitID("model-effort:" + item.ID), Bounds: ui.Rect{X: x, Y: row.Y, Width: minInt(ansi.StringWidth(label), maxInt(row.X+row.Width-x, 1)), Height: 1},
				Z: 2, Kind: ui.HitActivate, Action: "model-effort", Data: index, Focusable: true,
			})
		}
	}
	return append(hits, footerControlHits(innerFooterRow(box, 0), []surfaceHitAction{
		{ID: "model-effort", Key: "e"}, {ID: "model-close", Key: "esc"},
	})...)
}

func (c *primaryOverlayComponent) keymapHits(box ui.Rect) []ui.HitRegion {
	state := c.model.keymapEditor
	if state == nil || state.pending {
		return nil
	}
	switch state.mode {
	case keymapChooseAction:
		return footerControlHits(innerFooterRow(box, map[bool]int{true: 2, false: 0}[state.status != ""]), []surfaceHitAction{
			{ID: "keymap-replace", Key: c.model.firstBoundKey(KeyContextKeymapAction, ActionKeymapActionReplace, "r")},
			{ID: "keymap-add", Key: c.model.firstBoundKey(KeyContextKeymapAction, ActionKeymapActionAdd, "a")},
			{ID: "keymap-restore", Key: c.model.firstBoundKey(KeyContextKeymapAction, ActionKeymapActionRestore, "d")},
			{ID: "keymap-back", Key: c.model.firstBoundKey(KeyContextKeymapAction, ActionKeymapActionBack, "esc")},
		})
	case keymapCapture:
		return footerControlHits(innerFooterRow(box, map[bool]int{true: 2, false: 0}[state.status != ""]), []surfaceHitAction{
			{ID: "keymap-save", Key: c.model.firstBoundKey(KeyContextKeymapCapture, ActionKeymapCaptureCommit, "enter")},
			{ID: "keymap-literal", Key: keymapCaptureLiteralNext},
			{ID: "keymap-cancel", Key: c.model.firstBoundKey(KeyContextKeymapCapture, ActionKeymapCaptureCancel, "esc")},
		})
	default:
		var hits []ui.HitRegion
		end := minInt(state.scroll+c.model.keymapEditorPageHeight(), len(state.bindings))
		for index := state.scroll; index < end; index++ {
			hits = append(hits, rowControlHit(framedContentRow(box, 1+index-state.scroll), "keymap-row", index, 1))
		}
		return append(hits, footerControlHits(innerFooterRow(box, map[bool]int{true: 2, false: 0}[state.status != ""]), []surfaceHitAction{
			{ID: "keymap-edit", Key: c.model.firstBoundKey(KeyContextKeymap, ActionKeymapEdit, "enter")},
			{ID: "keymap-close", Key: c.model.firstBoundKey(KeyContextKeymap, ActionKeymapClose, "esc")},
		})...)
	}
}

func (c *primaryOverlayComponent) settingsHits(box ui.Rect) []ui.HitRegion {
	if c.model.settings == nil {
		return nil
	}
	var hits []ui.HitRegion
	tabRow := framedContentRow(box, 1)
	x := tabRow.X
	for index, label := range c.model.settingsTabs() {
		width := ansi.StringWidth(label) + 2
		if settingsTab(index) != c.model.settings.tab {
			width = ansi.StringWidth(label)
		}
		width = minInt(width, maxInt(tabRow.X+tabRow.Width-x, 1))
		hits = append(hits, ui.HitRegion{
			ID: ui.HitID("settings-tab:" + string(rune('0'+index))), Bounds: ui.Rect{X: x, Y: tabRow.Y, Width: width, Height: 1},
			Z: 2, Kind: ui.HitActivate, Action: "settings-tab", Data: settingsTab(index), Focusable: true,
		})
		x += width + 2
	}
	for index, action := range c.model.settingsActions() {
		_ = action
		hits = append(hits, rowControlHit(framedContentRow(box, 12+index), "settings-row", index, 1))
	}
	return append(hits, footerControlHits(innerFooterRow(box, 0), []surfaceHitAction{{
		ID: "settings-close", Key: c.model.firstBoundKey(KeyContextPager, ActionPagerClose, "esc"),
	}})...)
}

type surfaceHitAction struct {
	ID  ui.HitID
	Key string
}

func footerControlHits(row ui.Rect, actions []surfaceHitAction) []ui.HitRegion {
	if len(actions) == 0 {
		return nil
	}
	if row.Empty() {
		return nil
	}
	width := maxInt(row.Width, 1)
	cell := maxInt(width/len(actions), 1)
	hits := make([]ui.HitRegion, 0, len(actions))
	for index, action := range actions {
		x := row.X + index*cell
		w := cell
		if index == len(actions)-1 {
			w = maxInt(row.X+width-x, 1)
		}
		hits = append(hits, ui.HitRegion{
			ID: action.ID, Bounds: ui.Rect{X: x, Y: row.Y, Width: w, Height: 1},
			Z: 2, Kind: ui.HitActivate, Action: "key", Data: action.Key, Focusable: true,
		})
	}
	return hits
}

func rowControlHit(row ui.Rect, action string, data any, z int) ui.HitRegion {
	return ui.HitRegion{
		ID: ui.HitID(action + ":" + fmt.Sprint(data)), Bounds: row,
		Z: z, Kind: ui.HitActivate, Action: action, Data: data, Focusable: true,
	}
}

func framedContentRow(box ui.Rect, line int) ui.Rect {
	return ui.Rect{
		X: box.X + minInt(2, maxInt(box.Width-1, 0)), Y: box.Y + 1 + line,
		Width: maxInt(box.Width-4, 1), Height: 1,
	}.Intersect(box)
}

func innerFooterRow(box ui.Rect, linesAfter int) ui.Rect {
	return ui.Rect{
		X:     box.X + minInt(2, maxInt(box.Width-1, 0)),
		Y:     box.Y + maxInt(box.Height-2-linesAfter, 0),
		Width: maxInt(box.Width-4, 1), Height: 1,
	}.Intersect(box)
}

func (m *Model) activePrimaryOverlayKind() primaryOverlayKind {
	switch {
	case m.planReview != nil:
		return primaryOverlayPlan
	case m.checkpointPicker != nil:
		return primaryOverlayCheckpoint
	case m.modelPicker != nil:
		return primaryOverlayModel
	case m.sessionPicker != nil:
		return primaryOverlayNone
	case m.keymapEditor != nil:
		return primaryOverlayKeymap
	case m.settings != nil:
		return primaryOverlaySettings
	case m.helpOpen:
		return primaryOverlayHelp
	case m.transcriptPager != nil && m.transcriptPager.operationalKind == "":
		return primaryOverlayTranscript
	default:
		return primaryOverlayNone
	}
}

func (m *Model) firstBoundKey(context KeyContext, action KeyAction, fallback string) string {
	keys := m.keys.keys(context, action)
	if len(keys) == 0 {
		return fallback
	}
	return keys[0]
}

func (m *Model) ensurePrimaryOverlayFrame() ui.Frame {
	if m.primaryOverlayComponent == nil {
		m.primaryOverlayComponent = newPrimaryOverlayComponent(m)
	}
	top, ok := m.componentRuntime.Overlays.Top()
	if !ok || top.ID != primaryOverlayID {
		// Capture the previous owner before BeginFrame replaces the focus order
		// with the overlay's retained subtree.
		m.componentRuntime.PushOverlay(primaryOverlayID, primaryOverlayID, true)
	}
	frame := m.componentRuntime.BeginFrame(m.primaryOverlayComponent, ui.Rect{Width: maxInt(m.width, 1), Height: maxInt(m.height, 1)})
	m.componentFrame = frame
	m.reconcileFrameGraphics(frame)
	return frame
}

func (m *Model) teardownPrimaryOverlayFrame() {
	if m.primaryOverlayComponent == nil {
		return
	}
	if top, ok := m.componentRuntime.Overlays.Top(); ok && top.ID == primaryOverlayID {
		m.componentRuntime.PopOverlay()
	} else {
		m.componentRuntime.Overlays.Remove(primaryOverlayID)
	}
	m.componentRuntime.Unmount(primaryOverlayID)
	m.primaryOverlayComponent = nil
}

func (m *Model) dispatchPrimaryOverlayKey(key string) (tea.Cmd, bool) {
	if m.activePrimaryOverlayKind() == primaryOverlayNone {
		return nil, false
	}
	m.ensurePrimaryOverlayFrame()
	result := m.componentRuntime.Dispatch(ui.Event{Kind: ui.EventKey, Key: key})
	return m.applyPrimaryOverlayResult(result), true
}

func (m *Model) applyPrimaryOverlayResult(result ui.Result) tea.Cmd {
	commands := make([]tea.Cmd, 0, len(result.Actions))
	for _, action := range result.Actions {
		if action.Name != "key" {
			continue
		}
		keyAction, ok := action.Data.(primaryOverlayKeyAction)
		if !ok {
			continue
		}
		cmd, _ := m.primaryOverlayDomainKey(keyAction.Kind, keyAction.Key)
		commands = append(commands, cmd)
	}
	return tea.Batch(commands...)
}

func (m *Model) primaryOverlayDomainKey(kind primaryOverlayKind, key string) (tea.Cmd, bool) {
	switch kind {
	case primaryOverlayPlan:
		if cmd, handled := m.planReviewKey(key); handled {
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key) {
			return tea.ClearScreen, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			return m.ctrlC(), true
		}
	case primaryOverlayCheckpoint:
		return m.checkpointPickerKey(key)
	case primaryOverlayModel:
		return m.modelPickerKey(key)
	case primaryOverlayKeymap:
		return m.keymapEditorKey(key)
	case primaryOverlaySettings:
		if cmd, handled := m.settingsKey(key); handled {
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key) {
			return tea.ClearScreen, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			m.closeSettings()
			return m.resumeQueuedAfterTransient(), true
		}
	case primaryOverlayHelp:
		if cmd, handled := m.helpKey(key); handled {
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key) {
			return tea.ClearScreen, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			m.closeHelp()
			return m.resumeQueuedAfterTransient(), true
		}
	case primaryOverlayTranscript:
		return m.transcriptPagerKey(key)
	}
	return nil, true
}
