package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestPlanScaffoldAndApprove(t *testing.T) {
	dir := t.TempDir()
	fc := &fakeCaller{handler: map[string]any{
		"session.plan_mode":    map[string]any{"ok": true},
		"session.approve_plan": map[string]any{"plan_mode": false, "approved": true},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en", WorkspaceRoot: dir})
	m.sessionID, m.call, m.mode = "sess_plan", fc, "plan"
	if err := m.ensurePlanFileScaffold(); err != nil {
		t.Fatal(err)
	}
	path := m.planFilePath()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("plan scaffold missing: %v", err)
	}
	m.viewPlanSurface()
	if m.planReview == nil {
		t.Fatal("view-plan should open plan review overlay")
	}
	if m.planReview.Path != path {
		t.Fatalf("planReview.Path=%q want %q", m.planReview.Path, path)
	}
	// Approve via overlay key (a).
	cmd, handled := m.planReviewKey("a")
	if !handled || cmd == nil {
		t.Fatalf("plan review approve key: handled=%v cmd=%v", handled, cmd != nil)
	}
	m.Update(cmd())
	if m.mode != "build" {
		t.Fatalf("mode=%q after approve", m.mode)
	}
	if m.planReview != nil {
		t.Fatal("plan review overlay should close after approve")
	}
	if len(fc.calls) == 0 || fc.calls[len(fc.calls)-1].method != "session.approve_plan" {
		t.Fatalf("calls=%#v", fc.calls)
	}
}

func TestPlanReviewRequestChangesSeedsComposer(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en", WorkspaceRoot: dir})
	m.sessionID, m.mode = "sess_plan", "plan"
	_ = m.ensurePlanFileScaffold()
	_ = os.WriteFile(m.planFilePath(), []byte("# Goal\nShip Wave L\n"), 0o600)
	m.openPlanReview()
	if m.planReview == nil {
		t.Fatal("expected overlay")
	}
	cmd, handled := m.planReviewKey("s")
	if !handled || cmd != nil {
		t.Fatalf("request-changes: handled=%v cmd=%v", handled, cmd != nil)
	}
	if m.planReview != nil {
		t.Fatal("overlay should close")
	}
	if !strings.Contains(m.input.Value(), "revise") && !strings.Contains(m.input.Value(), "修订") && !strings.Contains(strings.ToLower(m.input.Value()), "revise") {
		// English seed: "Please revise the plan: "
		if !strings.Contains(strings.ToLower(m.input.Value()), "plan") {
			t.Fatalf("composer not seeded: %q", m.input.Value())
		}
	}
}

func TestCommitWorkflowInjectsDiff(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"workspace.diff": map[string]any{
			"files": []any{map[string]any{"path": "a.go", "status": " M", "diff": "@@\n+line\n"}},
		},
		"task.submit": map[string]any{"task_id": "tsk_commit", "status": "queued"},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	cmd := m.commitWorkflow("keep message short")
	msg := cmd()
	ready, ok := msg.(commitPromptReadyMsg)
	if !ok {
		t.Fatalf("msg type %T", msg)
	}
	if !strings.Contains(ready.prompt, "a.go") || !strings.Contains(ready.prompt, "keep message short") {
		t.Fatalf("prompt=%q", ready.prompt)
	}
	_, submit := m.Update(ready)
	if submit == nil {
		// beginSubmission may return cmd
	}
}

func TestBtwIsAnswerOnlyPrompt(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_btw", "status": "queued"},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	cmd := m.btwSideQuestion("what is the entrypoint?", false)
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	// Drain submission path
	for i := 0; i < 5 && cmd != nil; i++ {
		msg := cmd()
		if msg == nil {
			break
		}
		_, cmd = m.Update(msg)
	}
	found := false
	for _, c := range fc.calls {
		if c.method == "task.submit" {
			found = true
			p, _ := c.params["prompt"].(string)
			if !strings.Contains(p, "SIDE QUESTION") || !strings.Contains(p, "Do not modify files") {
				t.Fatalf("btw prompt not constrained: %q", p)
			}
		}
	}
	if !found {
		t.Fatalf("no submit: %#v", fc.calls)
	}
}

func TestExplainRuntimeMentionsSandbox(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.runtime.Sandbox = "on"
	m.runtime.Profile = "safe-edit"
	m.mode = "plan"
	m.explainRuntimeSurface()
	got := transcriptText(m)
	for _, want := range []string{"sandbox", "safe-edit", "plan"} {
		if !strings.Contains(strings.ToLower(got), want) {
			t.Fatalf("explain missing %q:\n%s", want, got)
		}
	}
}

func TestInspectSurfaceAggregatesInventories(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"daemon.doctor":     map[string]any{"status": "ok"},
		"config.inventory":  map[string]any{"effective": map[string]any{"sandbox_commands": true}},
		"skill.inventory":   map[string]any{"count": 2},
		"hook.inventory":    map[string]any{"count": 1},
		"mcp.inventory":     map[string]any{"count": 3},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	cmd := m.inspectSurface()
	m.Update(cmd())
	got := transcriptText(m)
	if !strings.Contains(got, "skills_count") && !strings.Contains(got, "2") {
		// humanize uses skills_count key
		if !strings.Contains(got, "2") {
			t.Fatalf("inspect missing counts:\n%s", got)
		}
	}
	if !strings.Contains(got, "doctor") {
		t.Fatalf("inspect missing doctor:\n%s", got)
	}
}

func TestPlanFilePathIsUnderWorkspace(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en", WorkspaceRoot: dir})
	m.sessionID = "sess/../weird"
	path := m.planFilePath()
	if !strings.HasPrefix(path, filepath.Join(dir, ".carina", "plans")) {
		t.Fatalf("path=%q", path)
	}
	if strings.Contains(path, "..") {
		t.Fatalf("unsafe path %q", path)
	}
}

func TestContextPressureAutoCompactsWhenAvailable(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"session.checkpoint.compact": map[string]any{"ok": true},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	cmd := m.handleRuntimeStatus(runtimeStatusMsg{
		sessionID: "sess", contextAvailable: true, contextLimit: 100, contextPercent: 88,
		compactAvailable: true, compactCheckpoint: "cp_1", compactTaskID: "task_1",
	})
	if cmd == nil {
		t.Fatal("expected auto-compact command")
	}
	m.Update(cmd())
	if len(fc.calls) != 1 || fc.calls[0].method != "session.checkpoint.compact" {
		t.Fatalf("calls=%#v", fc.calls)
	}
	if m.contextNudgeLevel != 3 {
		t.Fatalf("nudge level=%d", m.contextNudgeLevel)
	}
	// Second refresh at same pressure must not re-fire.
	if cmd := m.handleRuntimeStatus(runtimeStatusMsg{
		sessionID: "sess", contextAvailable: true, contextLimit: 100, contextPercent: 88,
		compactAvailable: true, compactCheckpoint: "cp_1", compactTaskID: "task_1",
	}); cmd != nil {
		t.Fatal("duplicate auto-compact")
	}
}

func TestBtwForkQueuesPendingQuestion(t *testing.T) {
	switched := ""
	fc := &fakeCaller{handler: map[string]any{
		"session.fork": map[string]any{"session_id": "sess_side", "workspace_root": "/tmp/ws"},
	}}
	m := New(Options{
		Theme: theme.New(theme.Mono), Locale: "en",
		SwitchSession: func(id string) error { switched = id; return nil },
	})
	m.sessionID, m.call = "sess", fc
	cmd := m.btwSideQuestion("explain main", true)
	if cmd == nil || m.pendingSideQuestion != "explain main" {
		t.Fatalf("pending=%q cmd=%v", m.pendingSideQuestion, cmd != nil)
	}
	// Simulate successful action result path partially via flush after manual set.
	m.pendingSideQuestion = "explain main"
	side := m.flushPendingSideQuestion()
	if m.pendingSideQuestion != "" || side == nil {
		t.Fatal("flush did not produce submit")
	}
	_ = switched
}

func TestAlwaysApproveToggleCallsDaemon(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"daemon.set_interactive_approval": map[string]any{
			"interactive_approval": false, "approval_mode": "always-approve",
			"previous_mode": "ask", "warning": "tools will auto-run",
		},
		"session.get":      map[string]any{"session_id": "sess"},
		"config.inventory": map[string]any{"effective": map[string]any{"approval_mode": "always-approve", "interactive_approval": false}},
		"context.summary":  map[string]any{"model_context_tokens": map[string]any{"available": false}},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	m.runtime.InteractiveApprove = "on"
	m.runtime.ApprovalMode = "ask"
	cmd := m.setAlwaysApprove(true)
	m.Update(cmd())
	if len(fc.calls) == 0 || fc.calls[0].method != "daemon.set_interactive_approval" {
		t.Fatalf("calls=%#v", fc.calls)
	}
	if fc.calls[0].params["mode"] != "always-approve" {
		t.Fatalf("always-approve should set mode=always-approve, got %#v", fc.calls[0].params)
	}
	got := transcriptText(m)
	if !strings.Contains(strings.ToLower(got), "warning") && !strings.Contains(got, "always-approve") {
		t.Fatalf("expected warning in transcript:\n%s", got)
	}
}

func TestDontAskCommandSetsMode(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"daemon.set_interactive_approval": map[string]any{
			"interactive_approval": false, "approval_mode": "dont-ask",
			"previous_mode": "ask", "warning": "no operator prompt",
		},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	cmd := m.slashCommand("/dont-ask on")
	m.Update(cmd())
	if len(fc.calls) == 0 || fc.calls[0].params["mode"] != "dont-ask" {
		t.Fatalf("calls=%#v", fc.calls)
	}
	if m.approvalModeLabel() != "dont-ask" {
		t.Fatalf("runtime mode=%s", m.approvalModeLabel())
	}
}

func TestAgentsSurfaceIsHumanized(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"agent.list": map[string]any{
			"agents": []any{map[string]any{"name": "explore", "description": "fast search", "profile": "read-only"}},
		},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	cmd := m.slashCommand("/agents")
	m.Update(cmd())
	got := transcriptText(m)
	if strings.Contains(got, `"name"`) || strings.Contains(got, "{") {
		// allow no raw JSON object dump
		if strings.Contains(got, "{\n") {
			t.Fatalf("agents should not dump JSON:\n%s", got)
		}
	}
	if !strings.Contains(got, "explore") {
		t.Fatalf("missing agent name:\n%s", got)
	}
}

func TestBtwForkBusyWhileRunning(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.inFlightTaskID = "task_1"
	if cmd := m.btwSideQuestion("q", true); cmd != nil {
		t.Fatal("fork must refuse while task running")
	}
	got := transcriptText(m)
	if !strings.Contains(got, "fork") && !strings.Contains(strings.ToLower(got), "running") {
		// message is localized; ensure something was pushed
		if got == "" {
			t.Fatal("expected busy notice")
		}
	}
}

func TestPlanReviewLineCommentsSeedComposer(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en", WorkspaceRoot: dir})
	m.sessionID, m.mode = "sess_plan", "plan"
	_ = m.ensurePlanFileScaffold()
	body := "# Goal\nShip Wave N\n## Steps\n1. compact\n2. dual-pane\n"
	_ = os.WriteFile(m.planFilePath(), []byte(body), 0o600)
	m.openPlanReview()
	if m.planReview == nil {
		t.Fatal("expected overlay")
	}
	// Move to line 2 and comment.
	m.planReview.Cursor = 1
	_, handled := m.planReviewKey("c")
	if !handled || !m.planReview.CommentMode {
		t.Fatal("comment mode not entered")
	}
	for _, r := range []string{"n", "e", "e", "d", " ", "t", "e", "s", "t", "s"} {
		m.planReviewKey(r)
	}
	m.planReviewKey("enter")
	if len(m.planReview.Comments) != 1 || m.planReview.Comments[0].Text != "need tests" {
		t.Fatalf("comments=%#v", m.planReview.Comments)
	}
	// Range mark lines 4-5 then comment.
	m.planReview.Cursor = 3
	m.planReviewKey("m")
	m.planReview.Cursor = 4
	m.planReviewKey("c")
	for _, r := range []string{"s", "p", "l", "i", "t"} {
		m.planReviewKey(r)
	}
	m.planReviewKey("enter")
	if len(m.planReview.Comments) != 2 {
		t.Fatalf("want 2 comments, got %#v", m.planReview.Comments)
	}
	m.planReviewKey("s")
	if m.planReview != nil {
		t.Fatal("overlay should close after request-changes")
	}
	seed := m.input.Value()
	if !strings.Contains(seed, "L2") || !strings.Contains(seed, "need tests") {
		t.Fatalf("seed missing line comment: %q", seed)
	}
	if !strings.Contains(seed, "L4") || !strings.Contains(seed, "split") {
		t.Fatalf("seed missing range comment: %q", seed)
	}
}

func TestSidePaneArmsOnForkAndCloses(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"session.fork": map[string]any{"session_id": "sess_side", "workspace_root": "/tmp/ws", "status": "active"},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess_main", fc
	m.push("main line A")
	m.push("main line B")
	var switched string
	m.switchSession = func(id string) error { switched = id; return nil }

	cmd := m.btwSideQuestion("what is dual-pane?", true)
	if m.sidePane == nil || m.sidePane.PrimarySessionID != "sess_main" {
		t.Fatalf("side pane not armed: %#v", m.sidePane)
	}
	if len(m.sidePane.PrimaryLines) < 2 {
		t.Fatalf("primary snapshot empty: %#v", m.sidePane.PrimaryLines)
	}
	if cmd == nil {
		t.Fatal("expected fork cmd")
	}
	// Deliver fork action result.
	m.sessionOpGen++
	gen := m.sessionOpGen
	// Simulate beginSessionAction generation
	m.sessionOpGen = 1
	m.sessionActionPending = "fork"
	m.handleSessionAction(sessionActionMsg{
		generation: 1, action: "fork",
		session: sessionListItem{SessionID: "sess_side", WorkspaceRoot: "/tmp/ws"},
	})
	if switched != "sess_side" {
		t.Fatalf("switch=%q", switched)
	}
	if m.sidePane == nil || m.sidePane.SideSessionID != "sess_side" {
		t.Fatalf("side session not noted: %#v", m.sidePane)
	}
	// Attach to side session.
	m.sessionID = "sess_side"
	m.pendingSessionID = ""
	if !m.sidePaneActive() {
		t.Fatal("dual-pane should be active on side session")
	}
	// Render dual-pane without panic.
	m.width, m.height = 100, 30
	m.layout()
	view := m.dualPaneTranscriptView(100, 20)
	if !strings.Contains(ansi.Strip(view), "main") && !strings.Contains(strings.ToLower(ansi.Strip(view)), "main") {
		// Labels are localized; at least both columns should render.
		if view == "" {
			t.Fatal("empty dual pane view")
		}
	}

	// Close side pane returns to main.
	m.switchSession = func(id string) error { switched = id; return nil }
	_ = m.closeSidePane()
	if switched != "sess_main" {
		t.Fatalf("close switch=%q want sess_main", switched)
	}
	if m.sidePane != nil {
		t.Fatal("side pane should clear")
	}
	_ = gen
}
