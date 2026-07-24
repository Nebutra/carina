package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

func TestTranslateTeaPointerCarriesPublishedGeneration(t *testing.T) {
	event, ok := translateTeaPointer(tea.MouseWheelMsg{X: 7, Y: 9, Button: tea.MouseWheelUp}, 42)
	if !ok {
		t.Fatal("wheel event was not translated")
	}
	if event.Kind != ui.EventPointer || event.FrameGeneration != 42 || event.Pointer.Kind != ui.PointerWheel || event.Pointer.WheelDelta != -1 {
		t.Fatalf("translated event = %#v", event)
	}
	if _, ok := translateTeaPointer(tea.MouseClickMsg{Button: tea.MouseRight}, 42); ok {
		t.Fatal("secondary click must not activate a component action")
	}
}

func TestComponentDispatchRoutesKeysThroughModalRuntime(t *testing.T) {
	m, _ := newTestModel(nil)
	m.width, m.height = 80, 24
	m.showHelp()
	key := m.firstBoundKey(KeyContextPager, ActionPagerClose, "esc")
	if _, handled := m.dispatchComponentKey(tea.KeyPressMsg{Text: key}); !handled {
		t.Fatal("modal key was not handled")
	}
	if m.helpOpen {
		t.Fatal("modal key did not reach the help domain reducer")
	}
}

func TestComponentDispatchRoutesPasteToFocusedNavigator(t *testing.T) {
	m, _ := newTestModel(nil)
	m.width, m.height = 80, 24
	m.sessionPicker = &sessionPickerState{searching: true, status: m.text(MsgSessionPickerHelp, nil)}
	m.layout()
	if _, handled := m.dispatchComponentPaste("跨项目"); !handled {
		t.Fatal("navigator paste was not handled")
	}
	if got := m.sessionPicker.query; got != "跨项目" {
		t.Fatalf("navigator query = %q", got)
	}
}

func TestRetainedTranscriptCellPublishesFoldGeometry(t *testing.T) {
	m, _ := newTestModel(nil)
	m.width, m.height = 80, 24
	m.tr.pushPresentation(eventPresentation{
		Key: "tool:call_1", Kind: presentationTool, Title: "tool",
		Body: []string{"detail"}, Collapsible: true, Collapsed: true,
	}, m.th, m.transcriptWidth())
	m.layout()
	hit, ok := findNodeHit(m.componentFrame.Root, "transcript-toggle")
	if !ok {
		t.Fatal("retained transcript cell did not publish fold geometry")
	}
	if _, handled := m.dispatchComponentPointer(tea.MouseClickMsg{X: hit.Bounds.X, Y: hit.Bounds.Y, Button: tea.MouseLeft}); !handled {
		t.Fatal("transcript fold click was not handled")
	}
	entry := m.tr.entries[m.tr.indexOf("tool:call_1")]
	if entry.presentation == nil || entry.presentation.Collapsed {
		t.Fatal("transcript fold action did not update the keyed entry")
	}
}

func TestTranscriptTimelineDelegatesContentToSemanticCells(t *testing.T) {
	m, _ := newTestModel(nil)
	m.width, m.height = 80, 24
	m.tr.pushPresentation(eventPresentation{
		Key: "result:task_1", Kind: presentationAgent, Status: statusSuccess,
		Title: "answer", Body: []string{"component-owned content"},
	}, m.th, m.transcriptWidth())
	m.layout()

	timeline, ok := findNodeByID(m.componentFrame.Root, conversationTranscriptID)
	if !ok || timeline.Content != "" {
		t.Fatalf("timeline content ownership = found:%v content:%q", ok, timeline.Content)
	}
	cell, ok := findNodeByID(m.componentFrame.Root, "transcript-cell:result:task_1")
	if !ok || !strings.Contains(cell.Content, "component-owned content") || cell.Role != ui.RoleSuccess {
		t.Fatalf("semantic cell = %#v, found=%v", cell, ok)
	}
}

func TestToolLifecycleRetainsOneInteractiveCell(t *testing.T) {
	m, _ := newTestModel(nil)
	m.width, m.height = 100, 28
	m.inFlightTaskID = "task_1"
	started := map[string]any{
		"type": "ToolCallStarted", "task_id": "task_1",
		"payload": map[string]any{"call_id": "call_1", "tool": "patch", "status": "running"},
	}
	m.handleEvent(started)
	m.layout()
	componentID := ui.ComponentID("transcript-cell:tool:call_1")
	retained := m.conversationScreen.transcript.cells[componentID]
	if retained == nil || !hasTranscriptAction(retained.actions, "cancel") {
		t.Fatalf("running tool cell = %#v", retained)
	}

	m.handleEvent(map[string]any{
		"type": "ToolCallCompleted", "task_id": "task_1",
		"payload": map[string]any{"call_id": "call_1", "tool": "patch", "status": "completed", "duration_ms": float64(5)},
	})
	m.layout()
	if got := m.conversationScreen.transcript.cells[componentID]; got != retained {
		t.Fatal("tool lifecycle replaced the retained component instance")
	}
	if hasTranscriptAction(retained.actions, "cancel") {
		t.Fatalf("terminal tool still advertises cancel: %#v", retained.actions)
	}
	node, ok := findNodeByID(m.componentFrame.Root, componentID)
	if !ok || node.Role != ui.RoleSuccess || !strings.Contains(node.Content, "completed") {
		t.Fatalf("completed tool node = %#v, found=%v", node, ok)
	}

	// A late running update is rejected by the keyed live-tool reducer.
	m.handleEvent(started)
	m.layout()
	node, _ = findNodeByID(m.componentFrame.Root, componentID)
	if node.Role != ui.RoleSuccess || !strings.Contains(node.Content, "completed") {
		t.Fatalf("late lifecycle regression changed terminal cell: %#v", node)
	}
}

func TestTranscriptHoverAndKeyboardMouseActionsSharePayload(t *testing.T) {
	m, _ := newTestModel(nil)
	m.width, m.height = 90, 24
	m.tr.pushPresentation(eventPresentation{
		Key: "tool:call_1", Kind: presentationTool, Status: statusRunning,
		TaskID: "task_1", CallID: "call_1", Title: "patch running",
	}, m.th, m.transcriptWidth())
	m.inFlightTaskID = "task_1"
	m.layout()

	hover, ok := findNodeHit(m.componentFrame.Root, "transcript-focus")
	if !ok {
		t.Fatal("semantic transcript cell has no hover geometry")
	}
	if _, handled := m.dispatchComponentPointer(tea.MouseMotionMsg{X: hover.Bounds.X, Y: hover.Bounds.Y}); !handled {
		t.Fatal("transcript hover was not routed")
	}
	node, _ := findNodeByID(m.componentFrame.Root, "transcript-cell:tool:call_1")
	if !node.Hovered || !strings.Contains(node.Content, "[i inspect]") {
		t.Fatalf("hover state is not visible: %#v", node)
	}

	cell := m.conversationScreen.transcript.cells["transcript-cell:tool:call_1"]
	keyboard := cell.Handle(ui.Event{Kind: ui.EventKey, Key: "i"})
	inspectAction := transcriptComponentAction{}
	for _, action := range cell.actions {
		if action.Data.Name == "inspect" {
			inspectAction = action.Data
			break
		}
	}
	inspectHit := ui.HitRegion{Action: "transcript-action", Data: inspectAction}
	pointer := cell.Handle(ui.Event{Kind: ui.EventPointer, Pointer: ui.PointerEvent{Kind: ui.PointerClick, Hit: &inspectHit}})
	if len(keyboard.Actions) != 1 || len(pointer.Actions) != 1 || !reflect.DeepEqual(keyboard.Actions[0].Data, pointer.Actions[0].Data) {
		t.Fatalf("keyboard/mouse action mismatch: key=%#v pointer=%#v", keyboard, pointer)
	}
	if result := cell.Handle(ui.Event{Kind: ui.EventKey, Key: "ctrl+c"}); result.Handled || len(result.Actions) > 0 {
		t.Fatalf("transcript cell swallowed global key: %#v", result)
	}
}

func TestTranscriptInspectPointerDispatchOpensDetailOverlay(t *testing.T) {
	m, _ := newTestModel(nil)
	m.width, m.height = 90, 24
	m.tr.pushPresentation(eventPresentation{
		Key: "governance:task_1", Kind: presentationGovernance, Status: statusSuccess,
		Title: "answer", Body: []string{"inspect this cell"},
	}, m.th, m.transcriptWidth())
	m.layout()

	hit, ok := findTranscriptActionHit(m.componentFrame.Root, "inspect")
	if !ok {
		t.Fatal("inspect action has no published geometry")
	}
	if _, handled := m.dispatchComponentPointer(tea.MouseClickMsg{X: hit.Bounds.X + hit.Bounds.Width/2, Y: hit.Bounds.Y, Button: tea.MouseLeft}); !handled {
		t.Fatal("inspect pointer action was not handled")
	}
	if m.transcriptPager == nil || !strings.Contains(m.transcriptPager.text, "inspect this cell") {
		t.Fatalf("inspect did not open selected cell: %#v", m.transcriptPager)
	}
}

func hasTranscriptAction(actions []conversationTranscriptActionView, name string) bool {
	for _, action := range actions {
		if action.Data.Name == name {
			return true
		}
	}
	return false
}

func findNodeHit(node ui.Node, action string) (ui.HitRegion, bool) {
	for _, hit := range node.Hit {
		if hit.Action == action {
			return hit, true
		}
	}
	for _, child := range node.Children {
		if hit, ok := findNodeHit(child, action); ok {
			return hit, true
		}
	}
	return ui.HitRegion{}, false
}

func findNodeByID(node ui.Node, id ui.ComponentID) (ui.Node, bool) {
	if node.ID == id {
		return node, true
	}
	for _, child := range node.Children {
		if found, ok := findNodeByID(child, id); ok {
			return found, true
		}
	}
	return ui.Node{}, false
}

func findTranscriptActionHit(node ui.Node, name string) (ui.HitRegion, bool) {
	for _, hit := range node.Hit {
		action, ok := hit.Data.(transcriptComponentAction)
		if ok && action.Name == name {
			return hit, true
		}
	}
	for _, child := range node.Children {
		if hit, ok := findTranscriptActionHit(child, name); ok {
			return hit, true
		}
	}
	return ui.HitRegion{}, false
}
