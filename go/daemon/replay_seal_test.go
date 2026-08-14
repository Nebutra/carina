package daemon

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSealedRunPhasesFromPrefixVisibleFinalAndTerminal(t *testing.T) {
	events := []map[string]any{
		{"type": "ToolCallRequested", "task_id": "run_1", "payload": map[string]any{"tool": "read"}},
		{"type": "ModelResponded", "task_id": "run_1", "payload": map[string]any{"text": `{"tool":"read","path":"main.go"}`}},
		{"type": "ModelResponded", "task_id": "run_1", "payload": map[string]any{"text": `{"tool":"done","summary":"hello"}`}},
		{"type": "ExecutionCompleted", "task_id": "run_1", "payload": map[string]any{"summary": "hello"}},
		{"type": "ModelResponded", "task_id": "run_2", "payload": map[string]any{"text": "plain visible"}},
		{"type": "ExecutionFailed", "task_id": "run_3", "payload": map[string]any{"error": "boom"}},
		{"type": "ModelResponded", "task_id": "run_4", "payload": map[string]any{"text": `I will inspect {"tool":"read","path":"trunc`}},
		{"type": "ModelResponded", "task_id": "run_5", "payload": map[string]any{"error": "provider dropped", "text": ""}},
	}
	raw := make([]json.RawMessage, len(events))
	for i, event := range events {
		body, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		raw[i] = body
	}
	sealed, err := sealedRunPhasesFromPrefix(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := []sealedRunPhase{
		{RunID: "run_1", Phase: assistantPhaseFinalAnswer},
		{RunID: "run_2", Phase: assistantPhaseFinalAnswer},
		{RunID: "run_3", Phase: assistantPhaseFinalAnswer},
		{RunID: "run_5", Phase: assistantPhaseFinalAnswer},
	}
	if len(sealed) != len(want) {
		t.Fatalf("sealed = %+v, want %+v", sealed, want)
	}
	for i := range want {
		if sealed[i] != want[i] {
			t.Fatalf("sealed[%d] = %+v, want %+v", i, sealed[i], want[i])
		}
	}
}

func TestSealedRunPhasesScanIncludesEventsAtOrBelowSince(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`{"type":"ModelResponded","task_id":"old","payload":{"text":"already in items"}}`),
		json.RawMessage(`{"type":"ExecutionCompleted","task_id":"old","payload":{"summary":"already in items"}}`),
		json.RawMessage(`{"type":"ToolCallStarted","task_id":"new","payload":{}}`),
	}
	sealed, err := sealedRunPhasesFromPrefix(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != 1 || sealed[0].RunID != "old" {
		t.Fatalf("prefix seal = %+v", sealed)
	}
}

func TestEventStreamLoadCatchUpReplaySealsFullPrefix(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	d.record(sess.SessionID, "ModelResponded", "old_run", "model", map[string]any{"text": `{"tool":"done","summary":"already hydrated"}`}, "")
	d.record(sess.SessionID, "ExecutionCompleted", "old_run", "go", map[string]any{"summary": "already hydrated"}, "")
	d.record(sess.SessionID, "ModelResponded", "tool_run", "model", map[string]any{"text": `{"tool":"read","path":"a.go"}`}, "")
	d.record(sess.SessionID, "ToolCallStarted", "new_run", "go", map[string]any{"call_id": "c1"}, "")

	replay, err := d.loadCatchUpReplay(sess.SessionID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if replay.durableCursor != 4 {
		t.Fatalf("durable cursor = %d, want 4", replay.durableCursor)
	}
	if len(replay.events) != 2 {
		t.Fatalf("replay events = %d, want 2 (strictly after since=2)", len(replay.events))
	}
	if len(replay.sealed) != 1 || replay.sealed[0].RunID != "old_run" {
		t.Fatalf("sealed = %+v, want old_run from the prefix at/below since", replay.sealed)
	}
	if rawAuditCursor(replay.events[0].(map[string]any)) != 3 || rawAuditCursor(replay.events[1].(map[string]any)) != 4 {
		t.Fatalf("replay cursors = %+v", replay.events)
	}
}

func TestEventStreamAttachSinceHSealsPrefixFinal(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.kern.RecordEvent(sess.SessionID, "ModelResponded", "old_run", "model", map[string]any{"text": `{"tool":"done","summary":"already in items"}`}, ""); err != nil {
		t.Fatal(err)
	}
	if err := d.kern.RecordEvent(sess.SessionID, "ExecutionCompleted", "old_run", "go", map[string]any{"summary": "already in items"}, ""); err != nil {
		t.Fatal(err)
	}
	var owner assistantTailOwner
	if err := d.events.PublishAssistantTail(&owner, sess.SessionID, "old_run", "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	if err := d.events.PublishAssistantTail(&owner, sess.SessionID, "old_run", "delta", 1, 2, "stale tail", false); err != nil {
		t.Fatal(err)
	}

	replay, err := d.loadCatchUpReplay(sess.SessionID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if replay.durableCursor != 2 {
		t.Fatalf("H = %d, want 2", replay.durableCursor)
	}
	if len(replay.events) != 0 {
		t.Fatalf("since=H must not replay items-owned events: %+v", replay.events)
	}
	if len(replay.sealed) != 1 || replay.sealed[0].RunID != "old_run" {
		t.Fatalf("prefix seal at H = %+v", replay.sealed)
	}

	inner := newFakeEventSub("since-h")
	result, err := d.events.subscribeCatchUp(sess.SessionID, inner, func() (catchUpReplay, error) {
		return replay, nil
	}, catchUpOptions{emitSnapshots: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.transientSnapshots != 0 {
		t.Fatalf("stale tail snapshot escaped since=H attach: %+v frames=%+v", result, inner.events)
	}
	if err := d.events.PublishAssistantTail(&owner, sess.SessionID, "old_run", "delta", 1, 3, "late", false); !errors.Is(err, errAssistantTailSealed) {
		t.Fatalf("prefix seal did not retire the captured tail: %v", err)
	}
}

func TestModelRespondedToolTurnIsNotASeal(t *testing.T) {
	pair, ok := eventSealsAssistantTail(map[string]any{
		"type":    "ModelResponded",
		"task_id": "run_1",
		"payload": map[string]any{"text": `{"tool":"read","path":"x.go"}`},
	})
	if ok {
		t.Fatalf("tool-turn ModelResponded sealed %+v", pair)
	}
	pair, ok = eventSealsAssistantTail(map[string]any{
		"type":    "TaskCompleted",
		"task_id": "task_1",
	})
	if ok {
		t.Fatalf("delegated TaskCompleted sealed %+v", pair)
	}
}
