package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
)

func TestAgentRegistryBuiltinsAndProjectOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := t.TempDir()
	dir := filepath.Join(ws, ".carina", "agents")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "explore.md"), []byte(`---
name: explore
description: Project explorer
profile: ci-runner
mode: subagent
max_turns: 4
---
Project-specific explore prompt.
`), 0o600); err != nil {
		t.Fatal(err)
	}

	specs := loadAgentSpecs(ws)
	if specs["converse"] == nil || specs["build"] == nil || specs["plan"] == nil || specs["general"] == nil {
		t.Fatalf("built-in agents missing: %v", specNames(specs))
	}
	explore := specs["explore"]
	if explore == nil || explore.Source != "project" || explore.Profile != "ci-runner" || explore.MaxTurns != 4 {
		t.Fatalf("project override not applied: %+v", explore)
	}
	infos := sortedAgentInfos(specs, false)
	if len(infos) < 4 || infos[0].Name == "" {
		t.Fatalf("bad agent infos: %+v", infos)
	}
}

func TestCommandRegistryExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := t.TempDir()
	dir := filepath.Join(ws, ".carina", "commands")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fix.md"), []byte(`---
description: Fix one target
agent: build
model: openai/gpt-5
---
Fix $1 with context $ARGUMENTS.
`), 0o600); err != nil {
		t.Fatal(err)
	}

	specs := loadCommandSpecs(ws)
	if specs["review"] == nil || specs["init"] == nil {
		t.Fatalf("built-in commands missing: %+v", specs)
	}
	expanded, ok, err := expandSlashCommand("/fix parser extra", specs)
	if err != nil || !ok {
		t.Fatalf("expand: %+v ok=%v err=%v", expanded, ok, err)
	}
	if expanded.Agent != "build" || expanded.Model != "openai/gpt-5" {
		t.Fatalf("metadata not preserved: %+v", expanded)
	}
	if !strings.Contains(expanded.Prompt, "Fix parser with context parser extra.") {
		t.Fatalf("template not expanded: %q", expanded.Prompt)
	}
}

func TestCommandRegistryProjectOverridesUserAndBuiltinSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	userDir := filepath.Join(home, ".carina", "commands")
	workspace := t.TempDir()
	projectDir := filepath.Join(workspace, ".carina", "commands")
	for _, dir := range []string{userDir, projectDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(userDir, "review.md"), []byte("User review $ARGUMENTS"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "review.md"), []byte("Project review $ARGUMENTS"), 0o600); err != nil {
		t.Fatal(err)
	}

	spec := loadCommandSpecs(workspace)["review"]
	if spec == nil || spec.Source != "project" || !strings.Contains(spec.Template, "Project review") {
		t.Fatalf("project command must win source precedence: %+v", spec)
	}
}

func TestCommandListSafeModeExposesOnlyBuiltins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	dir := filepath.Join(workspace, ".carina", "commands")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "project-only.md"), []byte("Project command"), 0o600); err != nil {
		t.Fatal(err)
	}
	d, _ := newLoopDaemon(t)
	defer d.Close()
	d.safeMode = true

	result, err := d.handleCommandList(mustJSON(t, map[string]any{"workspace_root": workspace}))
	if err != nil {
		t.Fatal(err)
	}
	commands := result.(map[string]any)["commands"].([]CommandInfo)
	for _, command := range commands {
		if command.Source != "built-in" || command.Name == "project-only" {
			t.Fatalf("safe mode leaked non-built-in command: %+v", commands)
		}
	}
}

func TestCommandInfosHaveStableIdentityAndRevision(t *testing.T) {
	specs := builtinCommandSpecs()
	first := sortedCommandInfos(specs)
	second := sortedCommandInfos(specs)
	if len(first) == 0 {
		t.Fatal("expected built-in commands")
	}
	for _, info := range first {
		if info.ID == "" || info.Kind != "prompt_template" {
			t.Fatalf("missing typed command identity: %+v", info)
		}
	}
	if got, want := commandRegistryRevision(first), commandRegistryRevision(second); got == "" || got != want {
		t.Fatalf("revision must be non-empty and deterministic: got %q want %q", got, want)
	}

	specs["z-last"] = &CommandSpec{Name: "z-last", Source: "project", Template: "last"}
	changed := commandRegistryRevision(sortedCommandInfos(specs))
	if changed == commandRegistryRevision(first) {
		t.Fatal("registry revision must change with public descriptors")
	}
}

func TestTaskSubmitExpandsSlashCommandAndAgent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	res, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "/review branch-main",
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := res.(*scheduler.ExecutionRun)
	if task.Agent != "explore" {
		t.Fatalf("command agent not applied: %+v", task)
	}
	if !strings.Contains(task.UserPrompt, "branch-main") || strings.Contains(task.UserPrompt, "/review") {
		t.Fatalf("command prompt not expanded: %q", task.UserPrompt)
	}
}
