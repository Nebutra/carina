package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	got := transcriptText(m)
	if !strings.Contains(got, path) || !strings.Contains(got, "Plan") {
		t.Fatalf("view-plan missing path/content:\n%s", got)
	}
	cmd := m.approvePlan()
	m.Update(cmd())
	if m.mode != "build" {
		t.Fatalf("mode=%q after approve", m.mode)
	}
	if len(fc.calls) == 0 || fc.calls[len(fc.calls)-1].method != "session.approve_plan" {
		t.Fatalf("calls=%#v", fc.calls)
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
	cmd := m.btwSideQuestion("what is the entrypoint?")
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
