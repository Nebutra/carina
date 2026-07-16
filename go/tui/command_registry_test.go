package tui

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
	"github.com/charmbracelet/x/ansi"
)

func TestBuiltinCommandRegistryDrivesHelpValidationAndSuggest(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range builtinCommandRegistry {
		if seen[d.Name] {
			t.Fatalf("duplicate %s", d.Name)
		}
		seen[d.Name] = true
		if d.Usage == "" || d.Description == "" || d.Source == "" || d.Validate == nil {
			t.Fatalf("incomplete descriptor: %+v", d)
		}
		if !strings.HasPrefix(d.Usage, "/"+d.Name) {
			t.Fatalf("usage mismatch: %+v", d)
		}
	}
	if len(builtinCommandNamesFromRegistry()) != len(builtinCommandRegistry) {
		t.Fatal("name registry diverged")
	}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	help := strings.Join(m.helpBodyLines(), "\n")
	for _, d := range builtinCommandRegistry {
		if d.HelpID != "" && !strings.Contains(help, "/"+d.Name) {
			t.Fatalf("help missing %s", d.Name)
		}
	}
	if !validSlashCommand("/effort high") || validSlashCommand("/effort extreme") || validSlashCommand("/doctor now") {
		t.Fatal("descriptor validation parity failed")
	}
}

func TestDynamicSlashResolvesThenUsesReliableTaskSubmit(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"command.list": map[string]any{"commands": []any{map[string]any{"name": "deploy", "description": "deploy safely", "source": "project"}}}, "task.submit": map[string]any{"task_id": "tsk_dynamic", "status": "queued"}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	m.input.SetValue("/deploy staging")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("dynamic resolver missing")
	}
	_, submitCmd := m.Update(cmd())
	if submitCmd == nil {
		t.Fatal("resolved command was not submitted")
	}
	m.Update(submitCmd())
	if len(fc.calls) < 2 || fc.calls[0].method != "command.list" || fc.calls[1].method != "task.submit" {
		t.Fatalf("calls=%#v", fc.calls)
	}
	if fc.calls[1].params["prompt"] != "/deploy staging" {
		t.Fatalf("prompt=%#v", fc.calls[1].params)
	}
}

func TestUnknownDynamicSlashPreservesDraft(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"command.list": map[string]any{"commands": []any{}}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	m.input.SetValue("/missing keep-me")
	cmd := m.submit()
	m.Update(cmd())
	if m.input.Value() != "/missing keep-me" {
		t.Fatalf("unknown command consumed draft: %q", m.input.Value())
	}
}

func TestCommandSuggestShowsMetadataAndFitsNarrow(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.width = 24
	m.handleSuggestResult(suggestResultMsg{gen: m.suggestGen, trigger: mentionTrigger{Kind: mentionCommand}, matches: []string{"doctor"}, details: []string{"/doctor · runtime diagnostics · builtin · enabled"}})
	for _, line := range m.suggestPanelLines() {
		if ansi.StringWidth(line) > 24 {
			t.Fatalf("overflow %d: %q", ansi.StringWidth(line), line)
		}
	}
}

func TestReviewUsesTaskSubmitAndSessionReviewRemainsReadOnly(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": map[string]any{"task_id": "tsk_review", "status": "queued"}, "session.review": map[string]any{"status": "healthy"}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	if cmd := m.slashCommand("/review branch main"); cmd == nil {
		t.Fatal("review did not submit")
	} else {
		m.Update(cmd())
	}
	if len(fc.calls) != 1 || fc.calls[0].method != "task.submit" || fc.calls[0].params["prompt"] != "/review branch main" {
		t.Fatalf("review calls=%#v", fc.calls)
	}
	if cmd := m.slashCommand("/session-review"); cmd == nil {
		t.Fatal("session-review did not query")
	} else {
		m.Update(cmd())
	}
	if len(fc.calls) != 2 || fc.calls[1].method != "session.review" {
		t.Fatalf("session review calls=%#v", fc.calls)
	}
}

func TestMemorySubcommandsDispatchStructuredRPCs(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"memory.status": map[string]any{}, "memory.list": map[string]any{}, "memory.search": map[string]any{},
		"memory.read": map[string]any{}, "memory.verify": map[string]any{}, "memory.rollback": map[string]any{}, "memory.handoff": map[string]any{},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	for _, command := range []string{
		"/memory status", "/memory list", "/memory search release notes", "/memory read user", "/memory verify memory mr_current",
		"/memory rollback memory mr_old mr_current rollback-1 --yes", "/memory handoff sess_target memory mr_target handoff-1 --yes",
	} {
		cmd := m.slashCommand(command)
		if cmd == nil {
			t.Fatalf("%s returned nil", command)
		}
		m.Update(cmd())
	}
	want := []string{"memory.status", "memory.list", "memory.search", "memory.read", "memory.verify", "memory.rollback", "memory.handoff"}
	for i := range want {
		if fc.calls[i].method != want[i] {
			t.Fatalf("call %d = %s, want %s", i, fc.calls[i].method, want[i])
		}
	}
	if fc.calls[2].params["query"] != "release notes" || fc.calls[2].params["mode"] != "auto" {
		t.Fatalf("search params=%#v", fc.calls[2].params)
	}
	if fc.calls[5].params["expected_revision"] != "mr_current" || fc.calls[6].params["target_session_id"] != "sess_target" {
		t.Fatalf("versioned memory params: rollback=%#v handoff=%#v", fc.calls[5].params, fc.calls[6].params)
	}
	if validSlashCommand("/memory rollback memory mr_old mr_current rollback-1") {
		t.Fatal("memory rollback accepted without explicit --yes")
	}
}

func TestPermissionProfileChangeCreatesExplicitSessionBoundary(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.create": map[string]any{"session_id": "sess_full", "workspace_root": "/repo"}}}
	var switched string
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en", WorkspaceRoot: "/repo", SwitchSession: func(id string) error { switched = id; return nil }})
	m.sessionID, m.call = "sess_safe", fc
	if validSlashCommand("/permissions new full-workspace") {
		t.Fatal("elevated profile accepted without --yes")
	}
	cmd := m.slashCommand("/permissions new full-workspace --yes")
	if cmd == nil {
		t.Fatal("permission boundary did not create a session")
	}
	m.Update(cmd())
	if len(fc.calls) != 1 || fc.calls[0].method != "session.create" || fc.calls[0].params["profile"] != "full-workspace" || switched != "sess_full" {
		t.Fatalf("permission boundary calls=%#v switched=%q", fc.calls, switched)
	}
	for _, call := range fc.calls {
		if call.method == "profile.set" || call.method == "session.profile.set" {
			t.Fatalf("permission profile mutated in place: %#v", call)
		}
	}
}

func TestConfigDelegatesToGovernedSettingTransactions(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"session.model.set": map[string]any{"model": "", "reasoning_effort": "high"},
		"session.plan_mode": map[string]any{"plan_mode": true},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	for _, command := range []string{"/config effort high", "/config mode plan"} {
		cmd := m.slashCommand(command)
		if cmd == nil {
			t.Fatalf("%s did not dispatch", command)
		}
		m.Update(cmd())
	}
	if len(fc.calls) != 2 || fc.calls[0].method != "session.model.set" || fc.calls[1].method != "session.plan_mode" {
		t.Fatalf("config bypassed governed handlers: %#v", fc.calls)
	}
}
