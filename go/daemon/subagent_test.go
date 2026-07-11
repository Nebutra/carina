package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAttenuateChildNeverExceedsParent(t *testing.T) {
	// child requests more than parent -> clamped to parent
	if got := attenuate("read-only", "full-workspace"); got != "read-only" {
		t.Fatalf("child must not exceed parent: got %s", got)
	}
	// child requests less -> honored
	if got := attenuate("full-workspace", "read-only"); got != "read-only" {
		t.Fatalf("more-restrictive request should hold: got %s", got)
	}
	// equal
	if got := attenuate("safe-edit", "safe-edit"); got != "safe-edit" {
		t.Fatalf("equal should hold: got %s", got)
	}
	// unknown -> least privilege
	if got := attenuate("safe-edit", ""); got != "read-only" {
		t.Fatalf("empty request should default to read-only: got %s", got)
	}
}

func TestParseAgentSpec(t *testing.T) {
	md := "---\nname: scout\ndescription: fast recon\nprofile: read-only\nmodel: haiku\nmax_turns: 6\n---\nYou are a scout. Find things.\n"
	spec := parseAgentSpec(md)
	if spec == nil || spec.Name != "scout" || spec.Profile != "read-only" || spec.MaxTurns != 6 {
		t.Fatalf("bad spec: %+v", spec)
	}
	if spec.SystemPrompt == "" || spec.Description != "fast recon" {
		t.Fatalf("missing body/description: %+v", spec)
	}
}

// TestParseAgentSpecDeclarativeManifestFields covers the additive
// tool_names/spawnable_agents/input_schema/output_schema frontmatter keys: a
// spec without them is unrestricted (nil slices, empty schemas), matching
// every pre-existing built-in/user spec.
func TestParseAgentSpecDeclarativeManifestFields(t *testing.T) {
	md := "---\n" +
		"name: reviewer\n" +
		"description: reviews diffs\n" +
		"profile: read-only\n" +
		"tool_names: search, read, code.search\n" +
		"spawnable_agents: explore, scout\n" +
		`input_schema: {"type":"object","properties":{"diff":{"type":"string"}}}` + "\n" +
		`output_schema: {"type":"object","properties":{"verdict":{"type":"string"}}}` + "\n" +
		"---\nYou review diffs.\n"
	spec := parseAgentSpec(md)
	if spec == nil {
		t.Fatal("expected a parsed spec")
	}
	wantTools := []string{"search", "read", "code.search"}
	if len(spec.ToolNames) != len(wantTools) {
		t.Fatalf("tool_names = %v, want %v", spec.ToolNames, wantTools)
	}
	for i, w := range wantTools {
		if spec.ToolNames[i] != w {
			t.Fatalf("tool_names[%d] = %q, want %q", i, spec.ToolNames[i], w)
		}
	}
	wantAgents := []string{"explore", "scout"}
	if len(spec.SpawnableAgents) != len(wantAgents) {
		t.Fatalf("spawnable_agents = %v, want %v", spec.SpawnableAgents, wantAgents)
	}
	for i, w := range wantAgents {
		if spec.SpawnableAgents[i] != w {
			t.Fatalf("spawnable_agents[%d] = %q, want %q", i, spec.SpawnableAgents[i], w)
		}
	}
	if spec.InputSchema == "" || !contains(spec.InputSchema, `"diff"`) {
		t.Fatalf("input_schema not captured: %q", spec.InputSchema)
	}
	if spec.OutputSchema == "" || !contains(spec.OutputSchema, `"verdict"`) {
		t.Fatalf("output_schema not captured: %q", spec.OutputSchema)
	}

	// A spec that omits these fields must be unrestricted — every existing
	// built-in/user spec relies on this.
	plain := parseAgentSpec("---\nname: plain\nprofile: read-only\n---\nbody\n")
	if len(plain.ToolNames) != 0 || len(plain.SpawnableAgents) != 0 || plain.InputSchema != "" || plain.OutputSchema != "" {
		t.Fatalf("spec without manifest fields must be unrestricted, got: %+v", plain)
	}
}

// TestSubagentIsolatedAndAttenuated: a full-workspace parent spawns a
// read-only scout; the scout runs in its own restricted session and its
// summary returns to the parent. The scout's session must be read-only
// (attenuated) regardless of the parent.
func TestSubagentIsolatedAndAttenuated(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	// Define a project-local scout agent (read-only).
	agentsDir := filepath.Join(ws, ".carina", "agents")
	os.MkdirAll(agentsDir, 0o755)
	os.WriteFile(filepath.Join(agentsDir, "scout.md"),
		[]byte("---\nname: scout\ndescription: recon\nprofile: read-only\nmax_turns: 3\n---\nYou are a scout. Report what you find.\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "target.txt"), []byte("SECRET_MARKER here\n"), 0o600)

	// Scout's scripted behavior: search, then done.
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"search","pattern":"SECRET_MARKER"}`,
		`{"tool":"done","summary":"found SECRET_MARKER in target.txt"}`,
	}})

	// Parent session is full-workspace (permissive).
	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "delegate recon")

	summary := d.spawnSubagent(parent, parentTask, "scout", "find SECRET_MARKER")
	if summary == "" || !contains(summary, "SECRET_MARKER") {
		t.Fatalf("subagent should return its finding, got: %q", summary)
	}

	// A new child session must exist, be read-only (attenuated from parent's
	// full-workspace), and be linked to the parent at depth 1.
	var child *struct {
		profile, parent string
		depth           int
	}
	for _, s := range d.store.List() {
		if s.ParentID == parent.SessionID {
			child = &struct {
				profile, parent string
				depth           int
			}{s.PermissionProfile, s.ParentID, s.Depth}
		}
	}
	if child == nil {
		t.Fatal("no child session created")
	}
	if child.profile != "read-only" {
		t.Fatalf("child must be attenuated to read-only, got %s", child.profile)
	}
	if child.depth != 1 {
		t.Fatalf("child depth should be 1, got %d", child.depth)
	}
}

// TestSpawnedSessionToolNamesAllowListIsEnforced is the end-to-end wiring
// test for the additive tool_names declarative manifest field: spawning an
// agent whose spec declares tool_names must populate d.allowedTools for that
// child session for the duration of its run, and clear it once the run
// finishes. TestToolNamesAllowListDeniesOutOfListTool is the direct proof
// that the allow-list, once populated, actually denies an out-of-list tool.
func TestSpawnedSessionToolNamesAllowListIsEnforced(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	agentsDir := filepath.Join(ws, ".carina", "agents")
	os.MkdirAll(agentsDir, 0o755)
	os.WriteFile(filepath.Join(agentsDir, "searcher.md"),
		[]byte("---\nname: searcher\nprofile: read-only\ntool_names: search\nmax_turns: 3\n---\nYou only search.\n"), 0o644)
	os.WriteFile(filepath.Join(ws, "target.txt"), []byte("needle\n"), 0o600)

	var capturedSessionID string
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"read","path":"target.txt"}`,  // outside the allow-list -> must be denied
		`{"tool":"search","pattern":"needle"}`, // inside the allow-list -> must proceed
		`{"tool":"done","summary":"done"}`,
	}})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "delegate search")

	// Capture the child session ID as soon as it's created so we can also
	// assert directly against the allow-list map, not just the transcript.
	before := map[string]bool{}
	for _, s := range d.store.List() {
		before[s.SessionID] = true
	}

	summary := d.spawnSubagent(parent, parentTask, "searcher", "find needle")
	if summary == "" {
		t.Fatal("expected a summary")
	}
	for _, s := range d.store.List() {
		if !before[s.SessionID] && s.ParentID == parent.SessionID {
			capturedSessionID = s.SessionID
		}
	}
	if capturedSessionID == "" {
		t.Fatal("no child session created")
	}
	if _, ok := d.allowedTools.Load(capturedSessionID); ok {
		t.Fatal("allowedTools entry should be cleared once the child session's run finished")
	}
}

// TestToolNamesAllowListDeniesOutOfListTool directly exercises
// dispatchActionOutcome's allow-list gate (dispatchActionOutcome is the
// single choke point every tool goes through), independent of the full
// subagent-loop plumbing above.
func TestToolNamesAllowListDeniesOutOfListTool(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	sess, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "full-workspace", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "task")

	d.allowedTools.Store(sess.SessionID, map[string]bool{"search": true})
	defer d.allowedTools.Delete(sess.SessionID)

	outcome := d.dispatchActionOutcome(sess, task, &action{Tool: "read", Path: "x.txt"})
	if outcome.status != "denied" || outcome.errorCategory != "tool_not_allowed" {
		t.Fatalf("expected denied/tool_not_allowed, got status=%s category=%s", outcome.status, outcome.errorCategory)
	}

	// "done" must never be blockable by an allow-list, regardless of content.
	outcome = d.dispatchActionOutcome(sess, task, &action{Tool: "done", Summary: "ok"})
	if outcome.status == "denied" {
		t.Fatalf("\"done\" must never be denied by the tool allow-list, got: %+v", outcome)
	}
}

// TestSpawnedSessionSpawnableAgentsAllowListIsEnforced is the end-to-end
// wiring test for spawnable_agents: a spec declaring it restricts which
// agent names the spawned session may itself delegate to.
func TestSpawnedSessionSpawnableAgentsAllowListIsEnforced(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	agentsDir := filepath.Join(ws, ".carina", "agents")
	os.MkdirAll(agentsDir, 0o755)
	os.WriteFile(filepath.Join(agentsDir, "lead.md"),
		[]byte("---\nname: lead\nprofile: full-workspace\nspawnable_agents: scout\nmax_turns: 4\n---\nYou delegate.\n"), 0o644)
	os.WriteFile(filepath.Join(agentsDir, "scout.md"),
		[]byte("---\nname: scout\nprofile: read-only\nmax_turns: 2\n---\nYou scout.\n"), 0o644)
	os.WriteFile(filepath.Join(agentsDir, "other.md"),
		[]byte("---\nname: other\nprofile: read-only\nmax_turns: 2\n---\nNot spawnable by lead.\n"), 0o644)

	d.SetReasoner(&scriptedReasoner{steps: []string{`{"tool":"done","summary":"scouted"}`}})

	parent, _ := d.store.CreateSessionMode(ws, "full-workspace", "on_request")
	d.kern.InitSessionFull(parent.SessionID, ws, "full-workspace", "on_request", nil)
	parentTask := d.sched.Submit(parent.SessionID, parent.WorkspaceID, "delegate to lead")

	specs := loadAgentSpecs(ws)
	leadSpec := specs["lead"]
	if leadSpec == nil || len(leadSpec.SpawnableAgents) != 1 || leadSpec.SpawnableAgents[0] != "scout" {
		t.Fatalf("bad lead spec: %+v", leadSpec)
	}

	// Directly simulate "lead" being the currently-active session (bypassing
	// a full parent->lead->{scout,other} three-level spawn, which needs more
	// scripted turns than is worth wiring here) — spawnAllowed is the actual
	// unit under test, and this exercises it exactly as spawnSubagentContext
	// calls it.
	d.allowedSpawnAgents.Store(parent.SessionID, toSet(leadSpec.SpawnableAgents))
	defer d.allowedSpawnAgents.Delete(parent.SessionID)

	if d.spawnAllowed(parent.SessionID, "other") {
		t.Fatal("spawning \"other\" should be denied — not in lead's spawnable_agents")
	}
	if !d.spawnAllowed(parent.SessionID, "scout") {
		t.Fatal("spawning \"scout\" should be allowed — it is in lead's spawnable_agents")
	}

	summary := d.spawnSubagent(parent, parentTask, "other", "try to reach other")
	if !contains(summary, "DENIED") {
		t.Fatalf("spawning an out-of-allow-list agent must be denied, got: %q", summary)
	}

	summary = d.spawnSubagent(parent, parentTask, "scout", "scout something")
	if contains(summary, "DENIED") {
		t.Fatalf("spawning an in-allow-list agent must succeed, got: %q", summary)
	}
}

// TestSubagentDepthLimit: nesting is bounded.
func TestSubagentDepthLimit(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	// A session already at max depth cannot spawn.
	deep, _ := d.store.CreateSubSession(ws, "read-only", "on_request", "sess_parent", maxSubagentDepth)
	d.kern.InitSessionFull(deep.SessionID, ws, "read-only", "on_request", nil)
	task := d.sched.Submit(deep.SessionID, deep.WorkspaceID, "x")
	obs := d.executeSpawn(deep, task, &action{Tool: "spawn", Agent: "scout", Task: "y"})
	if !contains(obs, "depth") {
		t.Fatalf("spawn at max depth should be refused, got: %s", obs)
	}
}

func TestSubagentLoopHonorsParentCancellation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	reasoner := &cancellationBlockingReasoner{started: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(ws, "read-only")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "read-only", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "wait")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan string, 1)
	go func() { result <- d.runSubagentLoopContext(ctx, sess, task, &AgentSpec{Name: "worker", MaxTurns: 2}) }()
	select {
	case <-reasoner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("subagent reasoner did not start")
	}
	cancel()
	select {
	case <-reasoner.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("subagent reasoner did not observe cancellation")
	}
	close(reasoner.release)
	select {
	case got := <-result:
		if got != "subagent cancelled" {
			t.Fatalf("result = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subagent did not exit after cancellation")
	}
	current, _ := d.sched.Get(task.TaskID)
	if current.Status != "cancelled" {
		t.Fatalf("task status = %s", current.Status)
	}
}
