package daemon

import (
	"strings"
	"testing"
)

func TestHITLPresetsAreLabelsNotNewPower(t *testing.T) {
	catalog := hitlPresetCatalog()
	if len(catalog) != 3 {
		t.Fatalf("catalog = %#v", catalog)
	}
	joined := ""
	for _, preset := range catalog {
		joined += preset.ID + " "
		if preset.ID == "yolo" || preset.ID == approvalModeAlwaysApprove || preset.ProductMode == approvalModeAlwaysApprove {
			t.Fatalf("yolo/always-approve must not be a named preset: %#v", preset)
		}
		if preset.ProductMode != approvalModeAsk && preset.ProductMode != approvalModeAcceptEdits {
			t.Fatalf("preset must compose existing product modes: %#v", preset)
		}
	}
	if !strings.Contains(joined, "read-only") || !strings.Contains(joined, "agent") || !strings.Contains(joined, "accept-edits") {
		t.Fatalf("missing named presets: %s", joined)
	}

	got, ok := resolveHITLPreset("read-only")
	if !ok || got.ProductMode != approvalModeAsk || got.Profile != "read-only" {
		t.Fatalf("read-only = %#v ok=%v", got, ok)
	}
	got, ok = resolveHITLPreset("agent")
	if !ok || got.ProductMode != approvalModeAsk || got.Profile != "safe-edit" {
		t.Fatalf("agent = %#v ok=%v", got, ok)
	}
	if _, ok := resolveHITLPreset("yolo"); ok {
		t.Fatal("yolo must stay an explicit always-approve alias, not a preset")
	}
	if _, ok := resolveHITLPreset("always-approve"); ok {
		t.Fatal("always-approve must stay an explicit verb")
	}
	if _, ok := resolveHITLPreset("never"); ok {
		t.Fatal("session/kernel never must not resolve as a preset")
	}
}

func TestHITLPresetRPCSetsProductModeOnly(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.handleSetInteractiveApproval(mustJSON(t, map[string]any{"mode": "agent", "session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["approval_mode"] != approvalModeAsk || res["preset"] != hitlPresetAgent {
		t.Fatalf("agent preset result = %#v", res)
	}
	current, ok := d.store.Get(sess.SessionID)
	if !ok || current.PermissionProfile != "safe-edit" {
		t.Fatalf("preset must not mutate session profile: %#v", current)
	}

	out, err = d.handleSetInteractiveApproval(mustJSON(t, map[string]any{"mode": "read-only"}))
	if err != nil {
		t.Fatal(err)
	}
	res = out.(map[string]any)
	if res["approval_mode"] != approvalModeAsk || res["preset"] != hitlPresetReadOnly {
		t.Fatalf("read-only preset result = %#v", res)
	}
	current, _ = d.store.Get(sess.SessionID)
	if current.PermissionProfile != "safe-edit" {
		t.Fatalf("read-only preset must not rewrite profile to read-only: %q", current.PermissionProfile)
	}

	inv, err := d.handleConfigInventory(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	cfg := inv.(map[string]any)
	if cfg["hitl_preset"] != hitlPresetAgent {
		t.Fatalf("ask + safe-edit should match agent: %#v", cfg)
	}
	presets, _ := cfg["hitl_presets"].([]hitlPreset)
	if len(presets) != 3 {
		t.Fatalf("inventory presets = %#v", cfg["hitl_presets"])
	}
	encoded := string(mustJSON(t, cfg))
	if strings.Contains(encoded, `"id":"yolo"`) || strings.Contains(encoded, `"id":"always-approve"`) {
		t.Fatalf("inventory leaked a yolo preset: %s", encoded)
	}

	d.SetDisableAlwaysApprove(true)
	if _, err := d.handleSetInteractiveApproval(mustJSON(t, map[string]any{"mode": "agent"})); err != nil {
		t.Fatalf("agent preset must still work under org lock: %v", err)
	}
	if err := d.SetApprovalMode(approvalModeAlwaysApprove); err == nil {
		t.Fatal("always-approve must stay forbidden under org lock")
	}
}

func TestMatchHITLPreset(t *testing.T) {
	if got := matchHITLPreset(approvalModeAsk, "read-only"); got != hitlPresetReadOnly {
		t.Fatalf("read-only match = %q", got)
	}
	if got := matchHITLPreset(approvalModeAsk, "safe-edit"); got != hitlPresetAgent {
		t.Fatalf("agent match = %q", got)
	}
	if got := matchHITLPreset(approvalModeAcceptEdits, "safe-edit"); got != hitlPresetAcceptEdits {
		t.Fatalf("accept-edits match = %q", got)
	}
	if got := matchHITLPreset(approvalModeAlwaysApprove, "safe-edit"); got != "" {
		t.Fatalf("always-approve must not match a cycle preset: %q", got)
	}
	if got := matchHITLPreset(approvalModeDontAsk, "safe-edit"); got != "" {
		t.Fatalf("dont-ask is not a named preset: %q", got)
	}
}
