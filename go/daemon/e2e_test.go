package daemon_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/daemon"
	"github.com/Nebutra/carina/go/rpc"
)

// TestEndToEndLoop exercises the PRD §18 MVP loop across all three
// languages: CLI-style RPC → Go control plane → Rust capability kernel →
// Zig native toolchain, then patch apply/rollback and audit.
//
// It skips gracefully if the kernel or tools have not been built, so
// `go test` stays green on a Go-only checkout; CI builds all three first.
func TestEndToEndLoop(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built; run `cargo build` first")
	}
	toolsDir := filepath.Join(repoRoot, "zig/zig-out/bin")
	if _, err := os.Stat(filepath.Join(toolsDir, "carina-scan")); err != nil {
		t.Skip("zig tools not built; run `zig build` first")
	}

	stateDir := t.TempDir()
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello world\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := daemon.New(daemon.Options{StateDir: stateDir, KernelBin: kernelBin, ToolsDir: toolsDir})
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
	}
	defer d.Close()

	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)

	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// 1. Create session.
	var sess struct {
		SessionID string `json:"session_id"`
	}
	if err := c.Call("session.create", map[string]any{"workspace_root": ws, "profile": "safe-edit"}, &sess); err != nil {
		t.Fatalf("session.create: %v", err)
	}

	// 2. FileRead outside workspace must be denied by the Rust kernel.
	var denied struct {
		Decision string `json:"decision"`
	}
	if err := c.Call("workspace.file.get", map[string]any{"session_id": sess.SessionID, "path": "/etc/passwd"}, &denied); err == nil {
		t.Fatal("expected out-of-workspace read to fail")
	}

	// 3. Workspace search via Zig carina-grep.
	var matches []struct {
		File string `json:"file"`
		Line int    `json:"line"`
	}
	if err := c.Call("workspace.search", map[string]any{"session_id": sess.SessionID, "pattern": "hello"}, &matches); err != nil {
		t.Fatalf("workspace.search: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("expected carina-grep to find 'hello'")
	}

	// 4. Propose + apply a patch (Rust transactional engine writes the file).
	var patch struct {
		PatchID string `json:"patch_id"`
		Status  string `json:"status"`
	}
	if err := c.Call("workspace.patch.propose", map[string]any{
		"session_id": sess.SessionID,
		"reason":     "e2e",
		"files":      []map[string]any{{"path": "a.txt", "new_content": "patched!\n"}},
	}, &patch); err != nil {
		t.Fatalf("patch.propose: %v", err)
	}
	if err := c.Call("workspace.patch.apply", map[string]any{"session_id": sess.SessionID, "patch_id": patch.PatchID}, &patch); err != nil {
		t.Fatalf("patch.apply: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "a.txt")); string(got) != "patched!\n" {
		t.Fatalf("patch not applied: got %q", got)
	}

	// 5. Rollback restores the pre-image.
	if err := c.Call("workspace.patch.rollback", map[string]any{"session_id": sess.SessionID, "patch_id": patch.PatchID}, &patch); err != nil {
		t.Fatalf("patch.rollback: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(ws, "a.txt")); string(got) != "hello world\n" {
		t.Fatalf("rollback did not restore: got %q", got)
	}

	// 6. Command execution: a safe test-class command is allowed and runs
	// through Zig carina-run.
	var exec1 struct {
		Decision struct {
			Decision string `json:"decision"`
		} `json:"decision"`
		Result struct {
			ExitCode int `json:"exit_code"`
		} `json:"result"`
	}
	if err := c.Call("command.exec", map[string]any{"session_id": sess.SessionID, "argv": []string{"echo", "hi"}}, &exec1); err != nil {
		t.Fatalf("command.exec echo: %v", err)
	}

	// 7. A destructive command must be denied.
	var exec2 struct {
		Decision struct {
			Decision string `json:"decision"`
		} `json:"decision"`
	}
	if err := c.Call("command.exec", map[string]any{"session_id": sess.SessionID, "argv": []string{"rm", "-rf", "/"}}, &exec2); err != nil {
		t.Fatalf("command.exec rm: %v", err)
	}
	if exec2.Decision.Decision != "denied" {
		t.Fatalf("expected rm -rf to be denied, got %q", exec2.Decision.Decision)
	}

	// 8. Audit report reflects the policy violation and command activity.
	var report struct {
		TotalEvents      int             `json:"total_events"`
		PolicyViolations []json.RawMessage `json:"policy_violations"`
	}
	if err := c.Call("audit.report", map[string]any{"session_id": sess.SessionID}, &report); err != nil {
		t.Fatalf("audit.report: %v", err)
	}
	if report.TotalEvents == 0 {
		t.Fatal("expected a non-empty audit trail")
	}
	if len(report.PolicyViolations) == 0 {
		t.Fatal("expected the denied read/command to appear as a policy violation")
	}
}

func repoRoot(t *testing.T) string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve caller")
	}
	// go/daemon/e2e_test.go -> repo root is two levels up.
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("socket %s never appeared", path)
}

// waitFor polls cond up to ~3s; fails the test if it never becomes true.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 150; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

// shortSocket returns a socket path under /tmp short enough for the
// platform's sockaddr_un limit (~104 bytes on macOS), regardless of the
// test name length that t.TempDir() would otherwise inject.
func shortSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pios")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "d.sock")
}

var _ = exec.Command // reserved for future worker tests
