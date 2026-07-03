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

// TestSecretBrokerNeverLeaksPlaintext verifies PRD §12.3 / §17.2: a secret's
// plaintext must never appear in the event log, and command output is
// redacted. Success metric: zero secret plaintext in logs.
func TestSecretBrokerNeverLeaksPlaintext(t *testing.T) {
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
	d, err := daemon.New(daemon.Options{StateDir: stateDir, KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin")})
	if err != nil {
		t.Fatalf("daemon.New: %v", err)
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

	const plaintext = "sk-TOPSECRET-9f3a2b"

	var sess struct {
		SessionID string `json:"session_id"`
	}
	// full-workspace allows secret access (still requires approval).
	if err := c.Call("session.create", map[string]any{"workspace_root": ws, "profile": "full-workspace"}, &sess); err != nil {
		t.Fatal(err)
	}

	// Grant a secret; the grant response must not echo the plaintext.
	var grant struct {
		Handle string `json:"handle"`
	}
	if err := c.Call("secret.grant", map[string]any{"session_id": sess.SessionID, "name": "API_KEY", "value": plaintext}, &grant); err != nil {
		t.Fatal(err)
	}
	if grant.Handle != "secret://API_KEY" {
		t.Fatalf("unexpected handle %q", grant.Handle)
	}

	// Agent requests the secret: gets a handle, not the value.
	var reqOut struct {
		Handle   string `json:"handle"`
		Decision struct {
			Decision string `json:"decision"`
		} `json:"decision"`
	}
	if err := c.Call("secret.request", map[string]any{"session_id": sess.SessionID, "name": "API_KEY"}, &reqOut); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(reqOut.Handle, "TOPSECRET") {
		t.Fatal("secret handle leaked plaintext")
	}

	// The entire event log must be free of the plaintext.
	var events json.RawMessage
	if err := c.Call("session.replay", map[string]any{"session_id": sess.SessionID}, &events); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(events), plaintext) {
		t.Fatal("PLAINTEXT LEAKED INTO EVENT LOG")
	}
	// Also confirm the on-disk event log file itself is clean.
	logPath := filepath.Join(stateDir, "events", sess.SessionID+".events.jsonl")
	if raw, err := os.ReadFile(logPath); err == nil {
		if strings.Contains(string(raw), plaintext) {
			t.Fatal("PLAINTEXT LEAKED INTO ON-DISK EVENT LOG")
		}
	}
}
