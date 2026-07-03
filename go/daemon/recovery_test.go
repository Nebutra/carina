package daemon_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/TsekaLuk/pi-os/go/daemon"
	"github.com/TsekaLuk/pi-os/go/rpc"
)

// TestDaemonRecoversSessions verifies PRD §17.3: after the daemon exits, a
// new daemon on the same state directory recovers the previously active
// session and its event history.
func TestDaemonRecoversSessions(t *testing.T) {
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
	toolsDir := filepath.Join(repoRoot, "zig/zig-out/bin")

	// First daemon: create a session and submit a task.
	var sessionID string
	func() {
		d, err := daemon.New(daemon.Options{StateDir: stateDir, KernelBin: kernelBin, ToolsDir: toolsDir})
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
		sessionID = sess.SessionID
		if err := c.Call("task.submit", map[string]any{"session_id": sessionID, "prompt": "hello"}, &struct{}{}); err != nil {
			t.Fatal(err)
		}
	}()

	// Second daemon on the same state dir must recover the session.
	d2, err := daemon.New(daemon.Options{StateDir: stateDir, KernelBin: kernelBin, ToolsDir: toolsDir})
	if err != nil {
		t.Fatal(err)
	}
	defer d2.Close()
	sock2 := shortSocket(t)
	go func() { _ = d2.Run(sock2) }()
	waitForSocket(t, sock2)
	c2, err := rpc.Dial(sock2)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	var recovered struct {
		SessionID string `json:"session_id"`
		Status    string `json:"status"`
	}
	if err := c2.Call("session.get", map[string]any{"session_id": sessionID}, &recovered); err != nil {
		t.Fatalf("recovered session not found: %v", err)
	}
	if recovered.SessionID != sessionID {
		t.Fatalf("recovered wrong session: %s", recovered.SessionID)
	}

	// The event history from before the restart must still be replayable,
	// and the recovered session must accept new work.
	var events []map[string]any
	if err := c2.Call("session.replay", map[string]any{"session_id": sessionID}, &events); err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected recovered event history")
	}
	if err := c2.Call("task.submit", map[string]any{"session_id": sessionID, "prompt": "continue"}, &struct{}{}); err != nil {
		t.Fatalf("recovered session should accept new tasks: %v", err)
	}
}
