package daemon_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TsekaLuk/pi-os/go/daemon"
	"github.com/TsekaLuk/pi-os/go/rpc"
)

// TestAuditHashChainAndActors verifies PRD §9.2 (tamper-evident hash chain)
// and §4.1 (events tagged with the Go/Rust/Zig actor) through the full
// stack.
func TestAuditHashChainAndActors(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("PI_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/pi-kernel-service"),
		filepath.Join(repoRoot, "target/debug/pi-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("pi-kernel-service not built")
	}
	stateDir := t.TempDir()
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("hello\n"), 0o600)

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

	var sess struct {
		SessionID string `json:"session_id"`
	}
	if err := c.Call("session.create", map[string]any{"workspace_root": ws, "profile": "safe-edit"}, &sess); err != nil {
		t.Fatal(err)
	}
	// Generate a spread of events across actors.
	c.Call("task.submit", map[string]any{"session_id": sess.SessionID, "prompt": "hi"}, &struct{}{})
	c.Call("workspace.search", map[string]any{"session_id": sess.SessionID, "pattern": "hello"}, &struct{}{})
	c.Call("command.exec", map[string]any{"session_id": sess.SessionID, "argv": []string{"echo", "ok"}}, &struct{}{})
	// let the async task loop record its events
	waitFor(t, func() bool {
		var ev []map[string]any
		c.Call("session.replay", map[string]any{"session_id": sess.SessionID}, &ev)
		return len(ev) >= 4
	})

	// 1. Chain verifies while intact.
	var report struct {
		OK         bool   `json:"ok"`
		EventCount int    `json:"event_count"`
		BrokenAt   *int   `json:"broken_at"`
		HeadHash   string `json:"head_hash"`
	}
	if err := c.Call("audit.verify", map[string]any{"session_id": sess.SessionID}, &report); err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.EventCount == 0 {
		t.Fatalf("intact chain should verify, got %+v", report)
	}

	// 2. Events carry actor tags spanning go/rust/zig.
	var events []struct {
		Type  string `json:"type"`
		Actor string `json:"actor"`
	}
	if err := c.Call("session.replay", map[string]any{"session_id": sess.SessionID}, &events); err != nil {
		t.Fatal(err)
	}
	actors := map[string]bool{}
	for _, e := range events {
		actors[e.Actor] = true
	}
	for _, want := range []string{"go", "rust", "zig"} {
		if !actors[want] {
			t.Errorf("expected an event with actor=%q; saw actors %v", want, actors)
		}
	}

	// 3. Tamper with the on-disk log; verify must catch it.
	logPath := filepath.Join(stateDir, "events", sess.SessionID+".events.jsonl")
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatal("need at least two events to tamper")
	}
	var mid map[string]any
	json.Unmarshal([]byte(lines[1]), &mid)
	mid["payload"] = map[string]any{"tampered": true}
	tampered, _ := json.Marshal(mid)
	lines[1] = string(tampered)
	os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	if err := c.Call("audit.verify", map[string]any{"session_id": sess.SessionID}, &report); err != nil {
		t.Fatal(err)
	}
	if report.OK {
		t.Fatal("tampered chain must NOT verify")
	}
	if report.BrokenAt == nil || *report.BrokenAt != 1 {
		t.Fatalf("expected break at event 1, got broken_at=%v", report.BrokenAt)
	}
}
