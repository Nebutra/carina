package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/continuity"
	"github.com/Nebutra/carina/go/kernel"
)

// scripted reasoner from reasoner.go drives the loop deterministically.

type repoMapRoutingReasoner struct {
	prompts []string
}

func (r *repoMapRoutingReasoner) Name() string { return "repo-map-routing" }
func (r *repoMapRoutingReasoner) Think(_ context.Context, prompt string) (string, error) {
	r.prompts = append(r.prompts, prompt)
	switch len(r.prompts) {
	case 1:
		return `{"tool":"code.map","intent":"Map the repository before inspecting its core entry point"}`, nil
	case 2:
		if strings.Contains(prompt, "main.go") && strings.Contains(prompt, "OverviewEntryPoint") {
			return `{"tool":"read","path":"main.go","intent":"Inspect the entry point identified by the repository map"}`, nil
		}
		return `{"tool":"done","summary":"The repository map was not available to guide a targeted read."}`, nil
	default:
		return `{"tool":"done","summary":"The repository entry point is implemented in main.go."}`, nil
	}
}

func TestAgentLoopFeedsCodeMapObservationIntoTargetedRead(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\n\nfunc OverviewEntryPoint() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reasoner := &repoMapRoutingReasoner{}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "What is this unfamiliar repository, and where is its core entry point?")
	d.runTask(sess, task)

	if len(reasoner.prompts) != 3 {
		t.Fatalf("map observation did not drive map -> targeted read -> done; prompts = %d", len(reasoner.prompts))
	}
	if !strings.Contains(reasoner.prompts[0], `call "code.map" first`) {
		t.Fatal("initial broad-repository prompt is missing the code.map routing contract")
	}
	if !strings.Contains(reasoner.prompts[1], "OverviewEntryPoint") || !strings.Contains(reasoner.prompts[1], "main.go") {
		t.Fatalf("code.map observation was not visible to the next model turn:\n%s", reasoner.prompts[1])
	}
	if !strings.Contains(reasoner.prompts[2], "func OverviewEntryPoint()") {
		t.Fatalf("targeted read observation was not visible to the completion turn:\n%s", reasoner.prompts[2])
	}
	completed, _ := d.sched.Get(task.RunID)
	if completed.Status != "completed" {
		t.Fatalf("repository overview run status = %s", completed.Status)
	}
}

// TestAgentLoopExecutesThroughKernel proves the ReAct loop actually edits a
// file and runs a command — all mediated by the kernel — using a scripted
// reasoner (no model, no cost). This is the mechanism test for "carina as a
// real coding agent".
func TestAgentLoopExecutesThroughKernel(t *testing.T) {
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	toolsDir := filepath.Join(repoRoot, "zig/zig-out/bin")
	if _, err := os.Stat(filepath.Join(toolsDir, "carina-scan")); err != nil {
		t.Skip("zig tools not built")
	}

	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hello\n"), 0o600)

	d, err := New(Options{StateDir: t.TempDir(), KernelBin: kernelBin, ToolsDir: toolsDir, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	// Scripted decisions: list -> read -> patch -> run -> done.
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"thought":"survey","action":{"tool":"list"}}`,
		`{"thought":"read it","action":{"tool":"read","path":"hello.txt"}}`,
		`{"thought":"edit","action":{"tool":"patch","path":"hello.txt","content":"hello\n// added by the carina agent\n"}}`,
		`{"thought":"verify","action":{"tool":"run","command":["cat","hello.txt"]}}`,
		`{"thought":"finish","action":{"tool":"done","summary":"added a comment to hello.txt"}}`,
	}})

	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, sess.PermissionProfile, nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "add a comment to hello.txt")
	d.runTask(sess, task) // synchronous for the test

	// The file must actually have been edited (through the kernel + Zig).
	got, _ := os.ReadFile(filepath.Join(ws, "hello.txt"))
	if string(got) != "hello\n// added by the carina agent\n" {
		t.Fatalf("file not edited by agent: %q", got)
	}

	// The task completed.
	if tk, _ := d.sched.Get(task.RunID); tk.Status != "completed" {
		t.Fatalf("task status = %s, want completed", tk.Status)
	}

	// The audit trail is complete and the hash chain verifies.
	events, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	json.Unmarshal(events, &evs)
	types := map[string]bool{}
	routingEvidence := map[string]string{}
	for _, e := range evs {
		if s, ok := e["type"].(string); ok {
			types[s] = true
			if s == "RoutingDecision" || s == "RoutingOutcome" {
				payload, _ := e["payload"].(map[string]any)
				evidenceID, _ := payload["evidence_id"].(string)
				promptHash, _ := payload["prompt_sha256"].(string)
				if evidenceID == "" || promptHash == "" {
					t.Fatalf("%s missing routing evidence fields: %+v", s, payload)
				}
				if prev := routingEvidence[evidenceID]; prev != "" && prev != promptHash {
					t.Fatalf("routing evidence %s changed prompt hash: %s vs %s", evidenceID, prev, promptHash)
				}
				routingEvidence[evidenceID] = promptHash
				if s == "RoutingOutcome" && payload["status"] == "succeeded" && payload["response_sha256"] == "" {
					t.Fatalf("successful RoutingOutcome missing response hash: %+v", payload)
				}
			}
		}
	}
	for _, want := range []string{"RoutingDecision", "RoutingOutcome", "FileRead", "PatchProposed", "PatchApplied", "CommandStarted", "CommandExited"} {
		if !types[want] {
			t.Errorf("missing audit event %q; saw %v", want, keys(types))
		}
	}
	rep, err := d.kern.AuditVerify(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var vr struct {
		OK bool `json:"ok"`
	}
	json.Unmarshal(rep, &vr)
	if !vr.OK {
		t.Fatal("audit chain should verify after an agent run")
	}
}

func TestAgentLoopWithoutReasonerFailsClosed(t *testing.T) {
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	d, err := New(Options{
		StateDir: t.TempDir(), KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), Offline: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ws := t.TempDir()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "do real model work")

	d.runTask(sess, task)
	got, _ := d.sched.Get(task.RunID)
	if got.Status != "degraded" || !contains(got.Summary, "no available model provider") {
		t.Fatalf("task = %+v", got)
	}
}

func TestReasonerAuthenticationFailureIsFailedAndTerminalEventOwnsRouteTruth(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&failingReasoner{err: providerStatusError{provider: "openai", status: 401}})
	sess, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	task := d.sched.SubmitWithGoalModelAgent(
		sess.SessionID,
		sess.WorkspaceID,
		"inspect this repository",
		"openai/gpt-5.6-sol",
		"build",
		nil,
	)
	d.sched.SetModelState(task.RunID, "openai/gpt-5.6-sol", "openai/gpt-5.6-sol")
	d.sched.SetReasoningEffortState(task.RunID, "medium", "medium")
	d.sched.SetRetryOf(task.RunID, "run-original")
	if frozen, ok := d.sched.Get(task.RunID); ok {
		task = frozen
	}

	d.runTask(sess, task)

	got, _ := d.sched.Get(task.RunID)
	if got.Status != "failed" || got.Continuity.Outcome != continuity.OutcomeFailed {
		t.Fatalf("credential rejection must be a failed run, got %+v", got)
	}
	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var events []struct {
		TaskID  string         `json:"task_id"`
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type != "ExecutionFailed" || event.TaskID != task.RunID {
			continue
		}
		if event.Payload["outcome"] != "failed" ||
			event.Payload["reason_code"] != "provider_authentication_failed" ||
			event.Payload["error_category"] != "authentication" ||
			event.Payload["model"] != "openai/gpt-5.6-sol" ||
			event.Payload["requested_model"] != "openai/gpt-5.6-sol" ||
			event.Payload["retry_of_run_id"] != "run-original" ||
			event.Payload["same_route_retryable"] != false {
			t.Fatalf("terminal failure truth = %#v", event.Payload)
		}
		return
	}
	t.Fatalf("ExecutionFailed not found for %s: %s", task.RunID, raw)
}

func TestReasonerFailureOnlyDegradesForPatchOwnedByCurrentRun(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	d.SetReasoner(&failingReasoner{err: providerStatusError{provider: "openai", status: 401}})
	sess, _ := d.store.CreateSession(workspace, "full-workspace")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "full-workspace", nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "result.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	apply := func(runID, content string) string {
		t.Helper()
		patch, err := d.kern.PatchPropose(sess.SessionID, runID, "continuity ownership regression", []kernel.FileChange{{
			Path: "result.txt", NewContent: content,
		}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.kern.PatchApply(sess.SessionID, patch.PatchID, "test"); err != nil {
			t.Fatal(err)
		}
		return patch.PatchID
	}

	olderPatchID := apply("run-older", "older patch\n")
	withoutOwnPatch := d.sched.SubmitWithGoalModelAgent(
		sess.SessionID, sess.WorkspaceID, "fail after an older run changed the workspace", "openai/gpt-5.6-sol", "build", nil,
	)
	d.runTask(sess, withoutOwnPatch)
	failed, _ := d.sched.Get(withoutOwnPatch.RunID)
	if failed.Status != "failed" || failed.Continuity.Outcome != continuity.OutcomeFailed {
		t.Fatalf("an older run's patch must not imply partial completion: %+v", failed)
	}
	if len(failed.AppliedPatches) != 0 {
		t.Fatalf("failed run claimed another run's patch %s: %+v", olderPatchID, failed.AppliedPatches)
	}

	withOwnPatch := d.sched.SubmitWithGoalModelAgent(
		sess.SessionID, sess.WorkspaceID, "fail after this run changed the workspace", "openai/gpt-5.6-sol", "build", nil,
	)
	currentPatchID := apply(withOwnPatch.RunID, "current patch\n")
	d.runTask(sess, withOwnPatch)
	degraded, _ := d.sched.Get(withOwnPatch.RunID)
	if degraded.Status != "degraded" || degraded.Continuity.Outcome != continuity.OutcomePartial {
		t.Fatalf("a current-run patch must preserve partial completion: %+v", degraded)
	}
	if len(degraded.AppliedPatches) != 1 || degraded.AppliedPatches[0] != currentPatchID {
		t.Fatalf("degraded run patch attribution = %+v, want %s", degraded.AppliedPatches, currentPatchID)
	}
}

// TestAgentLoopBlocksDestructive proves the agent cannot run a destructive
// command even if the model asks for it.
func TestAgentLoopBlocksDestructive(t *testing.T) {
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "keep.txt"), []byte("important\n"), 0o600)

	d, err := New(Options{StateDir: t.TempDir(), KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "delete everything")

	// Directly exercise the tool executor with a destructive command.
	obs := d.agentRun(sess, task, []string{"rm", "-rf", ws})
	if !contains(obs, "DENIED") {
		t.Fatalf("destructive command should be denied, got: %s", obs)
	}
	if _, err := os.Stat(filepath.Join(ws, "keep.txt")); err != nil {
		t.Fatal("file must survive a denied rm -rf")
	}
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// helpers local to the internal package test.
func repoRootFromHere(t *testing.T) string {
	wd, _ := os.Getwd()
	return filepath.Dir(filepath.Dir(wd)) // go/daemon -> repo root
}

func firstExistingPath(paths ...string) string {
	for _, p := range paths {
		if p != "" {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

var _ = context.Background
var _ = time.Second
