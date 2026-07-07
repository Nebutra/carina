package daemon

import "testing"

func TestProjectSessionItemsCommandAndTurn(t *testing.T) {
	events := []itemAuditEvent{
		{
			EventID:   "evt_1",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "TaskCreated",
			Timestamp: "2026-07-07T00:00:00Z",
			Payload:   map[string]any{"status": "submitted", "prompt": "ship"},
		},
		{
			EventID:              "evt_2",
			SessionID:            "sess_1",
			TaskID:               "task_1",
			Type:                 "CommandStarted",
			Actor:                "zig",
			Timestamp:            "2026-07-07T00:00:01Z",
			PermissionDecisionID: "dec_1",
			Payload: map[string]any{
				"command_id":       "cmd_1",
				"command":          "go test ./go/daemon",
				"cwd":              "/repo",
				"risk_level":       float64(1),
				"package_mutation": true,
			},
		},
		{
			EventID:   "evt_3",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "CommandOutput",
			Timestamp: "2026-07-07T00:00:02Z",
			Payload:   map[string]any{"command_id": "cmd_1", "stream": "stdout", "chunk": "ok\n"},
		},
		{
			EventID:   "evt_4",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "CommandExited",
			Timestamp: "2026-07-07T00:00:03Z",
			Payload:   map[string]any{"command_id": "cmd_1", "exit_code": float64(0), "duration_ms": float64(12)},
		},
		{
			EventID:   "evt_5",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "ModelResponded",
			Actor:     "model",
			Timestamp: "2026-07-07T00:00:04Z",
			Payload:   map[string]any{"provider": "openai", "model": "gpt-5", "text": "done"},
		},
		{
			EventID:   "evt_6",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "TaskCreated",
			Timestamp: "2026-07-07T00:00:05Z",
			Payload:   map[string]any{"status": "completed", "summary": "done"},
		},
	}

	items := projectSessionItems("sess_1", events)
	assertEventType(t, items, "thread.started")
	assertEventType(t, items, "turn.started")
	assertEventType(t, items, "turn.completed")

	cmd := findItem(t, items, "item.completed", "command_execution")
	if cmd.ID != "cmd_1" {
		t.Fatalf("command item id = %q, want cmd_1", cmd.ID)
	}
	if cmd.Status != "completed" {
		t.Fatalf("command status = %q", cmd.Status)
	}
	if cmd.Details["stdout"] != "ok\n" || cmd.Details["exit_code"] != float64(0) {
		t.Fatalf("unexpected command details: %+v", cmd.Details)
	}
	if cmd.Details["permission_decision_id"] != "dec_1" {
		t.Fatalf("missing decision id: %+v", cmd.Details)
	}

	msg := findItem(t, items, "item.completed", "agent_message")
	if msg.Status != "completed" || msg.Details["text"] != "done" {
		t.Fatalf("unexpected agent message: %+v", msg)
	}
}

func TestProjectSessionItemsLegacyCommandWithoutID(t *testing.T) {
	events := []itemAuditEvent{
		{
			EventID:   "evt_start",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "CommandStarted",
			Timestamp: "2026-07-07T00:00:01Z",
			Payload:   map[string]any{"command": "echo ok", "cwd": "/repo"},
		},
		{
			EventID:   "evt_out",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "CommandOutput",
			Timestamp: "2026-07-07T00:00:02Z",
			Payload:   map[string]any{"stream": "stdout", "chunk": "ok"},
		},
		{
			EventID:   "evt_exit",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "CommandExited",
			Timestamp: "2026-07-07T00:00:03Z",
			Payload:   map[string]any{"exit_code": float64(0)},
		},
	}

	items := projectSessionItems("sess_1", events)
	cmd := findItem(t, items, "item.completed", "command_execution")
	if cmd.ID != "cmd_evt_start" {
		t.Fatalf("legacy command id = %q, want cmd_evt_start", cmd.ID)
	}
	if cmd.Details["stdout"] != "ok" {
		t.Fatalf("legacy output not attached: %+v", cmd.Details)
	}
}

func TestProjectSessionItemsRiskReview(t *testing.T) {
	events := []itemAuditEvent{
		{
			EventID:              "evt_risk",
			SessionID:            "sess_1",
			TaskID:               "task_1",
			Type:                 "TaskCreated",
			Timestamp:            "2026-07-07T00:00:01Z",
			PermissionDecisionID: "dec_1",
			Payload: map[string]any{
				"status":        "risk_review",
				"decision_id":   "dec_1",
				"mode":          "enforce",
				"outcome":       "deny",
				"risk":          "high",
				"authorization": "low",
				"source":        "heuristic",
				"rationale":     "destructive command",
			},
		},
	}

	items := projectSessionItems("sess_1", events)
	review := findItem(t, items, "item.completed", "risk_review")
	if review.ID != "dec_1" || review.Status != "failed" {
		t.Fatalf("unexpected review item: %+v", review)
	}
	if review.Details["outcome"] != "deny" || review.Details["permission_decision_id"] != "dec_1" {
		t.Fatalf("unexpected review details: %+v", review.Details)
	}
}

func TestProjectSessionItemsTurnNetDiff(t *testing.T) {
	events := []itemAuditEvent{
		{
			EventID:   "evt_1",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "TaskCreated",
			Timestamp: "2026-07-07T00:00:00Z",
			Payload:   map[string]any{"status": "submitted", "prompt": "edit"},
		},
		{
			EventID:   "evt_2",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "PatchProposed",
			Timestamp: "2026-07-07T00:00:01Z",
			Payload: map[string]any{
				"patch_id":       "patch_1",
				"affected_files": []any{"a.go", "b.go"},
				"reason":         "main edit",
			},
		},
		{
			EventID:   "evt_3",
			SessionID: "sess_1",
			Type:      "PatchApplied",
			Timestamp: "2026-07-07T00:00:02Z",
			Payload: map[string]any{
				"patch_id":         "patch_1",
				"new_hash":         "hash_1",
				"rollback_pointer": "rb_1",
			},
		},
		{
			EventID:   "evt_4",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "PatchProposed",
			Timestamp: "2026-07-07T00:00:03Z",
			Payload: map[string]any{
				"patch_id":       "patch_2",
				"affected_files": []any{"scratch.txt"},
				"reason":         "temporary edit",
			},
		},
		{
			EventID:   "evt_5",
			SessionID: "sess_1",
			Type:      "PatchApplied",
			Timestamp: "2026-07-07T00:00:04Z",
			Payload:   map[string]any{"patch_id": "patch_2", "rollback_pointer": "rb_2"},
		},
		{
			EventID:   "evt_6",
			SessionID: "sess_1",
			Type:      "RollbackCompleted",
			Timestamp: "2026-07-07T00:00:05Z",
			Payload:   map[string]any{"patch_id": "patch_2"},
		},
		{
			EventID:   "evt_7",
			SessionID: "sess_1",
			TaskID:    "task_1",
			Type:      "TaskCreated",
			Timestamp: "2026-07-07T00:00:06Z",
			Payload:   map[string]any{"status": "completed", "summary": "done"},
		},
	}

	items := projectSessionItems("sess_1", events)
	diff := findItem(t, items, "item.completed", "turn_net_diff")
	if diff.ID != "diff_task_1" || diff.Status != "completed" {
		t.Fatalf("unexpected diff item: %+v", diff)
	}
	if diff.Details["patch_count"] != 2 {
		t.Fatalf("unexpected patch count: %+v", diff.Details)
	}
	if !hasString(diff.Details["active_files"], "a.go") || !hasString(diff.Details["active_files"], "b.go") {
		t.Fatalf("active files missing: %+v", diff.Details)
	}
	if !hasString(diff.Details["reverted_files"], "scratch.txt") {
		t.Fatalf("reverted file missing: %+v", diff.Details)
	}
}

func assertEventType(t *testing.T, events []SessionItemEvent, typ string) {
	t.Helper()
	for _, ev := range events {
		if ev.Type == typ {
			return
		}
	}
	t.Fatalf("missing event type %s in %+v", typ, events)
}

func findItem(t *testing.T, events []SessionItemEvent, eventType, itemType string) *SessionItem {
	t.Helper()
	for _, ev := range events {
		if ev.Type == eventType && ev.Item != nil && ev.Item.Type == itemType {
			return ev.Item
		}
	}
	t.Fatalf("missing %s item for %s in %+v", itemType, eventType, events)
	return nil
}

func hasString(v any, want string) bool {
	switch list := v.(type) {
	case []string:
		for _, s := range list {
			if s == want {
				return true
			}
		}
	case []any:
		for _, item := range list {
			if item == want {
				return true
			}
		}
	}
	return false
}
