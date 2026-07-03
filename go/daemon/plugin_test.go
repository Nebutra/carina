package daemon_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/TsekaLuk/pi-os/go/daemon"
	"github.com/TsekaLuk/pi-os/go/rpc"
)

// TestPluginPermissionBoundary verifies PRD §8.7 through the full stack: a
// WASM plugin runs under the session policy, its declared capability is
// allowed, and an undeclared one is refused and recorded.
func TestPluginPermissionBoundary(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("PI_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/pi-kernel-service"),
		filepath.Join(repoRoot, "target/debug/pi-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("pi-kernel-service not built")
	}
	wasmPath := filepath.Join(repoRoot, "examples/plugins/hello/hello.wasm")
	wasm, err := os.ReadFile(wasmPath)
	if err != nil {
		t.Skip("example plugin not built (run pi-wat2wasm)")
	}
	manifest, err := os.ReadFile(filepath.Join(repoRoot, "examples/plugins/hello/plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	ws := t.TempDir()
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

	// ci-runner allows command_exec (the declared capability) but denies secrets.
	var sess struct {
		SessionID string `json:"session_id"`
	}
	if err := c.Call("session.create", map[string]any{"workspace_root": ws, "profile": "ci-runner"}, &sess); err != nil {
		t.Fatal(err)
	}

	// Inspect: permissions are visible before running.
	var inspect struct {
		Permissions []string `json:"permissions"`
	}
	if err := c.Call("plugin.inspect", map[string]any{"manifest_toml": string(manifest)}, &inspect); err != nil {
		t.Fatal(err)
	}
	if len(inspect.Permissions) == 0 {
		t.Fatal("expected declared permissions in inspect")
	}

	// Run the plugin.
	var outcome struct {
		ResultCode int `json:"result_code"`
		Decisions  []struct {
			Capability string `json:"capability"`
			Allowed    bool   `json:"allowed"`
			Reason     string `json:"reason"`
		} `json:"decisions"`
	}
	if err := c.Call("plugin.run", map[string]any{
		"session_id":    sess.SessionID,
		"manifest_toml": string(manifest),
		"wasm_base64":   base64.StdEncoding.EncodeToString(wasm),
	}, &outcome); err != nil {
		t.Fatalf("plugin.run: %v", err)
	}

	// Only the declared command_exec ran; the undeclared secret was refused.
	if outcome.ResultCode != 1 {
		t.Fatalf("expected result_code 1 (one allowed), got %d", outcome.ResultCode)
	}
	var sawAllowedCmd, sawDeniedSecret bool
	for _, dcn := range outcome.Decisions {
		if dcn.Capability == "command_exec" && dcn.Allowed {
			sawAllowedCmd = true
		}
		if dcn.Capability == "secret" && !dcn.Allowed {
			sawDeniedSecret = true
		}
	}
	if !sawAllowedCmd {
		t.Fatal("expected declared command_exec to be allowed")
	}
	if !sawDeniedSecret {
		t.Fatal("expected undeclared secret to be refused")
	}

	// The refusal is in the audit log as a PolicyViolation.
	var events json.RawMessage
	if err := c.Call("session.replay", map[string]any{"session_id": sess.SessionID}, &events); err != nil {
		t.Fatal(err)
	}
	if !containsAll(string(events), "PolicyViolation", "hello-plugin") {
		t.Fatal("expected plugin policy violation in the audit log")
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		found := false
		for i := 0; i+len(n) <= len(haystack); i++ {
			if haystack[i:i+len(n)] == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
