package tui

import (
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
