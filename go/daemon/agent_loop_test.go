package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// scripted reasoner from reasoner.go drives the loop deterministically.

// TestAgentLoopExecutesThroughKernel proves the ReAct loop actually edits a
// file and runs a command — all mediated by the kernel — using a scripted
// reasoner (no model, no cost). This is the mechanism test for "pi-os as a
// real coding agent".
func TestAgentLoopExecutesThroughKernel(t *testing.T) {
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("PI_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/pi-kernel-service"),
		filepath.Join(repoRoot, "target/debug/pi-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("pi-kernel-service not built")
	}
	toolsDir := filepath.Join(repoRoot, "zig/zig-out/bin")
	if _, err := os.Stat(filepath.Join(toolsDir, "pi-scan")); err != nil {
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
		`{"thought":"edit","action":{"tool":"patch","path":"hello.txt","content":"hello\n// added by the pi-os agent\n"}}`,
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
	if string(got) != "hello\n// added by the pi-os agent\n" {
		t.Fatalf("file not edited by agent: %q", got)
	}

	// The task completed.
	if tk, _ := d.sched.Get(task.TaskID); tk.Status != "completed" {
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
	for _, e := range evs {
		if s, ok := e["type"].(string); ok {
			types[s] = true
		}
	}
	for _, want := range []string{"FileRead", "PatchProposed", "PatchApplied", "CommandStarted", "CommandExited"} {
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

// TestAgentLoopBlocksDestructive proves the agent cannot run a destructive
// command even if the model asks for it.
func TestAgentLoopBlocksDestructive(t *testing.T) {
	repoRoot := repoRootFromHere(t)
	kernelBin := firstExistingPath(
		os.Getenv("PI_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/pi-kernel-service"),
		filepath.Join(repoRoot, "target/debug/pi-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("pi-kernel-service not built")
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
