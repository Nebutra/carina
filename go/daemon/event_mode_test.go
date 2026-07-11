package daemon

import (
	"encoding/json"
	"testing"
)

func TestCanonicalEventModeFiltersOnlyDuplicateLifecycle(t *testing.T) {
	for _, typ := range []string{"ToolRequested", "ToolApproved", "ToolDenied"} {
		if _, ok := projectEvent(eventModeCanonical, map[string]any{"type": typ}); ok {
			t.Fatalf("%s retained", typ)
		}
	}
	for _, typ := range []string{"ToolCallRequested", "CommandStarted", "PatchApplied", "PolicyViolation"} {
		if _, ok := projectEvent(eventModeCanonical, map[string]any{"type": typ}); !ok {
			t.Fatalf("%s filtered", typ)
		}
	}
}

func TestSessionAttachCanonicalAdvancesRawCursor(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	d.record(sess.SessionID, "ToolRequested", "t", "go", map[string]any{"tool": "run"}, "")
	d.record(sess.SessionID, "ToolCallRequested", "t", "go", map[string]any{"call_id": "c", "tool": "run", "kind": "command", "status": "requested", "arguments": map[string]any{}}, "")
	raw, err := d.handleSessionAttach(json.RawMessage(`{"session_id":"` + sess.SessionID + `","event_mode":"canonical"}`))
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	if out["cursor"].(int) != 2 {
		t.Fatalf("cursor = %v", out["cursor"])
	}
	if len(out["events"].([]any)) != 1 {
		t.Fatalf("events = %#v", out["events"])
	}
	event := out["events"].([]any)[0].(map[string]any)
	if event["raw_cursor"] != 2 {
		t.Fatalf("raw_cursor = %#v", event["raw_cursor"])
	}
}

func TestCanonicalCursorSurvivesFilteredEventsAndReconnect(t *testing.T) {
	legacy := map[string]any{"type": "ToolRequested", internalRawAuditCursor: 11}
	if _, ok := projectEvent(eventModeCanonical, legacy); ok {
		t.Fatal("compatibility-only event was delivered")
	}
	canonical := map[string]any{"type": "ToolCallRequested", internalRawAuditCursor: 12}
	projected, ok := projectEvent(eventModeCanonical, canonical)
	if !ok || projected.(map[string]any)["raw_cursor"] != 12 {
		t.Fatalf("live cursor = %#v, delivered=%v", projected, ok)
	}
	replayed, ok := projectEvent(eventModeCanonical, json.RawMessage(`{"type":"ToolCallCompleted"}`), 15)
	if !ok || replayed.(map[string]any)["raw_cursor"] != 15 {
		t.Fatalf("replay cursor = %#v, delivered=%v", replayed, ok)
	}
	compat, ok := projectEvent(eventModeCompat, canonical)
	if !ok {
		t.Fatal("compat event filtered")
	}
	if _, leaked := compat.(map[string]any)[internalRawAuditCursor]; leaked {
		t.Fatal("internal cursor leaked into compat event")
	}
	if _, changed := compat.(map[string]any)["raw_cursor"]; changed {
		t.Fatal("compat notification shape changed")
	}
}
