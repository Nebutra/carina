package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestSettingsShellOpensFromConfigAndSettings(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID = "sess_settings"
	m.mode = "build"
	m.model = "openai/gpt-test"
	if cmd := m.slashCommand("/settings"); cmd == nil && m.settings == nil {
		// /settings returns refresh cmd after opening shell
	}
	m.slashCommand("/settings")
	if m.settings == nil {
		t.Fatal("/settings did not open settings shell")
	}
	view := m.settingsOverlayView()
	for _, want := range []string{"Settings", "Overview", "build", "openai/gpt-test"} {
		if !strings.Contains(view, want) {
			t.Fatalf("settings view missing %q:\n%s", want, view)
		}
	}
	m.closeSettings()
	m.slashCommand("/config")
	if m.settings == nil {
		t.Fatal("/config should open settings shell (not dump inventory)")
	}
}

func TestModeCycleTogglesPlanAndBuild(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.plan_mode": map[string]any{"ok": true}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call, m.mode = "sess", fc, "build"
	cmd := m.cycleInteractionMode()
	if cmd == nil {
		t.Fatal("cycle returned nil")
	}
	m.Update(cmd())
	if m.mode != "plan" {
		t.Fatalf("mode=%q, want plan", m.mode)
	}
	if len(fc.calls) != 1 || fc.calls[0].params["on"] != true {
		t.Fatalf("plan_mode call = %#v", fc.calls)
	}
	cmd = m.cycleInteractionMode()
	m.Update(cmd())
	if m.mode != "build" {
		t.Fatalf("mode=%q, want build", m.mode)
	}
}

func TestContextSurfaceIsHumanizedWithBar(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"context.summary": map[string]any{
			"model_context_tokens": map[string]any{
				"available": true, "tokens": 80, "limit_tokens": 100, "used_percent": 80,
				"remaining_tokens": 20, "measurement": "latest completed provider request",
			},
			"compact": map[string]any{"available": false, "reason": "no paused checkpoint"},
		},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	cmd := m.slashCommand("/context")
	m.Update(cmd())
	got := transcriptText(m)
	if !strings.Contains(got, "80%") || !strings.Contains(got, "[") || strings.Contains(got, `{"available"`) {
		t.Fatalf("context surface not humanized:\n%s", got)
	}
	if !m.runtime.ContextAvailable || m.runtime.ContextPercent != 80 {
		t.Fatalf("runtime context snapshot not updated: %+v", m.runtime)
	}
}

func TestSkillSlashResolvesViaInventory(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"command.list": map[string]any{"commands": []any{}},
		"skill.inventory": map[string]any{
			"skills": []any{map[string]any{"name": "review-pr", "user_invocable": true, "description": "review a PR"}},
		},
		"task.submit": map[string]any{"task_id": "tsk_skill", "status": "queued"},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	m.input.SetValue("/review-pr")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("expected dynamic skill resolve")
	}
	_, submit := m.Update(cmd())
	if submit == nil {
		t.Fatal("skill resolve should submit")
	}
	m.Update(submit())
	if len(fc.calls) < 2 {
		t.Fatalf("calls=%#v", fc.calls)
	}
	foundSubmit := false
	for _, c := range fc.calls {
		if c.method == "task.submit" {
			foundSubmit = true
			prompt, _ := c.params["prompt"].(string)
			if !strings.Contains(prompt, "review-pr") {
				t.Fatalf("prompt missing skill name: %q", prompt)
			}
		}
	}
	if !foundSubmit {
		t.Fatalf("task.submit not called: %#v", fc.calls)
	}
}

func TestExportWritesPlainTranscript(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en", WorkspaceRoot: dir})
	m.push("hello export line")
	path := dir + "/out.md"
	m.exportTranscript(path)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "hello export line") {
		t.Fatalf("export body=%q", body)
	}
}
