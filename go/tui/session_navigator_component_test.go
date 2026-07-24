package tui

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestNavigatorSearchFiltersWithoutLosingKeyboardOwnership(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.width, m.height = 80, 24
	m.sessionPicker = &sessionPickerState{
		items: []sessionListItem{
			{SessionID: "sess_alpha", Name: "Alpha review", Status: "paused"},
			{SessionID: "sess_beta", Name: "Beta release", Status: "paused"},
		},
		status: m.text(MsgSessionPickerHelp, nil),
	}

	if _, handled := m.dispatchNavigatorKey("/"); !handled || !m.sessionPicker.searching {
		t.Fatal("slash did not give search ownership to Navigator")
	}
	for _, key := range []string{"b", "e", "t", "a"} {
		if _, handled := m.dispatchNavigatorKey(key); !handled {
			t.Fatalf("search key %q was not consumed", key)
		}
	}
	if got := m.sessionPicker.query; got != "beta" {
		t.Fatalf("query=%q", got)
	}
	view := ansi.Strip(m.sessionPickerView())
	if !strings.Contains(view, "Beta release") || strings.Contains(view, "Alpha review") {
		t.Fatalf("search did not filter Navigator rows:\n%s", view)
	}
	if cursor := m.View().Cursor; cursor == nil {
		t.Fatal("searching Navigator did not own the physical cursor")
	}
}

func TestNavigatorHoverAndClickSelectThenActivate(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.width, m.height = 80, 24
	m.sessionPicker = &sessionPickerState{
		items: []sessionListItem{
			{SessionID: "sess_alpha", Name: "Alpha", Status: "paused"},
			{SessionID: "sess_beta", Name: "Beta", Status: "paused"},
		},
		status: m.text(MsgSessionPickerHelp, nil),
	}
	m.switchSession = func(string) error { return nil }
	m.call = &fakeCaller{handler: map[string]any{"session.resume": map[string]any{"session_id": "sess_beta", "status": "active"}}}

	frame := m.ensureNavigatorFrame()
	var x, y int
	for _, hit := range primaryFrameHits(frame.Root) {
		if strings.HasSuffix(string(hit.ID), "sess_beta") {
			x, y = hit.Bounds.X, hit.Bounds.Y
			break
		}
	}
	if x == 0 && y == 0 {
		t.Fatal("second row did not publish hit geometry")
	}
	if _, handled := m.dispatchComponentPointer(tea.MouseMotionMsg{X: x, Y: y}); !handled {
		t.Fatal("hover was not routed")
	}
	if m.sessionPicker.hoveredID != "sess_beta" || m.sessionPicker.selected != 0 {
		t.Fatalf("hover changed focus selection: hover=%q selected=%d", m.sessionPicker.hoveredID, m.sessionPicker.selected)
	}
	if cmd, handled := m.dispatchComponentPointer(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}); !handled || cmd != nil || m.sessionPicker.selected != 1 {
		t.Fatalf("first click must select only: handled=%v cmd=%v selected=%d", handled, cmd != nil, m.sessionPicker.selected)
	}
	cmd, handled := m.dispatchComponentPointer(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if !handled || cmd == nil {
		t.Fatal("second click did not activate selected row")
	}
	drain(m, cmd)
	if m.pendingSessionID != "sess_beta" {
		t.Fatalf("activated session=%q", m.pendingSessionID)
	}
}

func TestNavigatorWheelOwnsPointerContextAndRequestsAllMotion(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.width, m.height = 80, 18
	for i := 0; i < 20; i++ {
		m.sessionPicker = ensureSessionPickerItem(m.sessionPicker, sessionListItem{SessionID: "sess_" + string(rune('a'+i)), Name: "Session", Status: "paused"})
	}
	m.sessionPicker.status = m.text(MsgSessionPickerHelp, nil)
	view := m.View()
	if view.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("mouse mode=%v, want all motion while hover regions are published", view.MouseMode)
	}
	frame := m.componentFrame
	var x, y int
	for _, hit := range primaryFrameHits(frame.Root) {
		if strings.HasPrefix(string(hit.ID), "navigator-row:") {
			x, y = hit.Bounds.X, hit.Bounds.Y
			break
		}
	}
	beforeViewport := m.vp.YOffset()
	m.Update(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelDown})
	if m.sessionPicker.selected != 1 {
		t.Fatalf("wheel selected=%d", m.sessionPicker.selected)
	}
	if m.vp.YOffset() != beforeViewport {
		t.Fatal("Navigator wheel leaked to the background transcript")
	}
}

func TestNavigatorResponsiveBoundsAtQualificationSizes(t *testing.T) {
	for _, width := range []int{40, 80, 120} {
		for _, height := range []int{2, 5, 24} {
			m, _ := newTestModel(&fakeCaller{})
			m.width, m.height = width, height
			m.sessionPicker = &sessionPickerState{items: []sessionListItem{{
				SessionID: "sess_long", Name: "A very long 中文 session title that must remain bounded", Status: "paused", WorkspaceRoot: "/workspace/repository/worktree",
			}}, status: m.text(MsgSessionPickerHelp, nil)}
			m.layout()
			content := m.View().Content
			lines := strings.Split(content, "\n")
			if len(lines) > height {
				t.Fatalf("%dx%d rendered %d lines", width, height, len(lines))
			}
			for line, value := range lines {
				if cells := ansi.StringWidth(value); cells > width {
					t.Fatalf("%dx%d line %d width=%d: %q", width, height, line, cells, value)
				}
			}
			m.Close()
		}
	}
}

func TestNavigator1000RowsMeetsInteractionLatencyGates(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	defer m.Close()
	m.width, m.height = 120, 36
	items := make([]sessionListItem, 1000)
	for i := range items {
		items[i] = sessionListItem{
			SessionID: fmt.Sprintf("sess_%04d", i), Name: fmt.Sprintf("Workspace session %04d", i),
			Status: "paused", WorkspaceRoot: m.workspaceRoot,
		}
	}
	m.sessionPicker = &sessionPickerState{
		generation: 1, loading: true, scope: sessionScopeCurrent, stage: sessionStageSessions,
		status: m.text(MsgSessionPickerLoading, nil),
	}
	started := time.Now()
	m.handleSessionList(sessionListMsg{generation: 1, items: items})
	m.ensureNavigatorFrame()
	firstFrame := time.Since(started)
	if firstFrame >= 50*time.Millisecond {
		t.Fatalf("first 1000-row result frame took %s, want <50ms", firstFrame)
	}

	durations := make([]time.Duration, 200)
	for i := range durations {
		m.sessionPicker.selected = i % len(m.sessionPicker.items)
		started = time.Now()
		m.ensureNavigatorFrame()
		durations[i] = time.Since(started)
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[(len(durations)*95+99)/100-1]
	if p95 >= 16*time.Millisecond {
		t.Fatalf("1000-row Navigator input/render p95=%s, want <16ms", p95)
	}
	t.Logf("1000-row Navigator first-frame=%s p95=%s", firstFrame, p95)
}

func ensureSessionPickerItem(state *sessionPickerState, item sessionListItem) *sessionPickerState {
	if state == nil {
		state = &sessionPickerState{}
	}
	state.items = append(state.items, item)
	return state
}
