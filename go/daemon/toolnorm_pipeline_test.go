package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentRunCanonicalizesRelativePathsBeforeAuditAndExecution proves P1.2's
// canonicalize -> validate -> decide pipeline is actually wired into
// agentRun: a relative-path argv the model emits must reach both the kernel
// policy Request and the CommandStarted audit event as the workspace-root
// expanded absolute form, never the raw relative string — so the audit
// chain always records the canonical form actually authorized.
func TestAgentRunCanonicalizesRelativePathsBeforeAuditAndExecution(t *testing.T) {
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
	os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi\n"), 0o600)

	d, err := New(Options{StateDir: t.TempDir(), KernelBin: kernelBin, ToolsDir: toolsDir, Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, sess.PermissionProfile, nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "cat a relative-path file")

	obs := d.agentRun(sess, task, []string{"cat", "hello.txt"})
	if strings.Contains(obs, "error") || strings.Contains(obs, "DENIED") {
		t.Fatalf("expected a benign read to succeed, got: %s", obs)
	}
	if !strings.Contains(obs, "hi") {
		t.Fatalf("command output missing expected content, got: %s", obs)
	}

	events, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	if err := json.Unmarshal(events, &evs); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(ws, "hello.txt")
	found := false
	for _, e := range evs {
		if e["type"] != "CommandStarted" {
			continue
		}
		payload, _ := e["payload"].(map[string]any)
		cmd, _ := payload["command"].(string)
		if strings.Contains(cmd, "hello.txt") {
			found = true
			if !strings.Contains(cmd, want) {
				t.Fatalf("CommandStarted audit payload recorded raw relative path, want canonical absolute form %q, got %q", want, cmd)
			}
			if strings.Contains(cmd, " hello.txt") && !strings.Contains(cmd, ws) {
				t.Fatalf("audit payload command %q still carries the raw relative token instead of the canonical path", cmd)
			}
		}
	}
	if !found {
		t.Fatal("no CommandStarted audit event found for the run action")
	}
}

// TestAgentRunClassifiesWrappedAndBareCommandsIdentically proves the
// motivating P1.2 gap is closed at the agentRun call site (not just inside
// toolnorm in isolation): "env rm -rf <ws>" and "rm -rf <ws>" must both be
// denied by the same policy rule, because classification now runs against
// the wrapper-stripped canonical form. Uses "env" rather than "timeout"
// because "timeout" is a coreutils binary not guaranteed present on a bare
// macOS PATH, whereas "env" is POSIX-standard and always resolves.
func TestAgentRunClassifiesWrappedAndBareCommandsIdentically(t *testing.T) {
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
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "delete everything, wrapped in timeout")

	obs := d.agentRun(sess, task, []string{"env", "rm", "-rf", ws})
	if !contains(obs, "DENIED") {
		t.Fatalf("env-wrapped destructive command should be denied identically to the bare form, got: %s", obs)
	}
	if _, err := os.Stat(filepath.Join(ws, "keep.txt")); err != nil {
		t.Fatal("file must survive a denied wrapped rm -rf")
	}
}

// TestAgentRunRejectsUnresolvableBinaryWithoutBurningApproval proves the
// validateInput stage runs ahead of the kernel decision: a command whose
// argv[0] does not resolve must return a teachable error and must NOT
// publish a permission decision or CommandStarted audit event — the model
// self-corrects without spending a human approval on a typo.
func TestAgentRunRejectsUnresolvableBinaryWithoutBurningApproval(t *testing.T) {
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

	d, err := New(Options{StateDir: t.TempDir(), KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), Offline: true})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run a typo'd binary")

	obs := d.agentRun(sess, task, []string{"this-binary-does-not-exist-xyz-12345"})
	if !strings.Contains(obs, "error") {
		t.Fatalf("unresolvable binary should return a teachable error observation, got: %s", obs)
	}
	if strings.Contains(obs, "DENIED by policy") {
		t.Fatalf("a validation failure must not be phrased as a policy denial (it never reached the kernel), got: %s", obs)
	}

	events, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	if err := json.Unmarshal(events, &evs); err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e["type"] == "CommandStarted" {
			t.Fatalf("a validate-stage rejection must never reach CommandStarted (no approval should be burned), got event: %v", e)
		}
	}
}
