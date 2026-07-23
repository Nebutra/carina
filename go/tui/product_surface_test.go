package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

func TestTypedPresentationOmitsThoughtAndRawJSON(t *testing.T) {
	th := theme.New(theme.Mono)
	ev := map[string]any{
		"type":      "ModelResponded",
		"timestamp": "2026-07-09T10:11:12Z",
		"payload": map[string]any{
			"text": `{"thought":"private hidden reasoning","tool":"run","command":["go","test","./..."]}`,
		},
	}
	presentation := presentEvent(ev, th, "en")
	presentation.Collapsed = false
	line := presentation.render(th, 120)
	for _, forbidden := range []string{"private hidden reasoning", `"thought"`, `"tool"`} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("typed presentation leaked %q: %s", forbidden, line)
		}
	}
	for _, want := range []string{"agent", "selected run", "$ go test ./..."} {
		if !strings.Contains(line, want) {
			t.Fatalf("typed presentation missing %q: %s", want, line)
		}
	}
}

func TestLastVerboseEventCanBeFoldedAndExpanded(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(EventMsg{Raw: map[string]any{
		"type": "CommandOutput",
		"payload": map[string]any{
			"stream": "stdout",
			"chunk":  "first line\nsecond line",
		},
	}})
	before := transcriptText(m)
	if !strings.Contains(before, "[+2]") || strings.Contains(before, "second line") {
		t.Fatalf("command output should start folded:\n%s", before)
	}
	if _, handled := m.handleKey("ctrl+o"); !handled {
		t.Fatal("ctrl+o did not toggle the latest fold")
	}
	after := transcriptText(m)
	if !strings.Contains(after, "[open]") || !strings.Contains(after, "second line") {
		t.Fatalf("command output did not expand:\n%s", after)
	}
}

func TestTaskGraphBuildsSubagentAndWorkflowHierarchy(t *testing.T) {
	var graph taskGraph
	graph.observeEvent(map[string]any{
		"type": "TaskCreated", "task_id": "task_main",
		"payload": map[string]any{"task_id": "task_main", "user_prompt": "build release"},
	})
	graph.observeEvent(map[string]any{
		"type": "ToolApproved", "task_id": "task_main",
		"payload": map[string]any{"spawn_agent": "reviewer", "child_session": "sess_child"},
	})
	graph.observeEvent(map[string]any{
		"type": "TaskCreated", "task_id": "task_main",
		"payload": map[string]any{"status": "workflow_started", "workflow": "release", "run_id": "wf_1"},
	})
	graph.observeEvent(map[string]any{
		"type": "ToolApproved", "task_id": "task_main",
		"payload": map[string]any{"workflow": "release", "run_id": "wf_1", "step": "test", "agent": "qa"},
	})
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.tasks = graph
	text := strings.Join(graph.lines(m, 80, 8), "\n")
	for _, want := range []string{"subagent", "reviewer", "workflow", "release", "step", "test qa", "  `-"} {
		if !strings.Contains(text, want) {
			t.Fatalf("task tree missing %q:\n%s", want, text)
		}
	}

	graph.observeEvent(map[string]any{
		"type": "ModelResponded", "task_id": "task_main",
		"payload": map[string]any{"status": "workflow_completed", "workflow": "release", "run_id": "wf_1"},
	})
	if graph.nodes["wf_1"].Status != "completed" || graph.nodes["wf_1:test"].Status != "completed" {
		t.Fatalf("workflow completion did not fold into children: %#v %#v", graph.nodes["wf_1"], graph.nodes["wf_1:test"])
	}
}

func TestNarrowCJKAndANSIViewDoesNotOverflow(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.ANSI256), Locale: "zh", Socket: "/tmp/s.sock"})
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 18})
	m.Update(EventMsg{Raw: map[string]any{
		"type":      "CommandOutput",
		"timestamp": "2026-07-09T10:11:12Z",
		"task_id":   "任务_很长的标识符",
		"payload": map[string]any{
			"stream": "stdout",
			"chunk":  "你好世界你好世界你好世界\x1b[31m红色\x1b[0m and a very long suffix",
		},
	}})
	view := m.View().Content
	if strings.Contains(ansi.Strip(view), "\x1b[31m") {
		t.Fatal("attacker ANSI survived as visible content")
	}
	for i, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 32 {
			t.Fatalf("line %d width=%d exceeds terminal width 32: %q", i, width, line)
		}
	}
}

func TestDoctorUsesDedicatedScreenWithoutTranscriptSpam(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{"daemon.doctor": map[string]any{"status": "ok", "healthy": true}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionID, m.call = "sess", caller
	m.push("conversation stays here")
	before := transcriptText(m)
	cmd := m.slashCommand("/doctor")
	if cmd == nil || m.transcriptPager == nil || m.componentRuntime.Screens.Current().ID != ui.ScreenDoctor {
		t.Fatal("doctor did not enter the dedicated screen")
	}
	m.Update(cmd())
	if got := transcriptText(m); got != before {
		t.Fatalf("doctor appended permanent transcript output:\n%s", got)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(strings.ToLower(view), "status") || !strings.Contains(view, "ok") {
		t.Fatalf("doctor screen missing result:\n%s", view)
	}
	m.closeTranscriptPager()
	if m.componentRuntime.Screens.Current().ID != ui.ScreenConversation {
		t.Fatal("closing doctor did not restore Conversation screen")
	}
}

func TestOperationalScreenRefreshAndCloseUseComponentHitGeometry(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{"session.get": map[string]any{"status": "ready"}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionID, m.call = "sess", caller
	drain(m, m.slashCommand("/status"))
	frame := m.componentFrame
	var refresh, close ui.HitRegion
	for _, hit := range frame.Root.Hit {
		switch hit.Action {
		case "refresh":
			refresh = hit
		case "close":
			close = hit
		}
	}
	if refresh.ID == "" || close.ID == "" {
		t.Fatalf("operational actions missing hit regions: %+v", frame.Root.Hit)
	}
	beforeCalls := len(caller.calls)
	_, cmd := m.Update(tea.MouseClickMsg{X: refresh.Bounds.X, Y: refresh.Bounds.Y, Button: tea.MouseLeft})
	drain(m, cmd)
	if len(caller.calls) != beforeCalls+1 || m.transcriptPager == nil || m.transcriptPager.loading {
		t.Fatalf("component refresh did not reload the operational screen: calls=%d state=%+v", len(caller.calls), m.transcriptPager)
	}
	m.Update(tea.MouseClickMsg{X: close.Bounds.X, Y: close.Bounds.Y, Button: tea.MouseLeft})
	if m.transcriptPager != nil || m.componentRuntime.Screens.Current().ID != ui.ScreenConversation {
		t.Fatal("component close did not restore Conversation")
	}
}

func TestClosedOperationalScreenDropsLateResult(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{"session.get": map[string]any{"status": "ready"}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionID, m.call = "sess", caller
	m.push("conversation remains clean")
	before := transcriptText(m)
	cmd := m.slashCommand("/status")
	m.closeTranscriptPager()
	m.Update(cmd())
	if got := transcriptText(m); got != before {
		t.Fatalf("late operational result fell back into transcript:\n%s", got)
	}
}

func TestConnectionAndSubmissionRecoveryNoticesStayOutOfTranscript(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.push("conversation remains durable")
	before := transcriptText(m)

	m.Update(ConnRestoredMsg{SessionID: "sess"})
	if got := transcriptText(m); got != before {
		t.Fatalf("connection notice polluted transcript: %q", got)
	}
	if footer := strings.ToLower(ansi.Strip(m.statusFooterView(80))); !strings.Contains(footer, "reconnected") {
		t.Fatalf("connection notice missing from transient status: %q", footer)
	}

	m.setOperationalNotice(m.text(MsgSubmissionReconciling, nil), theme.RoleInfo)
	if got := transcriptText(m); got != before {
		t.Fatalf("submission reconciliation polluted transcript: %q", got)
	}
	if footer := strings.ToLower(ansi.Strip(m.statusFooterView(80))); !strings.Contains(footer, "reconcil") {
		t.Fatalf("submission notice missing from transient status: %q", footer)
	}
}

func TestContextPressureOnlyClearsItsOwnTransientNotice(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.setOperationalNotice("reconnected", theme.RoleSuccess)
	m.runtime.ContextAvailable = true
	m.runtime.ContextLimit = 100
	m.runtime.ContextPercent = 10
	m.contextNudgeLevel = 1
	m.applyContextPressurePolicy()
	if m.operationalNotice.Text != "reconnected" {
		t.Fatalf("context reset cleared unrelated lifecycle notice: %+v", m.operationalNotice)
	}
	m.setOperationalNoticeKind("context", "context pressure", theme.RoleWarning)
	m.contextNudgeLevel = 1
	m.applyContextPressurePolicy()
	if m.operationalNotice.Text != "" {
		t.Fatalf("resolved context notice remained visible: %+v", m.operationalNotice)
	}
}

func TestFrameGraphicsRejectStaleTargetGeneration(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionGeneration = 4
	m.componentGraphicsOwners = map[string]struct{}{"current-owner": {}}
	m.reconcileFrameGraphics(ui.Frame{
		Generation: 9,
		Graphics: []ui.GraphicsPlacement{{
			Owner: "stale-owner", Generation: 9, TargetGeneration: 3,
		}},
	})
	if _, ok := m.componentGraphicsOwners["stale-owner"]; ok {
		t.Fatal("stale target graphics placement was accepted")
	}
}

func TestQuestionComponentHoverSelectAndSecondClickAnswers(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{"task.user.answer": map[string]any{"ok": true}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.call = caller
	m.question = &questionState{
		QuestionID: "q1", Prompt: "Choose", Selected: 0, Hovered: -1,
		Options: []questionOption{{Label: "one", Value: "1"}, {Label: "two", Value: "2"}},
	}
	m.layout()
	if m.componentRuntime.Overlays.Len() != 1 {
		t.Fatal("question did not enter OverlayStack")
	}
	var second ui.HitRegion
	for _, hit := range m.componentFrame.Root.Hit {
		if hit.Action == "question-option" && hit.Data == 1 {
			second = hit
		}
	}
	if second.ID == "" {
		t.Fatalf("question option missing hit geometry: %+v", m.componentFrame.Root.Hit)
	}
	m.Update(tea.MouseMotionMsg{X: second.Bounds.X, Y: second.Bounds.Y})
	if m.question.Hovered != 1 || m.question.Selected != 0 {
		t.Fatalf("hover moved keyboard selection: hovered=%d selected=%d", m.question.Hovered, m.question.Selected)
	}
	_, first := m.Update(tea.MouseClickMsg{X: second.Bounds.X, Y: second.Bounds.Y, Button: tea.MouseLeft})
	if first != nil || m.question.Selected != 1 || m.question.Resolving {
		t.Fatalf("first click should select only: selected=%d resolving=%v", m.question.Selected, m.question.Resolving)
	}
	_, secondCmd := m.Update(tea.MouseClickMsg{X: second.Bounds.X, Y: second.Bounds.Y, Button: tea.MouseLeft})
	if secondCmd == nil || !m.question.Resolving {
		t.Fatal("second click did not activate the selected question option")
	}
	drain(m, secondCmd)
	_ = m.View()
	if m.question != nil || m.componentRuntime.Overlays.Len() != 0 {
		t.Fatal("resolved question did not teardown its overlay ownership")
	}
}

func TestApprovalComponentClickUsesExistingDecisionPath(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{"task.action.approve": map[string]any{"scope": "once"}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionID, m.call = "sess", caller
	m.approval = &approvalState{DecisionID: "d1", Action: "command.exec", Resource: "go test ./..."}
	m.layout()
	var once ui.HitRegion
	for _, hit := range m.componentFrame.Root.Hit {
		if hit.Action == "approval-once" {
			once = hit
		}
	}
	if once.ID == "" {
		t.Fatalf("approval action missing hit geometry: %+v", m.componentFrame.Root.Hit)
	}
	m.Update(tea.MouseMotionMsg{X: once.Bounds.X, Y: once.Bounds.Y})
	if m.approval.HoveredAction != "approval-once" {
		t.Fatal("approval hover did not update component state")
	}
	_, cmd := m.Update(tea.MouseClickMsg{X: once.Bounds.X, Y: once.Bounds.Y, Button: tea.MouseLeft})
	if cmd == nil || !m.approval.Resolving || m.approval.PendingScope != "once" {
		t.Fatal("approval click bypassed the governed decision path")
	}
}
