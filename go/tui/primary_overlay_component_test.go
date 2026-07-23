package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

func TestPrimaryOverlaysAreRuntimeOwnedAndPublishHitGeometry(t *testing.T) {
	tests := []struct {
		name string
		open func(*Model)
	}{
		{"plan", func(m *Model) { m.planReview = &planReviewState{Body: []string{"one", "two"}, MarkStart: -1} }},
		{"checkpoint", func(m *Model) {
			m.checkpointPicker = &checkpointPickerState{items: []checkpointInfo{{CheckpointID: "cp_1", Turn: 1, Summary: "first"}}}
		}},
		{"model", func(m *Model) {
			m.modelPicker = &modelPickerState{items: []modelPickerItem{{ID: "openai/test", Name: "Test"}}, status: m.text(MsgModelPickerHelp, nil)}
		}},
		{"keymap", func(m *Model) { m.keymapEditor = &keymapEditorState{bindings: m.keys.BindingDescriptors()} }},
		{"settings", func(m *Model) { m.settings = &settingsShellState{} }},
		{"help", func(m *Model) { m.helpOpen = true }},
		{"transcript", func(m *Model) { m.transcriptPager = &transcriptPagerState{text: "one\ntwo\nthree"} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newTestModel(nil)
			m.width, m.height = 100, 30
			tc.open(m)
			m.layout()

			top, ok := m.componentRuntime.Overlays.Top()
			if !ok || top.ID != primaryOverlayID || top.Root != primaryOverlayID || !top.Modal {
				t.Fatalf("overlay owner = %#v, %v", top, ok)
			}
			if m.componentFrame.Root.ID != primaryOverlayID {
				t.Fatalf("frame root = %q", m.componentFrame.Root.ID)
			}
			if len(m.componentFrame.Root.Hit) < 2 {
				t.Fatalf("surface published no actionable geometry: %#v", m.componentFrame.Root.Hit)
			}
			if !m.componentFrame.AllMotion {
				t.Fatal("hover-capable overlay did not request all-motion mouse reporting")
			}
		})
	}
}

func TestPrimaryOverlayMouseAndKeyboardUseExistingDomainActions(t *testing.T) {
	t.Run("settings tab and action", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.width, m.height = 100, 30
		m.settings = &settingsShellState{}
		m.layout()
		tab := primaryHit(t, m.componentFrame, "settings-tab", settingsTabModel)
		m.Update(tea.MouseMotionMsg{X: tab.Bounds.X, Y: tab.Bounds.Y})
		if m.settings.tab != settingsTabOverview {
			t.Fatal("hover moved keyboard selection")
		}
		if len(m.componentFrame.Root.Children) != 1 || m.componentFrame.Root.Children[0].Role != ui.RoleHovered {
			t.Fatalf("hover produced no visible component state: %#v", m.componentFrame.Root.Children)
		}
		m.Update(tea.MouseClickMsg{X: tab.Bounds.X, Y: tab.Bounds.Y, Button: tea.MouseLeft})
		if m.settings == nil || m.settings.tab != settingsTabModel {
			t.Fatalf("settings tab = %#v", m.settings)
		}
	})

	t.Run("model row activates", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.width, m.height = 100, 30
		m.modelPicker = &modelPickerState{items: []modelPickerItem{
			{ID: "openai/first"}, {ID: "openai/second"},
		}, status: m.text(MsgModelPickerHelp, nil)}
		m.layout()
		row := primaryHit(t, m.componentFrame, "model-row", 1)
		m.Update(tea.MouseClickMsg{X: row.Bounds.X, Y: row.Bounds.Y, Button: tea.MouseLeft})
		if m.model != "openai/second" || m.modelPicker != nil {
			t.Fatalf("model=%q picker=%#v", m.model, m.modelPicker)
		}
	})

	t.Run("help keyboard closes through overlay", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.width, m.height = 80, 20
		m.showHelp()
		if _, handled := m.handleKey(m.firstBoundKey(KeyContextPager, ActionPagerClose, "esc")); !handled {
			t.Fatal("help close was not handled")
		}
		if m.helpOpen {
			t.Fatal("help remained open")
		}
		if top, ok := m.componentRuntime.Overlays.Top(); ok && top.ID == primaryOverlayID {
			t.Fatal("closed help retained OverlayStack ownership")
		}
	})

	t.Run("help wheel owns background", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.width, m.height = 80, 8
		m.showHelp()
		before := m.helpScroll
		m.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
		if m.helpScroll <= before {
			t.Fatalf("help scroll = %d, want > %d", m.helpScroll, before)
		}
	})

	t.Run("transcript footer closes", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.width, m.height = 80, 20
		m.transcriptPager = &transcriptPagerState{text: "one\ntwo"}
		m.layout()
		closeHit := primaryHitByID(t, m.componentFrame, "transcript-close")
		m.Update(tea.MouseClickMsg{X: closeHit.Bounds.X, Y: closeHit.Bounds.Y, Button: tea.MouseLeft})
		if m.transcriptPager != nil {
			t.Fatal("transcript pager remained open")
		}
	})
}

func TestPrimaryOverlayActionGeometrySurvivesNarrowRendering(t *testing.T) {
	t.Run("model footer", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.width, m.height = 40, 10
		m.modelPicker = &modelPickerState{
			items:  []modelPickerItem{{ID: "openai/test"}},
			status: m.text(MsgModelPickerHelp, nil),
		}
		m.layout()
		primaryHitByID(t, m.componentFrame, "model-close")
	})

	t.Run("transcript footer", func(t *testing.T) {
		m, _ := newTestModel(nil)
		m.width, m.height = 40, 8
		m.transcriptPager = &transcriptPagerState{text: "one\ntwo\nthree"}
		m.layout()
		primaryHitByID(t, m.componentFrame, "transcript-close")
	})
}

func TestNestedOverlaysRestoreComposerFocusAndCaret(t *testing.T) {
	m, _ := newTestModel(nil)
	m.width, m.height = 80, 20
	m.input.SetValue("a你b")
	m.input.SetCursorColumn(2)
	line, column := m.input.Line(), m.input.Column()

	composer := &ui.TextSurface{Base: ui.Base{ComponentID: "conversation-composer"}, Content: ">"}
	m.componentRuntime.Mount(composer)
	m.componentRuntime.Focus.SetOrder([]ui.ComponentID{composer.ID()})
	m.componentRuntime.SetFocus(composer.ID(), ui.FocusProgrammatic)

	m.showHelp()
	if top, ok := m.componentRuntime.Overlays.Top(); !ok || top.PreviousFocus != composer.ID() {
		t.Fatalf("help previous focus = %#v, %v", top, ok)
	}
	m.question = &questionState{
		QuestionID: "q-focus", Prompt: "Choose", Selected: 0, Hovered: -1,
		Options: []questionOption{{Label: "one", Value: "1"}},
	}
	m.layout()
	if top, ok := m.componentRuntime.Overlays.Top(); !ok || top.ID != governanceOverlayID || top.PreviousFocus != primaryOverlayID {
		t.Fatalf("governance previous focus = %#v, %v", top, ok)
	}

	m.question = nil
	m.layout()
	if got := m.componentRuntime.Focus.Current(); got != primaryOverlayID {
		t.Fatalf("focus after governance close = %q", got)
	}
	m.closeHelp()
	if got := m.componentRuntime.Focus.Current(); got != composer.ID() {
		t.Fatalf("focus after help close = %q", got)
	}
	if m.input.Line() != line || m.input.Column() != column {
		t.Fatalf("caret moved from %d:%d to %d:%d", line, column, m.input.Line(), m.input.Column())
	}
}

func primaryHit(t *testing.T, frame ui.Frame, action string, data any) ui.HitRegion {
	t.Helper()
	for _, hit := range frame.Root.Hit {
		if hit.Action == action && hit.Data == data {
			return hit
		}
	}
	t.Fatalf("missing hit action=%q data=%v in %#v", action, data, frame.Root.Hit)
	return ui.HitRegion{}
}

func primaryHitByID(t *testing.T, frame ui.Frame, id ui.HitID) ui.HitRegion {
	t.Helper()
	for _, hit := range frame.Root.Hit {
		if hit.ID == id {
			return hit
		}
	}
	t.Fatalf("missing hit id=%q in %#v", id, frame.Root.Hit)
	return ui.HitRegion{}
}
