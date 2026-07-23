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
	model   *Model
	kind    primaryOverlayKind
	hovered ui.HitID
}

func newPrimaryOverlayComponent(model *Model) *primaryOverlayComponent {
	return &primaryOverlayComponent{Base: ui.Base{ComponentID: primaryOverlayID}, model: model}
}

func (c *primaryOverlayComponent) Measure(constraints ui.Constraints) ui.Size {
	return constraints.Constrain(ui.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight})
}

func (c *primaryOverlayComponent) Render(ui.RenderContext) ui.Node {
	kind := c.model.activePrimaryOverlayKind()
	if kind == primaryOverlayNone || c.Bounds.Empty() {
		return ui.Node{ID: primaryOverlayID}
	}
	c.kind = kind
	content := c.content(kind)
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
	hits := []ui.HitRegion{{
		ID: "primary-overlay-surface", Owner: primaryOverlayID, Bounds: c.Bounds,
		Kind: ui.HitHover, Action: "surface",
	}}
	hits = append(hits, c.hits(kind, content, box)...)
	var children []ui.Node
	if hover := c.hoverNode(content, box, hits); hover != nil {
		children = append(children, *hover)
	}
	return ui.Node{
		ID: primaryOverlayID, Bounds: box, Z: 20, Content: content,
		Focusable: true, Focused: c.Focused(), Hovered: c.hovered != "", Hit: hits, Children: children,
	}
}

func (c *primaryOverlayComponent) hoverNode(content string, box ui.Rect, hits []ui.HitRegion) *ui.Node {
	if c.hovered == "" {
		return nil
	}
	for _, hit := range hits {
		if hit.ID != c.hovered || hit.Action == "surface" {
			continue
		}
		lineIndex := hit.Bounds.Y - box.Y
		lines := strings.Split(ansi.Strip(content), "\n")
		if lineIndex < 0 || lineIndex >= len(lines) {
			return nil
		}
		line := fitRenderedLine(lines[lineIndex], box.Width)
		return &ui.Node{
			ID:     ui.ComponentID(string(primaryOverlayID) + ":hover"),
			Bounds: ui.Rect{X: box.X, Y: hit.Bounds.Y, Width: box.Width, Height: 1},
			Z:      1, Content: c.model.th.Style(theme.RoleTitle).Render(line),
			Role: ui.RoleHovered, Hovered: true,
		}
	}
	return nil
}

func (c *primaryOverlayComponent) content(kind primaryOverlayKind) string {
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

func (c *primaryOverlayComponent) hits(kind primaryOverlayKind, content string, box ui.Rect) []ui.HitRegion {
	switch kind {
	case primaryOverlayPlan:
		return c.planHits(content, box)
	case primaryOverlayCheckpoint:
		return c.checkpointHits(content, box)
	case primaryOverlayModel:
		return c.modelHits(content, box)
	case primaryOverlayKeymap:
		return c.keymapHits(content, box)
	case primaryOverlaySettings:
		return c.settingsHits(content, box)
	case primaryOverlayHelp:
		footer := c.model.helpFooterText()
		return segmentedLineHits(content, box, footer, []surfaceHitAction{{ID: "help-close", Key: c.model.firstBoundKey(KeyContextPager, ActionPagerClose, "esc")}})
	case primaryOverlayTranscript:
		footer := c.model.transcriptPagerFooterText()
		return segmentedLineHits(content, box, footer, []surfaceHitAction{{ID: "transcript-close", Key: c.model.firstBoundKey(KeyContextPager, ActionPagerClose, "esc")}})
	default:
		return nil
	}
}

func (c *primaryOverlayComponent) planHits(content string, box ui.Rect) []ui.HitRegion {
	state := c.model.planReview
	if state == nil {
		return nil
	}
	var hits []ui.HitRegion
	end := minInt(state.Scroll+c.model.planReviewViewportHeight(), len(state.Body))
	for index := state.Scroll; index < end; index++ {
		needle := fmt.Sprintf("%4d ", index+1)
		hits = append(hits, lineHits(content, box, needle, "plan-row", index)...)
	}
	if state.Busy || state.CommentMode {
		return hits
	}
	footer := c.model.text(MsgPlanReviewFooter, nil)
	return append(hits, segmentedLineHits(content, box, footer, []surfaceHitAction{
		{ID: "plan-approve", Key: "a"}, {ID: "plan-revise", Key: "s"},
		{ID: "plan-comment", Key: "c"}, {ID: "plan-mark", Key: "m"},
		{ID: "plan-quit", Key: "q"}, {ID: "plan-close", Key: "esc"},
	})...)
}

func (c *primaryOverlayComponent) checkpointHits(content string, box ui.Rect) []ui.HitRegion {
	state := c.model.checkpointPicker
	if state == nil || state.loading || state.restoring || state.resuming {
		return nil
	}
	if state.restored != nil {
		footer := c.model.text(MsgCheckpointResumeActions, MessageArgs{
			"resume": primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointRestored, ActionCheckpointRestoredResume)),
			"close":  primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointRestored, ActionCheckpointRestoredClose)),
		})
		if state.resumeError != "" {
			footer = c.model.text(MsgCheckpointRetryResumeActions, MessageArgs{
				"resume": primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointRestored, ActionCheckpointRestoredResume)),
				"close":  primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointRestored, ActionCheckpointRestoredClose)),
			})
		}
		return segmentedLineHits(content, box, footer, []surfaceHitAction{
			{ID: "checkpoint-resume", Key: c.model.firstBoundKey(KeyContextCheckpointRestored, ActionCheckpointRestoredResume, "enter")},
			{ID: "checkpoint-close", Key: c.model.firstBoundKey(KeyContextCheckpointRestored, ActionCheckpointRestoredClose, "esc")},
		})
	}
	if state.preview != nil {
		if state.restoreError != "" {
			footer := c.model.text(MsgCheckpointRetryRestoreActions, MessageArgs{
				"retry": primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewRetry)),
				"back":  primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewBack)),
				"close": primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewClose)),
			})
			return segmentedLineHits(content, box, footer, []surfaceHitAction{
				{ID: "checkpoint-retry", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewRetry, "enter")},
				{ID: "checkpoint-back", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewBack, "esc")},
				{ID: "checkpoint-close", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewClose, "esc")},
			})
		}
		footer := c.model.text(MsgCheckpointRestoreActions, MessageArgs{
			"arm":     primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewArm)),
			"confirm": primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewConfirm)),
			"back":    primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewBack)),
			"close":   primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointPreview, ActionCheckpointPreviewClose)),
		})
		return segmentedLineHits(content, box, footer, []surfaceHitAction{
			{ID: "checkpoint-arm", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewArm, "r")},
			{ID: "checkpoint-confirm", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewConfirm, "enter")},
			{ID: "checkpoint-back", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewBack, "backspace")},
			{ID: "checkpoint-close", Key: c.model.firstBoundKey(KeyContextCheckpointPreview, ActionCheckpointPreviewClose, "esc")},
		})
	}
	var hits []ui.HitRegion
	end := minInt(state.scroll+c.model.checkpointPickerPageHeight(), len(state.items))
	for index := state.scroll; index < end; index++ {
		item := state.items[index]
		summary := strings.TrimSpace(item.Summary)
		if summary == "" {
			summary = c.model.text(MsgCheckpointDefaultSummary, nil)
		}
		needle := strings.TrimSpace(c.model.text(MsgCheckpointListItem, MessageArgs{"prefix": "", "turn": item.Turn, "summary": summary}))
		hits = append(hits, lineHits(content, box, needle, "checkpoint-row", index)...)
	}
	footer := c.model.text(MsgCheckpointListActions, MessageArgs{
		"preview": primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointList, ActionCheckpointListPreview)),
		"up":      primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointList, ActionCheckpointListUp)),
		"down":    primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointList, ActionCheckpointListDown)),
		"close":   primaryKeyLabel(c.model.keys.keys(KeyContextCheckpointList, ActionCheckpointListClose)),
	})
	return append(hits, segmentedLineHits(content, box, footer, []surfaceHitAction{
		{ID: "checkpoint-preview", Key: c.model.firstBoundKey(KeyContextCheckpointList, ActionCheckpointListPreview, "enter")},
		{ID: "checkpoint-close", Key: c.model.firstBoundKey(KeyContextCheckpointList, ActionCheckpointListClose, "esc")},
	})...)
}

func (c *primaryOverlayComponent) modelHits(content string, box ui.Rect) []ui.HitRegion {
	state := c.model.modelPicker
	if state == nil || state.loading {
		return nil
	}
	var hits []ui.HitRegion
	end := minInt(state.scroll+c.model.modelPickerPageHeight(), len(state.items))
	for index := state.scroll; index < end; index++ {
		item := state.items[index]
		rows := lineHits(content, box, item.ID, "model-row", index)
		hits = append(hits, rows...)
		effort := item.ReasoningEffort
		if effort == "" {
			effort = item.DefaultReasoningEffort
		}
		if effort != "" {
			hits = append(hits, textHits(content, box, "[effort: "+effort+"]", "model-effort", index)...)
		}
	}
	footer := c.model.text(MsgModelPickerHelp, nil)
	return append(hits, segmentedLineHits(content, box, footer, []surfaceHitAction{
		{ID: "model-effort", Key: "e"}, {ID: "model-close", Key: "esc"},
	})...)
}

func (c *primaryOverlayComponent) keymapHits(content string, box ui.Rect) []ui.HitRegion {
	state := c.model.keymapEditor
	if state == nil || state.pending {
		return nil
	}
	switch state.mode {
	case keymapChooseAction:
		footer := c.model.text(MsgKeymapActionFooter, MessageArgs{
			"replace": c.model.keys.label(KeyContextKeymapAction, ActionKeymapActionReplace),
			"add":     c.model.keys.label(KeyContextKeymapAction, ActionKeymapActionAdd),
			"restore": c.model.keys.label(KeyContextKeymapAction, ActionKeymapActionRestore),
			"back":    c.model.keys.label(KeyContextKeymapAction, ActionKeymapActionBack),
		})
		return segmentedLineHits(content, box, footer, []surfaceHitAction{
			{ID: "keymap-replace", Key: c.model.firstBoundKey(KeyContextKeymapAction, ActionKeymapActionReplace, "r")},
			{ID: "keymap-add", Key: c.model.firstBoundKey(KeyContextKeymapAction, ActionKeymapActionAdd, "a")},
			{ID: "keymap-restore", Key: c.model.firstBoundKey(KeyContextKeymapAction, ActionKeymapActionRestore, "d")},
			{ID: "keymap-back", Key: c.model.firstBoundKey(KeyContextKeymapAction, ActionKeymapActionBack, "esc")},
		})
	case keymapCapture:
		footer := c.model.text(MsgKeymapCaptureFooter, MessageArgs{
			"save":    c.model.keys.label(KeyContextKeymapCapture, ActionKeymapCaptureCommit),
			"cancel":  c.model.keys.label(KeyContextKeymapCapture, ActionKeymapCaptureCancel),
			"literal": keymapCaptureLiteralNext,
		})
		return segmentedLineHits(content, box, footer, []surfaceHitAction{
			{ID: "keymap-save", Key: c.model.firstBoundKey(KeyContextKeymapCapture, ActionKeymapCaptureCommit, "enter")},
			{ID: "keymap-literal", Key: keymapCaptureLiteralNext},
			{ID: "keymap-cancel", Key: c.model.firstBoundKey(KeyContextKeymapCapture, ActionKeymapCaptureCancel, "esc")},
		})
	default:
		var hits []ui.HitRegion
		end := minInt(state.scroll+c.model.keymapEditorPageHeight(), len(state.bindings))
		for index := state.scroll; index < end; index++ {
			binding := state.bindings[index]
			needle := fmt.Sprintf("%s  %s",
				strings.TrimPrefix(string(binding.Action), string(binding.Context)+"."),
				strings.Join(binding.Keys, ", "))
			hits = append(hits, lineHits(content, box, needle, "keymap-row", index)...)
		}
		footer := c.model.text(MsgKeymapBrowseFooter, MessageArgs{
			"edit":  c.model.keys.label(KeyContextKeymap, ActionKeymapEdit),
			"up":    c.model.keys.label(KeyContextKeymap, ActionKeymapUp),
			"down":  c.model.keys.label(KeyContextKeymap, ActionKeymapDown),
			"close": c.model.keys.label(KeyContextKeymap, ActionKeymapClose),
		})
		return append(hits, segmentedLineHits(content, box, footer, []surfaceHitAction{
			{ID: "keymap-edit", Key: c.model.firstBoundKey(KeyContextKeymap, ActionKeymapEdit, "enter")},
			{ID: "keymap-close", Key: c.model.firstBoundKey(KeyContextKeymap, ActionKeymapClose, "esc")},
		})...)
	}
}

func (c *primaryOverlayComponent) settingsHits(content string, box ui.Rect) []ui.HitRegion {
	if c.model.settings == nil {
		return nil
	}
	var hits []ui.HitRegion
	for index, label := range c.model.settingsTabs() {
		hits = append(hits, textHits(content, box, label, "settings-tab", settingsTab(index))...)
	}
	for index, action := range c.model.settingsActions() {
		hits = append(hits, lineHits(content, box, action.Label, "settings-row", index)...)
	}
	footer := c.model.text(MsgSettingsFooter, MessageArgs{"close": c.model.keys.label(KeyContextPager, ActionPagerClose)})
	return append(hits, segmentedLineHits(content, box, footer, []surfaceHitAction{{
		ID: "settings-close", Key: c.model.firstBoundKey(KeyContextPager, ActionPagerClose, "esc"),
	}})...)
}

type surfaceHitAction struct {
	ID  ui.HitID
	Key string
}

func segmentedLineHits(content string, box ui.Rect, needle string, actions []surfaceHitAction) []ui.HitRegion {
	if len(actions) == 0 {
		return nil
	}
	line, ok := renderedLineContaining(content, needle)
	if !ok {
		return nil
	}
	line.X += box.X
	line.Y += box.Y
	line.Width = minInt(line.Width, box.Width)
	width := maxInt(line.Width, 1)
	cell := maxInt(width/len(actions), 1)
	hits := make([]ui.HitRegion, 0, len(actions))
	for index, action := range actions {
		x := line.X + index*cell
		w := cell
		if index == len(actions)-1 {
			w = maxInt(line.X+width-x, 1)
		}
		hits = append(hits, ui.HitRegion{
			ID: action.ID, Owner: primaryOverlayID, Bounds: ui.Rect{X: x, Y: line.Y, Width: w, Height: 1},
			Z: 2, Kind: ui.HitActivate, Action: "key", Data: action.Key, Focusable: true,
		})
	}
	return hits
}

func lineHits(content string, box ui.Rect, needle, action string, data any) []ui.HitRegion {
	plainNeedle := strings.TrimSpace(ansi.Strip(needle))
	if plainNeedle == "" {
		return nil
	}
	lines := strings.Split(ansi.Strip(content), "\n")
	var hits []ui.HitRegion
	for index, line := range lines {
		if !strings.Contains(line, plainNeedle) {
			continue
		}
		hits = append(hits, ui.HitRegion{
			ID: ui.HitID(fmt.Sprintf("%s:%v", action, data)), Owner: primaryOverlayID,
			Bounds: ui.Rect{X: box.X, Y: box.Y + index, Width: minInt(maxInt(ansi.StringWidth(line), 1), box.Width), Height: 1},
			Z:      1, Kind: ui.HitActivate, Action: action, Data: data, Focusable: true,
		})
	}
	return hits
}

func textHits(content string, box ui.Rect, needle, action string, data any) []ui.HitRegion {
	needle = ansi.Strip(needle)
	if needle == "" {
		return nil
	}
	lines := strings.Split(ansi.Strip(content), "\n")
	var hits []ui.HitRegion
	for lineIndex, line := range lines {
		start := 0
		for start <= len(line) {
			relative := strings.Index(line[start:], needle)
			if relative < 0 {
				break
			}
			byteIndex := start + relative
			x := box.X + ansi.StringWidth(line[:byteIndex])
			hits = append(hits, ui.HitRegion{
				ID: ui.HitID(fmt.Sprintf("%s:%v:%d", action, data, lineIndex)), Owner: primaryOverlayID,
				Bounds: ui.Rect{X: x, Y: box.Y + lineIndex, Width: ansi.StringWidth(needle), Height: 1},
				Z:      2, Kind: ui.HitActivate, Action: action, Data: data, Focusable: true,
			})
			start = byteIndex + len(needle)
		}
	}
	return hits
}

func renderedLineContaining(content, needle string) (ui.Rect, bool) {
	needle = strings.TrimSpace(ansi.Strip(needle))
	if needle == "" {
		return ui.Rect{}, false
	}
	lines := strings.Split(ansi.Strip(content), "\n")
	for index, line := range lines {
		if strings.Contains(strings.TrimSpace(line), needle) {
			return ui.Rect{X: 0, Y: index, Width: maxInt(ansi.StringWidth(line), 1), Height: 1}, true
		}
	}
	// Narrow modal fitting truncates action rows. Match their first rendered
	// segment so the visible control retains click geometry at small widths.
	prefix := needle
	for _, separator := range []string{"  ", " · "} {
		if index := strings.Index(prefix, separator); index > 0 {
			prefix = prefix[:index]
		}
	}
	prefix = strings.TrimSpace(prefix)
	for index, line := range lines {
		if prefix != "" && strings.Contains(strings.TrimSpace(line), prefix) {
			return ui.Rect{X: 0, Y: index, Width: maxInt(ansi.StringWidth(line), 1), Height: 1}, true
		}
	}
	return ui.Rect{}, false
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

func (m *Model) helpFooterText() string {
	return m.text(MsgHelpCloseScroll, MessageArgs{
		"close": m.keys.label(KeyContextPager, ActionPagerClose),
		"up":    m.keys.label(KeyContextPager, ActionPagerUp),
		"down":  m.keys.label(KeyContextPager, ActionPagerDown),
	})
}

func (m *Model) transcriptPagerFooterText() string {
	return m.text(MsgWorkspaceTranscriptFooter, MessageArgs{
		"up":        m.keys.label(KeyContextPager, ActionPagerUp),
		"down":      m.keys.label(KeyContextPager, ActionPagerDown),
		"page_up":   m.keys.label(KeyContextPager, ActionPagerPageUp),
		"page_down": m.keys.label(KeyContextPager, ActionPagerPageDown),
		"close":     m.keys.label(KeyContextPager, ActionPagerClose),
	})
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

func (m *Model) dispatchPrimaryOverlayMouse(msg tea.MouseMsg) (tea.Cmd, bool) {
	if m.activePrimaryOverlayKind() == primaryOverlayNone {
		return nil, false
	}
	m.ensurePrimaryOverlayFrame()
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
	cmd := m.applyPrimaryOverlayResult(result)
	if result.Handled {
		m.layout()
	}
	return cmd, true
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
