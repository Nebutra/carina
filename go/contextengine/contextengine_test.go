package contextengine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeConfigDefaults(t *testing.T) {
	cfg, err := NormalizeConfig(Config{CarinaStateDir: "/tmp/carina-state"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ContextEngine != ModeAuto {
		t.Fatalf("context engine default = %q", cfg.ContextEngine)
	}
	if cfg.HeadroomMode != HeadroomModeManagedMCP {
		t.Fatalf("headroom mode default = %q", cfg.HeadroomMode)
	}
	if cfg.HeadroomStateDir != filepath.Join("/tmp/carina-state", "headroom") {
		t.Fatalf("headroom state dir = %q", cfg.HeadroomStateDir)
	}
	if cfg.HeadroomTokenBudget != 4000 {
		t.Fatalf("token budget = %d", cfg.HeadroomTokenBudget)
	}
}

func TestNormalizeConfigRejectsInvalidModes(t *testing.T) {
	if _, err := NormalizeConfig(Config{ContextEngine: "always"}); err == nil {
		t.Fatal("invalid context engine should fail")
	}
	if _, err := NormalizeConfig(Config{HeadroomMode: "http"}); err == nil {
		t.Fatal("invalid headroom mode should fail")
	}
	if _, err := NormalizeConfig(Config{HeadroomProxyPort: -1}); err == nil {
		t.Fatal("negative proxy port should fail")
	}
	if _, err := NormalizeConfig(Config{HeadroomTokenBudget: -1}); err == nil {
		t.Fatal("negative token budget should fail")
	}
}

func TestHeadroomRequiredErrorsWhenBinaryMissing(t *testing.T) {
	_, err := New(Config{ContextEngine: ModeHeadroom, HeadroomBin: filepath.Join(t.TempDir(), "missing")})
	if err == nil {
		t.Fatal("required headroom mode should fail when the binary is missing")
	}
}

func TestHeadroomAutoFallsBackToNoopWhenBinaryMissing(t *testing.T) {
	m, err := New(Config{ContextEngine: ModeAuto, HeadroomBin: filepath.Join(t.TempDir(), "missing")})
	if err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.EffectiveEngine != ModeNoop || st.HeadroomAvailable {
		t.Fatalf("unexpected fallback status: %+v", st)
	}
}

func TestHeadroomAutoDoesNotUsePathBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "headroom")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	m, err := New(Config{ContextEngine: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.EffectiveEngine != ModeNoop || st.HeadroomSource != "path" {
		t.Fatalf("auto should not enable PATH-only headroom: %+v", st)
	}
}

func TestHeadroomBinaryDiscovery(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "headroom")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{ContextEngine: ModeHeadroom, HeadroomBin: bin})
	if err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if !st.HeadroomAvailable || st.EffectiveEngine != ModeHeadroom || st.HeadroomBin == "" {
		t.Fatalf("unexpected status: %+v", st)
	}
	if st.HeadroomSource != "configured" {
		t.Fatalf("headroom source = %q", st.HeadroomSource)
	}
}

func TestHeadroomAutoUsesConfiguredBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "headroom")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "state")
	m, err := New(Config{ContextEngine: ModeAuto, HeadroomBin: bin, CarinaStateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	st := m.Status()
	if st.EffectiveEngine != ModeHeadroom || st.HeadroomSource != "configured" {
		t.Fatalf("auto should enable configured headroom: %+v", st)
	}
	if _, err := os.Stat(st.HeadroomStateDir); err != nil {
		t.Fatalf("headroom state dir should be prepared: %v", err)
	}
}

func TestManagedMCPServerSpec(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "headroom")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(t.TempDir(), "hr")
	m, err := New(Config{
		ContextEngine:     ModeHeadroom,
		HeadroomBin:       bin,
		HeadroomMode:      HeadroomModeManagedMCP,
		HeadroomStateDir:  stateDir,
		HeadroomProxyPort: 8787,
	})
	if err != nil {
		t.Fatal(err)
	}
	name, srv, ok := m.ManagedMCPServer()
	if !ok || name != ManagedMCPServerName {
		t.Fatalf("managed MCP should be enabled: name=%q ok=%v", name, ok)
	}
	if srv.Command != bin {
		t.Fatalf("command = %q", srv.Command)
	}
	wantArgs := []string{"mcp", "serve", "--proxy-url", "http://127.0.0.1:8787"}
	if len(srv.Args) != len(wantArgs) {
		t.Fatalf("args = %#v", srv.Args)
	}
	for i := range wantArgs {
		if srv.Args[i] != wantArgs[i] {
			t.Fatalf("args = %#v", srv.Args)
		}
	}
	if srv.Env["HEADROOM_WORKSPACE_DIR"] != stateDir || srv.Env["HEADROOM_MCP_READ"] != "0" {
		t.Fatalf("unexpected env: %#v", srv.Env)
	}
}

func TestManagedMCPConnectionStatus(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "headroom")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{ContextEngine: ModeHeadroom, HeadroomBin: bin})
	if err != nil {
		t.Fatal(err)
	}
	m.MarkManagedMCPConnected(nil)
	st := m.Status()
	if !st.ManagedMCPConnected || st.Phase != PhaseManagedMCP || st.ManagedMCPServer != "headroom" {
		t.Fatalf("unexpected connected status: %+v", st)
	}
	if doc := m.Doctor(); doc["ok"] != true {
		t.Fatalf("doctor should pass after MCP connect: %+v", doc)
	}
	m.MarkManagedMCPConnected(os.ErrNotExist)
	st = m.Status()
	if st.ManagedMCPConnected || st.Phase != PhaseFailed {
		t.Fatalf("unexpected failed status: %+v", st)
	}
}

func TestCompressIsNoopInDiscoveryPhase(t *testing.T) {
	m, err := New(Config{ContextEngine: ModeNoop})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Compress(context.Background(), CompressRequest{Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello" || res.OriginalBytes != 5 || res.CompressedBytes != 5 || res.Ratio != 1 {
		t.Fatalf("unexpected compression result: %+v", res)
	}
	st, err := m.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.CompressionCalls != 1 || st.RetrievalCalls != 0 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}
