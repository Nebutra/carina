package daemon

import (
	"encoding/json"
	"testing"
)

func TestCanonicalEventModeFiltersOnlyDuplicateLifecycle(t *testing.T) {
	for _, typ := range []string{"ToolRequested", "ToolApproved", "ToolDenied", "execution.completed"} {
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

func TestCanonicalEventModePreservesDelegatedTaskIdentity(t *testing.T) {
	projected, ok := projectEvent(eventModeCanonical, map[string]any{
		"event_id": "evt_task",
		"type":     "TaskCompleted",
		"task_id":  "task_1",
	})
	if !ok {
		t.Fatal("delegated task lifecycle was filtered")
	}
	event := projected.(map[string]any)
	if event["task_id"] != "task_1" || event["run_id"] != nil {
		t.Fatalf("delegated task identity = %#v", event)
	}
}

func TestCanonicalEventModeProjectsDurableGovernanceRequests(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		eventType string
		idKey     string
		id        string
	}{
		{name: "approval", status: "permission_requested", eventType: "permission.request", idKey: "decision_id", id: "perm_1"},
		{name: "question", status: "user_question_requested", eventType: "user.question", idKey: "question_id", id: "q_1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projected, ok := projectEvent(eventModeCanonical, map[string]any{
				"type":       "ToolRequested",
				"event_id":   "evt_1",
				"session_id": "sess_1",
				"task_id":    "task_1",
				"payload": map[string]any{
					"status":  tt.status,
					tt.idKey:  tt.id,
					"request": map[string]any{"prompt": "Continue?"},
				},
			}, 7)
			if !ok {
				t.Fatal("durable governance request was filtered")
			}
			event := projected.(map[string]any)
			if event["type"] != tt.eventType || event[tt.idKey] != tt.id {
				t.Fatalf("projected request = %#v", event)
			}
			if event["event_id"] != "evt_1" || event["run_id"] != "task_1" || event["raw_cursor"] != 7 {
				t.Fatalf("projected replay identity = %#v", event)
			}
			if _, legacy := event["task_id"]; legacy {
				t.Fatalf("canonical request retained task_id = %#v", event)
			}
		})
	}
}

func TestCanonicalLiveAndReplayRetainDurableIdentity(t *testing.T) {
	live, ok := projectEvent(eventModeCanonical, map[string]any{
		"event_id":             "evt_identity",
		"type":                 "ToolCallStarted",
		"task_id":              "task_identity",
		internalRawAuditCursor: 9,
	})
	if !ok {
		t.Fatal("live canonical event was filtered")
	}
	replay, ok := projectEvent(eventModeCanonical, json.RawMessage(`{
		"event_id":"evt_identity",
		"type":"ToolCallStarted",
		"task_id":"task_identity"
	}`), 9)
	if !ok {
		t.Fatal("replayed canonical event was filtered")
	}
	for _, event := range []map[string]any{live.(map[string]any), replay.(map[string]any)} {
		if event["event_id"] != "evt_identity" || event["run_id"] != "task_identity" || event["raw_cursor"] != 9 {
			t.Fatalf("canonical identity = %#v", event)
		}
		if _, legacy := event["task_id"]; legacy {
			t.Fatalf("canonical event retained task_id = %#v", event)
		}
	}
}

func TestEventProjectionRejectsUnencodablePrivateCursorEvent(t *testing.T) {
	event := map[string]any{
		"type":                 "ToolCallStarted",
		internalRawAuditCursor: 9,
		"invalid":              func() {},
	}
	for _, mode := range []eventMode{eventModeCompat, eventModeCanonical} {
		if projected, ok := projectEvent(mode, event); ok || projected != nil {
			t.Fatalf("%s projected unencodable private event: %#v", mode, projected)
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

func TestSessionAttachCanonicalReplaysGovernanceLifecycle(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	d.record(sess.SessionID, "ToolRequested", "task_1", "go", map[string]any{
		"status": "permission_requested", "decision_id": "perm_1",
		"request": map[string]any{
			"type": "permission.request", "session_id": sess.SessionID,
			"task_id": "task_1", "decision_id": "perm_1", "capability": "CommandExec",
		},
	}, "perm_1")
	d.record(sess.SessionID, "ExecutionProgressed", "run_1", "operator", map[string]any{
		"status": "approval_resolved", "decision_id": "perm_1", "granted": true,
	}, "perm_1")

	raw, err := d.handleSessionAttach(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "event_mode": "canonical",
	}))
	if err != nil {
		t.Fatal(err)
	}
	events := raw.(map[string]any)["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("governance replay = %#v", events)
	}
	request := events[0].(map[string]any)
	if request["type"] != "permission.request" || request["decision_id"] != "perm_1" || request["raw_cursor"] != 1 {
		t.Fatalf("request replay = %#v", request)
	}
	resolution := events[1].(map[string]any)
	payload := resolution["payload"].(map[string]any)
	if payload["status"] != "approval_resolved" || payload["decision_id"] != "perm_1" || resolution["raw_cursor"] != 2 {
		t.Fatalf("resolution replay = %#v", resolution)
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
