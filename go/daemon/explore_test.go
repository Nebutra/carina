package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/scheduler"
)

type promptSpyReasoner struct {
	scriptedReasoner
	prompts []string
}

func (r *promptSpyReasoner) Think(ctx context.Context, prompt string) (string, error) {
	r.prompts = append(r.prompts, prompt)
	return r.scriptedReasoner.Think(ctx, prompt)
}

func TestCheapestSameProviderModelPicksStrictlyCheaperSibling(t *testing.T) {
	catalog := provider.Catalog{
		"anthropic": {
			ID: "anthropic",
			Models: map[string]provider.Model{
				"claude-sonnet-4": {ID: "claude-sonnet-4", Cost: &provider.ModelCost{Input: 3, Output: 15}},
				"claude-haiku-4":  {ID: "claude-haiku-4", Cost: &provider.ModelCost{Input: 0.8, Output: 4}},
				"claude-opus-4":   {ID: "claude-opus-4", Cost: &provider.ModelCost{Input: 15, Output: 75}},
			},
		},
	}
	got := cheapestSameProviderModel(catalog, "anthropic/claude-sonnet-4", nil)
	if got != "anthropic/claude-haiku-4" {
		t.Fatalf("cheaper model = %q", got)
	}
	if got := cheapestSameProviderModel(catalog, "anthropic/claude-haiku-4", nil); got != "" {
		t.Fatalf("already-cheapest must stay, got %q", got)
	}
	if got := cheapestSameProviderModel(catalog, "anthropic/claude-sonnet-4", map[string]bool{"anthropic": true}); got != "" {
		t.Fatalf("disabled provider must not reroute, got %q", got)
	}
	if got := cheapestSameProviderModel(nil, "anthropic/claude-sonnet-4", nil); got != "" {
		t.Fatalf("empty catalog must not invent a model, got %q", got)
	}
}

func TestExplorePromptOmitsProjectInstructionsAndWriteTools(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if err := os.WriteFile(filepath.Join(ws, "AGENTS.md"), []byte("DO_NOT_INJECT_AGENTS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "CARINA.md"), []byte("DO_NOT_INJECT_CARINA\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "target.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	spy := &promptSpyReasoner{scriptedReasoner: scriptedReasoner{steps: []string{
		`{"tool":"search","pattern":"needle"}`,
		`{"tool":"done","summary":"found needle in target.txt"}`,
	}}}
	d.SetReasoner(spy)

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.SubmitWithGoalModelAgent(parent.SessionID, parent.WorkspaceID, "delegate explore", "anthropic/claude-sonnet-4", "build", nil)

	summary := d.spawnSubagent(parent, parentTask, "explore", "find needle")
	if !strings.Contains(summary, "needle") {
		t.Fatalf("explore summary = %q", summary)
	}
	if len(spy.prompts) == 0 {
		t.Fatal("explore must call the reasoner")
	}
	joined := strings.Join(spy.prompts, "\n")
	for _, banned := range []string{
		"DO_NOT_INJECT_AGENTS", "DO_NOT_INJECT_CARINA", "PROJECT INSTRUCTIONS",
		`"tool":"patch"`, `"tool":"edit"`, `"tool":"run"`, `"tool":"spawn"`,
		`"tool":"mcp"`, "On branch",
	} {
		if strings.Contains(joined, banned) {
			t.Fatalf("explore prompt leaked %q:\n%s", banned, joined)
		}
	}
	for _, want := range []string{`"tool":"search"`, `"tool":"code.map"`, "workspace_root="} {
		if !strings.Contains(joined, want) {
			t.Fatalf("explore prompt missing %q:\n%s", want, joined)
		}
	}
}

func TestExploreUsesCheaperCatalogModelUnlessSpecPinsOne(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.providerCatalog = provider.Catalog{
		"anthropic": {
			ID: "anthropic",
			Models: map[string]provider.Model{
				"claude-sonnet-4": {ID: "claude-sonnet-4", Cost: &provider.ModelCost{Input: 3, Output: 15}},
				"claude-haiku-4":  {ID: "claude-haiku-4", Cost: &provider.ModelCost{Input: 0.8, Output: 4}},
			},
		},
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"ok"}`}})
	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.SubmitWithGoalModelAgent(parent.SessionID, parent.WorkspaceID, "parent", "anthropic/claude-sonnet-4", "build", nil)

	_ = d.spawnSubagent(parent, parentTask, "explore", "look around")
	var child *scheduler.ExecutionRun
	for _, task := range d.sched.List() {
		if task.Agent == "explore" {
			child = task
			break
		}
	}
	if child == nil || child.Model != "anthropic/claude-haiku-4" {
		t.Fatalf("explore child model = %+v", child)
	}

	agentsDir := filepath.Join(ws, ".carina", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "explore.md"), []byte("---\nname: explore\nprofile: read-only\nmodel: anthropic/claude-sonnet-4\nmax_turns: 2\n---\nPinned explore.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = d.spawnSubagent(parent, parentTask, "explore", "pinned")
	var pinned *scheduler.ExecutionRun
	for _, task := range d.sched.List() {
		if task.Agent == "explore" && task.UserPrompt == "pinned" {
			pinned = task
			break
		}
	}
	if pinned == nil || pinned.Model != "anthropic/claude-sonnet-4" {
		t.Fatalf("explicit explore model = %+v", pinned)
	}
}

func TestExploreAllowListDeniesPatch(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"patch","path":"x.txt","content":"nope"}`,
		`{"tool":"done","summary":"stayed read-only"}`,
	}})
	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "delegate")
	summary := d.spawnSubagent(parent, parentTask, "explore", "do not edit")
	if !strings.Contains(summary, "read-only") {
		t.Fatalf("explore should finish after denied patch, got %q", summary)
	}
	var childTaskID string
	for _, task := range d.sched.List() {
		if task.Agent == "explore" {
			childTaskID = task.RunID
		}
	}
	cp := d.runs.loadCheckpoint(childTaskID)
	if cp == nil || cp.Transcript == nil {
		t.Fatal("missing explore checkpoint")
	}
	denied := false
	for _, turn := range cp.Transcript.Turns {
		if strings.Contains(turn.Obs.Content, "DENIED") && strings.Contains(turn.ActionBrief, "patch") {
			denied = true
		}
	}
	if !denied {
		t.Fatalf("explore patch must be denied, turns=%+v", cp.Transcript.Turns)
	}
}

func TestNonExploreSubagentKeepsFullToolContract(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	agentsDir := filepath.Join(ws, ".carina", "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "scout.md"), []byte("---\nname: scout\nprofile: read-only\nmax_turns: 2\n---\nYou scout.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	spy := &promptSpyReasoner{scriptedReasoner: scriptedReasoner{steps: []string{`{"tool":"done","summary":"scouted"}`}}}
	d.SetReasoner(spy)
	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "delegate")
	_ = d.spawnSubagent(parent, parentTask, "scout", "look")
	if len(spy.prompts) == 0 || !strings.Contains(spy.prompts[0], toolsHelp) || !strings.Contains(spy.prompts[0], "patch:") {
		t.Fatalf("non-explore subagent must keep the full tool contract, prompts=%q", spy.prompts)
	}
}

func TestBuiltinExploreSpecIsAttenuated(t *testing.T) {
	spec := builtinAgentSpecs()["explore"]
	if spec == nil || !isExploreSubagent(spec) {
		t.Fatal("missing explore spec")
	}
	if len(spec.ToolNames) == 0 || !spec.RestrictedTools["patch"] || !spec.RestrictedTools["run"] {
		t.Fatalf("explore must attenuate writes: %+v", spec)
	}
}
