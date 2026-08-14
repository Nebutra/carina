package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
)

func TestSessionItemsWatermarkIsRawAuditSnapshotAndLegacyStaysBare(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSession(sess.SessionID, workspace, "safe-edit"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := d.kern.RecordEvent(sess.SessionID, "ExternalEvent", "", "go", map[string]any{"index": i}, ""); err != nil {
			t.Fatal(err)
		}
	}

	legacy, err := d.handleSessionItems(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	legacyItems, ok := legacy.([]SessionItemEvent)
	if !ok || len(legacyItems) != 1 {
		t.Fatalf("legacy response changed shape or projection: %#v", legacy)
	}

	result, err := d.handleSessionItems(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "watermark_version": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok := result.(SessionItemsSnapshot)
	if !ok {
		t.Fatalf("watermark response = %#v", result)
	}
	if snapshot.SessionID != sess.SessionID || snapshot.DurableCursor != 3 || len(snapshot.Items) != 1 {
		t.Fatalf("watermark must count raw audit holes: %+v", snapshot)
	}
	if snapshot.DurableCursor == len(snapshot.Items) {
		t.Fatal("durable cursor was incorrectly derived from projected items")
	}
}

func TestSessionItemsWatermarkRejectsVersionAndPaginationMixes(t *testing.T) {
	for _, params := range []map[string]any{
		{"session_id": "sess", "watermark_version": 0},
		{"session_id": "sess", "watermark_version": 2},
		{"session_id": "sess", "watermark_version": nil},
		{"session_id": "sess", "watermark_version": 1, "limit": 10},
		{"session_id": "sess", "watermark_version": 1, "cursor": nil},
	} {
		if _, err := (&Daemon{}).handleSessionItems(mustJSON(t, params)); err == nil {
			t.Fatalf("invalid watermark params accepted: %#v", params)
		}
	}
}

func TestPaginateSessionItemsStableCursor(t *testing.T) {
	authority := &projectionCursorAuthority{key: []byte("01234567890123456789012345678901")}
	epoch := "epoch-1"
	items := make([]SessionItemEvent, 7)
	for i := range items {
		items[i].ItemID = string(rune('a' + i))
	}
	first, err := paginateSessionItems(authority, "sess_1", epoch, items, 0, 3)
	if err != nil {
		t.Fatal(err)
	}
	page := first["data"].([]SessionItemEvent)
	nextCursor, ok := first["next_cursor"].(string)
	if len(page) != 3 || !ok || strings.Contains(nextCursor, "sess_1") || first["projection_version"] != sessionProjectionVersion {
		t.Fatalf("unexpected first page: %+v", first)
	}
	nextRaw, _ := json.Marshal(nextCursor)
	next, err := authority.decode("sess_1", epoch, nextRaw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := paginateSessionItems(authority, "sess_1", epoch, items, next, 3)
	if err != nil {
		t.Fatal(err)
	}
	got := second["data"].([]SessionItemEvent)
	if got[0].ItemID != "d" {
		t.Fatalf("cursor skipped or duplicated items: %+v", got)
	}
	if _, err := paginateSessionItems(authority, "sess_1", epoch, items, -1, 3); err == nil {
		t.Fatal("negative cursor accepted")
	}
	if _, err := paginateSessionItems(authority, "sess_1", epoch, items, 0, 201); err == nil {
		t.Fatal("oversized limit accepted")
	}
	if _, err := authority.decode("sess_1", epoch, json.RawMessage(`"broken"`)); err == nil {
		t.Fatal("invalid opaque cursor accepted")
	}
	if _, err := authority.decode("sess_2", epoch, nextRaw); err == nil {
		t.Fatal("cross-session cursor accepted")
	}
	expired := authority.encodeClaims(projectionCursorClaims{Version: 1, SessionID: "sess_1", Projection: sessionProjectionVersion, Epoch: "retired", Position: 3})
	expiredRaw, _ := json.Marshal(expired)
	_, err = authority.decode("sess_1", epoch, expiredRaw)
	var cursorErr *rpc.Error
	if !errors.As(err, &cursorErr) || cursorErr.Message != "cursor_expired" || cursorErr.Data == nil {
		t.Fatalf("typed expiry recovery missing: %#v", err)
	}
	if _, err := paginateSessionItems(authority, "sess_1", epoch, items, len(items)+1, 3); err == nil {
		t.Fatal("expired cursor was silently clamped")
	}
}

func TestProjectionCursorKeyPersistsAcrossDaemonRestart(t *testing.T) {
	stateDir := t.TempDir()
	firstDaemon := &Daemon{stateDir: stateDir}
	first, err := firstDaemon.projectionCursorAuthority()
	if err != nil {
		t.Fatal(err)
	}
	cursor := first.encode("sess", "epoch", 4)
	projectionCursorAuthorities.Lock()
	delete(projectionCursorAuthorities.byStateDir, stateDir)
	projectionCursorAuthorities.Unlock()
	second, err := (&Daemon{stateDir: stateDir}).projectionCursorAuthority()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cursor)
	position, err := second.decode("sess", "epoch", raw)
	if err != nil || position != 4 {
		t.Fatalf("cursor did not survive restart: position=%d err=%v", position, err)
	}
	info, err := os.Stat(filepath.Join(stateDir, "projection-cursor.key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cursor key permissions=%v err=%v", info.Mode().Perm(), err)
	}
}

func TestProjectionCursorKeyRejectsPartialFile(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stateDir, "projection-cursor.key"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectionCursorAuthorities.Lock()
	delete(projectionCursorAuthorities.byStateDir, stateDir)
	projectionCursorAuthorities.Unlock()
	if _, err := (&Daemon{stateDir: stateDir}).projectionCursorAuthority(); err == nil || !strings.Contains(err.Error(), "32 bytes") {
		t.Fatalf("corrupt key did not fail closed: %v", err)
	}
}

func TestPersistProjectionCursorKeyConcurrentPublish(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "projection-cursor.key")
	keys := [][]byte{[]byte("01234567890123456789012345678901"), []byte("abcdefghijklmnopqrstuvwxyzABCDEF")}
	errs := make([]error, len(keys))
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = persistProjectionCursorKey(stateDir, path, keys[i])
		}(i)
	}
	wg.Wait()
	winners := 0
	for _, err := range errs {
		if err == nil {
			winners++
		} else if !os.IsExist(err) {
			t.Fatalf("unexpected concurrent publish error: %v", err)
		}
	}
	stored, err := os.ReadFile(path)
	if err != nil || winners != 1 || len(stored) != 32 {
		t.Fatalf("atomic publish failed: winners=%d len=%d err=%v", winners, len(stored), err)
	}
	if string(stored) != string(keys[0]) && string(stored) != string(keys[1]) {
		t.Fatalf("stored key is torn: %q", stored)
	}
}

func TestProjectSessionReviewDeterministic(t *testing.T) {
	items := projectSessionItems("sess_1", []itemAuditEvent{
		{EventID: "evt_1", SessionID: "sess_1", TaskID: "task_1", Type: "TaskCreated", Payload: map[string]any{"status": "submitted", "user_prompt": "ship", "success_criteria": []any{map[string]any{"kind": "command", "command": []any{"go", "test", "./..."}}}}},
		{EventID: "evt_q1", SessionID: "sess_1", TaskID: "task_1", Type: "ToolRequested", Payload: map[string]any{"status": "user_question_requested", "question_id": "q1", "request": map[string]any{"prompt": "Proceed?", "options": []any{"yes", "no"}}}},
		{EventID: "evt_q2", SessionID: "sess_1", TaskID: "task_1", Type: "TaskCreated", Payload: map[string]any{"status": "user_question_resolved", "question_id": "q1", "value": "yes"}},
		{EventID: "evt_p1", SessionID: "sess_1", TaskID: "task_1", Type: "PatchProposed", Payload: map[string]any{"patch_id": "p1", "affected_files": []any{"a.go"}}},
		{EventID: "evt_p2", SessionID: "sess_1", Type: "PatchApplied", Payload: map[string]any{"patch_id": "p1", "rollback_pointer": "rb1"}},
		{EventID: "evt_2", SessionID: "sess_1", TaskID: "task_1", Type: "ToolCallCompleted", Payload: map[string]any{"call_id": "call_1", "tool": "run", "kind": "command", "status": "completed", "artifact_ids": []any{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}},
		{EventID: "evt_3", SessionID: "sess_1", TaskID: "task_1", Type: "TaskCreated", Payload: map[string]any{"status": "completed", "summary": "shipped"}},
	})
	first := projectSessionReview("sess_1", items, "cursor")
	second := projectSessionReview("sess_1", items, "cursor")
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Fatalf("review projection is not deterministic:\n%s\n%s", a, b)
	}
	if first.State != "completed" || first.Summary != "shipped" || first.Intent != "ship" || len(first.SuccessCriteria) != 1 || len(first.Commands) != 1 || len(first.Tools) != 0 || len(first.Questions) != 1 || first.Questions[0].Status != "resolved" || len(first.Artifacts) != 1 || first.Rollback["available"] != true {
		t.Fatalf("unexpected review: %+v", first)
	}
}

func TestProjectSessionReviewLatestTurnAndWaitingState(t *testing.T) {
	events := []SessionItemEvent{
		{Type: "turn.started", TurnID: "t1", Details: map[string]any{"prompt": "first"}},
		{Type: "turn.completed", TurnID: "t1", Details: map[string]any{"summary": "old"}},
		{Type: "turn.started", TurnID: "t2", Details: map[string]any{"prompt": "second"}},
		{Type: "item.started", Item: &SessionItem{ID: "approve-1", Type: "approval", Status: "requested"}},
	}
	review := projectSessionReview("s", events, "cursor")
	if review.State != "needs_input" || review.WaitingReason != "waiting_approval" || review.Summary != "" || review.Intent != "second" {
		t.Fatalf("latest turn/waiting state not reduced: %+v", review)
	}
	events = append(events, SessionItemEvent{Type: "item.completed", Item: &SessionItem{ID: "approve-1", Type: "approval", Status: "resolved"}})
	review = projectSessionReview("s", events, "cursor")
	if review.State != "active" || review.WaitingReason != "" || len(review.Questions) != 1 || review.Questions[0].Status != "resolved" {
		t.Fatalf("resolved approval did not clear wait: %+v", review)
	}
}

func TestProjectSessionReviewClassifiesGoalCheck(t *testing.T) {
	review := projectSessionReview("s", []SessionItemEvent{{Type: "item.completed", Item: &SessionItem{ID: "check-1", Type: "tool_call", Status: "completed", Details: map[string]any{"tool": "goal_check"}}}}, "cursor")
	if len(review.Checks) != 1 || len(review.Tools) != 1 || len(review.Commands) != 0 {
		t.Fatalf("goal_check classification: %+v", review)
	}
}

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

func TestProjectSessionItemsPreservesToolTimeout(t *testing.T) {
	events := []itemAuditEvent{
		{EventID: "evt_1", SessionID: "sess_1", TaskID: "task_1", Type: "ToolCallStarted", Timestamp: "2026-07-07T00:00:01Z", Payload: map[string]any{"call_id": "call_1", "tool": "run", "status": "running"}},
		{EventID: "evt_2", SessionID: "sess_1", TaskID: "task_1", Type: "ToolCallFailed", Timestamp: "2026-07-07T00:00:02Z", Payload: map[string]any{"call_id": "call_1", "tool": "run", "status": "timed_out"}},
	}
	item := findItem(t, projectSessionItems("sess_1", events), "item.completed", "tool_call")
	if item.Status != "timed_out" {
		t.Fatalf("status=%q", item.Status)
	}
}

func TestProjectSessionItemsKeepsAskUserInGovernanceOnly(t *testing.T) {
	events := []itemAuditEvent{
		{EventID: "evt_1", SessionID: "sess_1", TaskID: "task_1", Type: "ToolCallRequested", Payload: map[string]any{
			"call_id": "call_1", "tool": "ask_user", "kind": "interaction", "status": "pending",
			"intent": "Need a location before querying weather",
		}},
		{EventID: "evt_2", SessionID: "sess_1", TaskID: "task_1", Type: "ToolRequested", Payload: map[string]any{
			"status": "user_question_requested", "question_id": "question_1",
			"request": map[string]any{"prompt": "Which city?"},
		}},
		{EventID: "evt_3", SessionID: "sess_1", TaskID: "task_1", Type: "ToolCallCompleted", Payload: map[string]any{
			"call_id": "call_1", "tool": "ask_user", "kind": "interaction", "status": "completed",
		}},
	}

	items := projectSessionItems("sess_1", events)
	var questions, toolCalls int
	for _, event := range items {
		if event.Item == nil {
			continue
		}
		switch event.Item.Type {
		case "question":
			questions++
		case "tool_call":
			toolCalls++
		}
	}
	if questions != 1 || toolCalls != 0 {
		t.Fatalf("ask_user must have one governance owner: questions=%d tool_calls=%d items=%+v", questions, toolCalls, items)
	}
}

func TestProjectSessionItemsMergesLifecycleOwnedCommandDetails(t *testing.T) {
	events := []itemAuditEvent{
		{EventID: "evt_1", SessionID: "sess_1", TaskID: "task_1", Type: "ToolCallRequested", Payload: map[string]any{
			"call_id": "call_1", "tool": "run", "kind": "command", "status": "pending",
			"intent":    "Verify the daemon lifecycle",
			"arguments": map[string]any{"executable": "go", "argc": 3},
		}},
		{EventID: "evt_2", SessionID: "sess_1", TaskID: "task_1", Type: "CommandStarted", Payload: map[string]any{
			"command_id": "cmd_1", "command": "go test ./...", "cwd": "/workspace",
		}},
		{EventID: "evt_3", SessionID: "sess_1", TaskID: "task_1", Type: "CommandOutput", Payload: map[string]any{
			"command_id": "cmd_1", "stream": "stdout", "chunk": "ok\n",
		}},
		{EventID: "evt_4", SessionID: "sess_1", TaskID: "task_1", Type: "CommandExited", Payload: map[string]any{
			"command_id": "cmd_1", "exit_code": 0, "duration_ms": 42,
		}},
		{EventID: "evt_5", SessionID: "sess_1", TaskID: "task_1", Type: "ToolCallCompleted", Payload: map[string]any{
			"call_id": "call_1", "tool": "run", "kind": "command", "status": "completed",
		}},
	}

	items := projectSessionItems("sess_1", events)
	item := findItem(t, items, "item.completed", "tool_call")
	if item.ID != "call_1" || item.Status != "completed" {
		t.Fatalf("unexpected lifecycle command: %+v", item)
	}
	if item.Details["command"] != "go test ./..." || item.Details["stdout"] != "ok\n" || fmt.Sprint(item.Details["exit_code"]) != "0" {
		t.Fatalf("command details were not retained: %+v", item.Details)
	}
	if item.Details["intent"] != "Verify the daemon lifecycle" {
		t.Fatalf("tool intent was not retained for replay: %+v", item.Details)
	}
	for _, event := range items {
		if event.Item != nil && event.Item.Type == "command_execution" {
			t.Fatalf("lifecycle-owned command duplicated as command_execution: %+v", event.Item)
		}
	}
}

func TestProjectSessionItemsHidesInternalModelActionJSON(t *testing.T) {
	items := projectSessionItems("sess_1", []itemAuditEvent{
		{EventID: "evt_1", SessionID: "sess_1", TaskID: "task_1", Type: "ModelResponded", Actor: "model", Payload: map[string]any{
			"text": `{"tool":"read","path":"main.go"}`,
		}},
	})
	for _, event := range items {
		if event.Item != nil && event.Item.Type == "agent_message" {
			t.Fatalf("internal action JSON leaked as assistant content: %+v", event.Item)
		}
	}
}

func TestProjectSessionItemsSurfacesDoneSummaryAsAgentMessage(t *testing.T) {
	items := projectSessionItems("sess_1", []itemAuditEvent{
		{EventID: "evt_q", SessionID: "sess_1", TaskID: "run_1", Type: "ExecutionQueued", Payload: map[string]any{
			"user_prompt": "hi", "model": "provider/model",
		}},
		{EventID: "evt_m", SessionID: "sess_1", TaskID: "run_1", Type: "ModelResponded", Actor: "model", Payload: map[string]any{
			"text": `{"tool":"done","summary":"你好！有什么可以帮你的？"}`,
		}},
		{EventID: "evt_c", SessionID: "sess_1", TaskID: "run_1", Type: "ExecutionCompleted", Payload: map[string]any{
			"summary": "你好！有什么可以帮你的？",
		}},
	})
	assertEventType(t, items, "turn.started")
	msg := findItem(t, items, "item.completed", "agent_message")
	if msg.ID != "run_1" {
		t.Fatalf("agent message id = %q, want stable run identity", msg.ID)
	}
	if msg.Details["text"] != "你好！有什么可以帮你的？" {
		t.Fatalf("done summary not projected as agent_message text: %+v", msg.Details)
	}
	// ExecutionCompleted retains lifecycle for retry-chain settlement, but must
	// not duplicate the answer already projected by ModelResponded(done).
	var completed bool
	for _, event := range items {
		if event.Type == "turn.completed" {
			completed = true
			if stringField(event.Details, "summary") != "" {
				t.Fatalf("turn.completed duplicated done summary: %+v", event)
			}
		}
	}
	if !completed {
		t.Fatalf("ExecutionCompleted lifecycle was not retained: %+v", items)
	}
}

func TestProjectSessionItemsPresentsLegacyStructuredDoneSummaryAsMarkdown(t *testing.T) {
	items := projectSessionItems("sess_1", []itemAuditEvent{
		{EventID: "evt_m", SessionID: "sess_1", TaskID: "run_1", Type: "ModelResponded", Actor: "model", Payload: map[string]any{
			"text": `{"tool":"done","summary":"{\"summary\":\"Pebble is a local-first IDE.\",\"architecture\":[\"Desktop: Tauri\",\"Runtime: Go\"],\"verification\":\"No files changed.\"}"}`,
		}},
	})
	msg := findItem(t, items, "item.completed", "agent_message")
	text := stringField(msg.Details, "text")
	if !strings.Contains(text, "Pebble is a local-first IDE.") ||
		!strings.Contains(text, "**Architecture**\n- Desktop: Tauri") ||
		!strings.Contains(text, "**Verification**\nNo files changed.") {
		t.Fatalf("legacy report was not presented as markdown: %q", text)
	}
	if strings.HasPrefix(text, "{") {
		t.Fatalf("legacy report leaked as JSON: %q", text)
	}
}

func TestProjectSessionItemsPreservesExplicitStructuredOutput(t *testing.T) {
	raw := `{"summary":"machine result","architecture":["runtime"]}`
	items := projectSessionItems("sess_1", []itemAuditEvent{
		{EventID: "evt_m", SessionID: "sess_1", TaskID: "run_1", Type: "ModelResponded", Actor: "model", Payload: map[string]any{
			"text":              `{"tool":"done","summary":"{\"summary\":\"machine result\",\"architecture\":[\"runtime\"]}"}`,
			"structured_output": true,
		}},
	})
	msg := findItem(t, items, "item.completed", "agent_message")
	if text := stringField(msg.Details, "text"); text != raw {
		t.Fatalf("explicit structured output changed: got %q want %q", text, raw)
	}
}

func TestProjectSessionItemsPresentsTerminalOnlyLegacyReport(t *testing.T) {
	items := projectSessionItems("sess_1", []itemAuditEvent{
		{EventID: "evt_c", SessionID: "sess_1", TaskID: "run_1", Type: "ExecutionCompleted", Payload: map[string]any{
			"summary": `{"summary":"Readable result.","verification":"No files changed."}`,
		}},
	})
	for _, event := range items {
		if event.Type == "turn.completed" {
			if summary := stringField(event.Details, "summary"); summary != "Readable result.\n\n**Verification**\nNo files changed." {
				t.Fatalf("terminal-only report projection = %q", summary)
			}
			return
		}
	}
	t.Fatalf("terminal completion missing: %+v", items)
}

func TestProjectSessionItemsSuppressesTruncatedActionAndUsesTerminalSummary(t *testing.T) {
	items := projectSessionItems("sess_1", []itemAuditEvent{
		{EventID: "evt_prefixed", SessionID: "sess_1", TaskID: "run_1", Type: "ModelResponded", Payload: map[string]any{
			"text": `I will inspect one more boundary. {"actions":[{"tool":"read","path":"truncated`,
		}},
		{EventID: "evt_m", SessionID: "sess_1", TaskID: "run_1", Type: "ModelResponded", Payload: map[string]any{
			"text": `{"tool":"done","summary":"{\"summary\":\"Pebble is a local-first IDE.\",\"architecture\":[\"truncated`,
		}},
		{EventID: "evt_c", SessionID: "sess_1", TaskID: "run_1", Type: "ExecutionCompleted", Payload: map[string]any{
			"summary": `{"summary":"Pebble is a local-first IDE.","architecture":["Desktop: Tauri"]}`,
		}},
	})
	for _, event := range items {
		if event.Item != nil && event.Item.Type == "agent_message" {
			t.Fatalf("truncated internal action leaked as a message: %+v", event.Item)
		}
	}
	for _, event := range items {
		if event.Type == "turn.completed" {
			if summary := stringField(event.Details, "summary"); summary != "Pebble is a local-first IDE.\n\n**Architecture**\n- Desktop: Tauri" {
				t.Fatalf("terminal fallback = %q", summary)
			}
			return
		}
	}
	t.Fatalf("terminal completion missing: %+v", items)
}

func TestProjectSessionItemsHydratesTurnStartFromExecutionQueued(t *testing.T) {
	items := projectSessionItems("sess_1", []itemAuditEvent{
		{
			EventID: "evt_created", SessionID: "sess_1", TaskID: "run_retry", Type: "TaskCreated",
			Timestamp: "2026-08-11T00:00:00Z", Payload: map[string]any{"status": "submitted"},
		},
		{
			EventID: "evt_queued", SessionID: "sess_1", TaskID: "run_retry", Type: "ExecutionQueued",
			Timestamp: "2026-08-11T00:00:01Z", Payload: map[string]any{
				"user_prompt":     "retry the request",
				"model":           "provider/effective",
				"requested_model": "provider/requested",
				"effective_model": "provider/effective",
				"retry_of_run_id": "run_original",
				"retry_routing":   "current",
			},
		},
	})

	var starts []SessionItemEvent
	for _, event := range items {
		if event.Type == "turn.started" {
			starts = append(starts, event)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("turn.started count=%d, want 1: %+v", len(starts), items)
	}
	start := starts[0]
	if start.SourceEventID != "evt_queued" || start.Timestamp != "2026-08-11T00:00:01Z" {
		t.Fatalf("turn.started did not use queued provenance: %+v", start)
	}
	want := map[string]string{
		"source_type":     "ExecutionQueued",
		"user_prompt":     "retry the request",
		"model":           "provider/effective",
		"requested_model": "provider/requested",
		"effective_model": "provider/effective",
		"retry_of_run_id": "run_original",
		"retry_routing":   "current",
	}
	for key, value := range want {
		if got := stringField(start.Details, key); got != value {
			t.Fatalf("turn.started detail %s=%q, want %q: %+v", key, got, value, start.Details)
		}
	}
}

func TestProjectSessionItemsProjectsTypedTerminalFailures(t *testing.T) {
	for _, tc := range []struct {
		eventType string
		status    string
	}{
		{eventType: "ExecutionFailed", status: "failed"},
		{eventType: "ExecutionInterrupted", status: "interrupted"},
		{eventType: "ExecutionCancelled", status: "cancelled"},
	} {
		t.Run(tc.eventType, func(t *testing.T) {
			items := projectSessionItems("sess_1", []itemAuditEvent{
				{EventID: "evt_q", SessionID: "sess_1", TaskID: "run_1", Type: "ExecutionQueued"},
				{EventID: "evt_terminal", SessionID: "sess_1", TaskID: "run_1", Type: tc.eventType, Payload: map[string]any{
					"reason": "terminal reason", "owner": "runtime", "retryable": true,
				}},
			})
			var terminal *SessionItemEvent
			for i := range items {
				if items[i].Type == "turn.failed" && items[i].SourceEventID == "evt_terminal" {
					terminal = &items[i]
					break
				}
			}
			if terminal == nil {
				t.Fatalf("typed terminal failure was not projected: %+v", items)
			}
			if stringField(terminal.Details, "source_type") != tc.eventType ||
				stringField(terminal.Details, "status") != tc.status ||
				stringField(terminal.Details, "reason") != "terminal reason" {
				t.Fatalf("typed terminal failure lost semantics: %+v", terminal.Details)
			}
		})
	}
}

func TestProjectSessionItemsExecutionCompletedFallback(t *testing.T) {
	items := projectSessionItems("sess_1", []itemAuditEvent{
		{EventID: "evt_q", SessionID: "sess_1", TaskID: "run_2", Type: "ExecutionQueued", Payload: map[string]any{
			"user_prompt": "你好",
		}},
		{EventID: "evt_c", SessionID: "sess_1", TaskID: "run_2", Type: "ExecutionCompleted", Payload: map[string]any{
			"summary": "你好！",
		}},
	})
	assertEventType(t, items, "turn.started")
	assertEventType(t, items, "turn.completed")
	var found bool
	for _, event := range items {
		if event.Type == "turn.completed" && stringField(event.Details, "summary") == "你好！" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ExecutionCompleted summary not projected: %+v", items)
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
