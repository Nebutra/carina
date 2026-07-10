package daemon

import (
	"testing"

	"github.com/Nebutra/carina/go/config"
)

func TestApplyConfigHotReload(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	if err := d.ApplyConfig(config.Config{
		MaxTaskTokens:         999,
		InteractiveApproval:   true,
		EnableDebugRPC:        true,
		RequireWorkspaceTrust: true,
		SandboxCommands:       true,
		EgressAllow:           []string{"example.com"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if d.maxTaskTokens.Load() != 999 {
		t.Errorf("max tokens not applied: %d", d.maxTaskTokens.Load())
	}
	if !d.interactiveApproval.Load() {
		t.Error("interactive approval not applied")
	}
	if !d.debugRPCEnabled.Load() {
		t.Error("debug rpc not applied")
	}
	if !d.requireTrust.Load() {
		t.Error("require trust not applied")
	}
	if !d.sandbox.Load() {
		t.Error("sandbox not applied")
	}
}

func TestApplyConfigRejectsInvalidKeepsLastGood(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	d.maxTaskTokens.Store(50)
	if err := d.ApplyConfig(config.Config{MaxTaskTokens: -1}); err == nil {
		t.Fatal("an invalid config must be rejected")
	}
	if d.maxTaskTokens.Load() != 50 {
		t.Fatalf("a rejected reload must keep the last-good value, got %d", d.maxTaskTokens.Load())
	}
}

func TestHandleReloadInvokesClosure(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	// No reloader installed yet: handleReload errors rather than panicking.
	if _, err := d.handleReload(nil); err == nil {
		t.Fatal("handleReload without a reloader must error")
	}
	called := false
	d.SetReloader(func() error { called = true; return nil })
	if _, err := d.handleReload(nil); err != nil {
		t.Fatalf("handleReload: %v", err)
	}
	if !called {
		t.Fatal("reload closure was not invoked")
	}
}
