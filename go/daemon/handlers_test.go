package daemon_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/daemon"
	"github.com/Nebutra/carina/go/rpc"
)

// TestDaemonHandlerSurface exercises the breadth of RPC handlers so the
// control-plane coverage reflects the full API (PRD §15).
func TestDaemonHandlerSurface(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	stateDir := t.TempDir()
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "a.go"), []byte("package p\n// TODO x\n"), 0o600)

	d, err := daemon.New(daemon.Options{StateDir: stateDir, KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)
	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	must := func(method string, params map[string]any) {
		if err := c.Call(method, params, nil); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}

	// daemon-level
	must("daemon.status", map[string]any{})
	must("daemon.metrics", map[string]any{})

	// worker lifecycle
	var reg struct {
		WorkerID string `json:"worker_id"`
	}
	if err := c.Call("worker.register", map[string]any{"name": "w", "kind": "ci"}, &reg); err != nil {
		t.Fatal(err)
	}
	must("worker.heartbeat", map[string]any{"worker_id": reg.WorkerID})
	must("worker.list", map[string]any{})
	must("worker.revoke", map[string]any{"worker_id": reg.WorkerID})

	// session + workspace
	var sess struct {
		SessionID string `json:"session_id"`
	}
	if err := c.Call("session.create", map[string]any{"workspace_root": ws, "profile": "safe-edit"}, &sess); err != nil {
		t.Fatal(err)
	}
	sid := sess.SessionID
	must("session.get", map[string]any{"session_id": sid})
	must("session.list", map[string]any{})
	must("workspace.tree", map[string]any{"session_id": sid})
	must("workspace.search", map[string]any{"session_id": sid, "pattern": "TODO"})
	must("workspace.file.get", map[string]any{"session_id": sid, "path": "a.go"})
	must("profile.describe", map[string]any{"session_id": sid})

	// patches
	var patch struct {
		PatchID string `json:"patch_id"`
	}
	if err := c.Call("workspace.patch.propose", map[string]any{
		"session_id": sid, "reason": "t", "files": []map[string]any{{"path": "a.go", "new_content": "package p\n"}},
	}, &patch); err != nil {
		t.Fatal(err)
	}
	must("workspace.patch.list", map[string]any{"session_id": sid})
	must("workspace.patch.show", map[string]any{"session_id": sid, "patch_id": patch.PatchID})
	must("workspace.patch.apply", map[string]any{"session_id": sid, "patch_id": patch.PatchID})
	must("workspace.patch.rollback", map[string]any{"session_id": sid, "patch_id": patch.PatchID})

	// task lifecycle
	var task struct {
		TaskID string `json:"task_id"`
	}
	if err := c.Call("task.submit", map[string]any{"session_id": sid, "prompt": "hi"}, &task); err != nil {
		t.Fatal(err)
	}
	must("task.status", map[string]any{"task_id": task.TaskID})
	must("task.cancel", map[string]any{"task_id": task.TaskID})

	// secrets + audit
	must("secret.grant", map[string]any{"session_id": sid, "name": "K", "value": "v"})
	must("secret.request", map[string]any{"session_id": sid, "name": "K"})
	must("session.items", map[string]any{"session_id": sid})
	must("audit.report", map[string]any{"session_id": sid})
	must("audit.export", map[string]any{"session_id": sid})
	must("audit.verify", map[string]any{"session_id": sid})

	// command approval flow: risk-2 install -> approve -> executes
	var exec struct {
		Decision struct {
			Decision   string `json:"decision"`
			DecisionID string `json:"decision_id"`
		} `json:"decision"`
	}
	if err := c.Call("command.exec", map[string]any{"session_id": sid, "argv": []string{"npm", "install", "x"}}, &exec); err != nil {
		t.Fatal(err)
	}
	if exec.Decision.Decision == "requires_approval" {
		must("task.action.approve", map[string]any{"session_id": sid, "decision_id": exec.Decision.DecisionID})
	}
	// deny path
	var exec2 struct {
		Decision struct {
			Decision   string `json:"decision"`
			DecisionID string `json:"decision_id"`
		} `json:"decision"`
	}
	c.Call("command.exec", map[string]any{"session_id": sid, "argv": []string{"pip", "install", "y"}}, &exec2)
	if exec2.Decision.DecisionID != "" {
		must("task.action.deny", map[string]any{"session_id": sid, "decision_id": exec2.Decision.DecisionID, "reason": "no"})
	}

	must("session.close", map[string]any{"session_id": sid})

	// Error paths: unknown session / missing params.
	if err := c.Call("session.get", map[string]any{"session_id": "sess_missing"}, nil); err == nil {
		t.Fatal("unknown session should error")
	}
	if err := c.Call("session.create", map[string]any{"profile": "safe-edit"}, nil); err == nil {
		t.Fatal("missing workspace_root should error")
	}
	if err := c.Call("command.exec", map[string]any{"session_id": "sess_missing", "argv": []string{"ls"}}, nil); err == nil {
		t.Fatal("exec on unknown session should error")
	}
}

// TestDaemonEventStream covers the streaming subscription handler and the
// event bus fan-out (PRD §8.6).
func TestDaemonEventStream(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	d, err := daemon.New(daemon.Options{StateDir: t.TempDir(), KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)

	c, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var sess struct {
		SessionID string `json:"session_id"`
	}
	c.Call("session.create", map[string]any{"workspace_root": t.TempDir(), "profile": "safe-edit"}, &sess)

	sub, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := sub.Call("session.events.stream", map[string]any{"session_id": sess.SessionID}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	got := make(chan string, 4)
	go func() {
		for {
			m, _, err := sub.ReadNotification()
			if err != nil {
				return
			}
			got <- m
		}
	}()
	c.Call("task.submit", map[string]any{"session_id": sess.SessionID, "prompt": "go"}, &struct{}{})
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no event streamed")
	}
}
