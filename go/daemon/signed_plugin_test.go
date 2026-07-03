package daemon_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/TsekaLuk/pi-os/go/daemon"
	"github.com/TsekaLuk/pi-os/go/rpc"
)

// TestSignedPluginEnforcement verifies PRD §5 Phase 5: when the deployment
// trusts a publisher key, only plugins signed by it may run. Unsigned and
// wrongly-signed modules are refused.
func TestSignedPluginEnforcement(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("PI_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/pi-kernel-service"),
		filepath.Join(repoRoot, "target/debug/pi-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("pi-kernel-service not built")
	}
	wasm, err := os.ReadFile(filepath.Join(repoRoot, "examples/plugins/hello/hello.wasm"))
	if err != nil {
		t.Skip("example plugin not built")
	}
	manifest, err := os.ReadFile(filepath.Join(repoRoot, "examples/plugins/hello/plugin.toml"))
	if err != nil {
		t.Fatal(err)
	}

	// Trusted publisher keypair.
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	// An untrusted signer.
	_, rogue, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	stateDir := t.TempDir()
	ws := t.TempDir()
	policyDir := filepath.Join(stateDir, "policy")
	if err := os.MkdirAll(policyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Trust only `pub`.
	if err := os.WriteFile(filepath.Join(policyDir, "trusted-keys"),
		[]byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	d, err := daemon.New(daemon.Options{
		StateDir: stateDir, KernelBin: kernelBin,
		ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin"), PolicyDir: policyDir, Offline: true,
	})
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
	if err := c.Call("session.create", map[string]any{"workspace_root": ws, "profile": "ci-runner"}, &sess); err != nil {
		t.Fatal(err)
	}

	run := func(sig []byte) error {
		params := map[string]any{
			"session_id":    sess.SessionID,
			"manifest_toml": string(manifest),
			"wasm_base64":   base64.StdEncoding.EncodeToString(wasm),
		}
		if sig != nil {
			params["signature_base64"] = base64.StdEncoding.EncodeToString(sig)
		}
		return c.Call("plugin.run", params, &struct{}{})
	}

	// 1. Unsigned → refused.
	if err := run(nil); err == nil {
		t.Fatal("unsigned plugin should be refused when a key is trusted")
	}
	// 2. Signed by the rogue key → refused.
	if err := run(ed25519.Sign(rogue, wasm)); err == nil {
		t.Fatal("plugin signed by an untrusted key should be refused")
	}
	// 3. Signed by the trusted key → runs.
	if err := run(ed25519.Sign(priv, wasm)); err != nil {
		t.Fatalf("plugin signed by the trusted key should run: %v", err)
	}
}
