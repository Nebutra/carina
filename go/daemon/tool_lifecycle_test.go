package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestApprovalLifecycleUsesSameCallIDBeforeStarted(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	d.SetInteractiveApproval(true)
	d.approvalTimeout = 5 * time.Second
	sess, _ := d.store.CreateSessionMode(ws, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, ws, "safe-edit", "on_request", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "patch")
	if err := os.WriteFile(filepath.Join(ws, "a.txt"), []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.recordRead(sess.SessionID, "a.txt", "before")

	var types []string
	var callIDs []string
	d.events.Tap(func(_ string, event map[string]any) {
		typ, _ := event["type"].(string)
		if strings.HasPrefix(typ, "ToolCall") {
			types = append(types, typ)
		}
		if payload, ok := event["payload"].(map[string]any); ok {
			if id, _ := payload["call_id"].(string); id != "" {
				callIDs = append(callIDs, id)
			}
		}
	})
	done := make(chan string, 1)
	go func() { done <- d.executeAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "after"}) }()
	var decisionID string
	deadline := time.After(2 * time.Second)
	for decisionID == "" {
		select {
		case <-deadline:
			t.Fatal("approval request not observed")
		default:
			d.approvalMu.Lock()
			for id := range d.pendingApprovals {
				decisionID = id
			}
			d.approvalMu.Unlock()
			time.Sleep(5 * time.Millisecond)
		}
	}
	if _, err := d.handleApprovalResolve(mustJSON(t, map[string]any{"decision_id": decisionID, "approve": true})); err != nil {
		t.Fatal(err)
	}
	if got := <-done; !strings.Contains(got, "applied") {
		t.Fatalf("patch result = %q", got)
	}
	want := []string{"ToolCallRequested", "ToolCallApprovalRequired", "ToolCallStarted", "ToolCallCompleted"}
	if len(types) != len(want) {
		t.Fatalf("tool lifecycle types = %#v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("types[%d]=%s want %s", i, types[i], want[i])
		}
	}
	for _, id := range callIDs {
		if id != callIDs[0] {
			t.Fatalf("call ids differ: %#v", callIDs)
		}
	}
}

func TestBatchToolLifecycleCallIDsAreIsolated(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "batch")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "batch")
	_ = os.WriteFile(filepath.Join(ws, "a.txt"), []byte("a"), 0o600)
	_ = os.WriteFile(filepath.Join(ws, "b.txt"), []byte("b"), 0o600)
	var mu sync.Mutex
	requested := map[string]bool{}
	completed := map[string]bool{}
	d.events.Tap(func(_ string, event map[string]any) {
		payload, _ := event["payload"].(map[string]any)
		id, _ := payload["call_id"].(string)
		if id == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch event["type"] {
		case "ToolCallRequested":
			requested[id] = true
		case "ToolCallCompleted":
			completed[id] = true
		}
	})
	d.executeBatch(sess, task, []action{{Tool: "read", Path: "a.txt"}, {Tool: "read", Path: "b.txt"}})
	if len(requested) != 2 || len(completed) != 2 {
		t.Fatalf("requested=%v completed=%v", requested, completed)
	}
	for id := range requested {
		if !completed[id] {
			t.Fatalf("call %s did not complete independently", id)
		}
	}
}

func TestExecuteActionEmitsAuthoritativeToolLifecycle(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "tool lifecycle")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "read secret")
	if err := os.WriteFile(filepath.Join(ws, "secret.txt"), []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}

	var observed []map[string]any
	d.events.Tap(func(sessionID string, event map[string]any) {
		if sessionID == sess.SessionID {
			observed = append(observed, event)
		}
	})
	if got := d.executeAction(sess, task, &action{Tool: "read", Path: "secret.txt", Content: "must-not-leak"}); got != "value" {
		t.Fatalf("read result = %q", got)
	}

	wantTypes := []string{
		"ToolCallRequested", "RuntimeStageChanged", "ToolCallStarted",
		"RuntimeStageChanged", "FileRead", "ToolCallCompleted", "RuntimeStageChanged",
	}
	if len(observed) != len(wantTypes) {
		t.Fatalf("events = %#v", observed)
	}
	var callID string
	stageSequence := 0
	for i, event := range observed {
		if event["type"] != wantTypes[i] {
			t.Fatalf("event[%d] type = %v, want %s", i, event["type"], wantTypes[i])
		}
		payload, _ := event["payload"].(map[string]any)
		if id, _ := payload["call_id"].(string); id != "" {
			if callID == "" {
				callID = id
			} else if id != callID {
				t.Fatalf("call id changed from %s to %s", callID, id)
			}
		}
		if event["type"] == "RuntimeStageChanged" {
			seq, _ := payload["sequence"].(int)
			if seq <= stageSequence {
				t.Fatalf("stage sequence not monotonic: %d after %d", seq, stageSequence)
			}
			stageSequence = seq
		}
	}
	requested := observed[0]["payload"].(map[string]any)
	args := requested["arguments"].(map[string]any)
	if _, leaked := args["content"]; leaked {
		t.Fatal("sensitive content leaked into lifecycle arguments")
	}
	if args["path"] != "secret.txt" {
		t.Fatalf("redacted args lost safe path: %#v", args)
	}
	completed := observed[5]["payload"].(map[string]any)
	output := completed["output"].(map[string]any)
	if output["bytes"] != 5 || output["sha256"] == "" || output["redacted"] != true {
		t.Fatalf("unsafe/incomplete output metadata: %#v", output)
	}
	if _, leaked := completed["output_preview"]; leaked {
		t.Fatal("raw output preview leaked into authoritative event")
	}
	ids, ok := completed["artifact_ids"].([]string)
	if !ok || len(ids) != 1 {
		t.Fatalf("artifact reference missing: %#v", completed)
	}
	params, _ := json.Marshal(map[string]any{"session_id": sess.SessionID, "task_id": task.TaskID, "call_id": callID, "artifact_id": ids[0]})
	read, err := d.handleArtifactRead(params)
	if err != nil {
		t.Fatal(err)
	}
	encoded := read.(map[string]any)["content_base64"].(string)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || string(raw) != "value" {
		t.Fatalf("artifact=%q err=%v", raw, err)
	}
	audit, _ := d.kern.ReadEvents(sess.SessionID)
	if strings.Contains(string(audit), `"value"`) {
		t.Fatal("tool output leaked into authoritative audit log")
	}
}

func TestCommandOutcomeUsesExitStatusNotDisplayText(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "command outcome")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run false")

	outcome := d.agentRunOutcome(sess, task, []string{"sh", "-c", "printf success-looking; exit 7"})
	if outcome.status != "failed" || outcome.errorCategory != "nonzero_exit" {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestLifecyclePersistenceFailurePreventsPatchSideEffect(t *testing.T) {
	d, ws := newLoopDaemon(t)
	sess, _ := d.store.CreateSession(ws, "strict lifecycle")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "patch")
	path := filepath.Join(ws, "guarded.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.recordRead(sess.SessionID, "guarded.txt", "before\n")
	_ = d.kern.Close()

	got := d.executeAction(sess, task, &action{Tool: "patch", Path: "guarded.txt", Content: "after\n"})
	if !strings.HasPrefix(got, "governance error:") {
		t.Fatalf("expected governance failure, got %q", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "before\n" {
		t.Fatalf("patch ran despite failed lifecycle persistence: %q", raw)
	}
}

func TestTerminalLifecyclePersistenceFailureIsSurfaced(t *testing.T) {
	d, ws := newLoopDaemon(t)
	sess, _ := d.store.CreateSession(ws, "strict terminal lifecycle")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "read")
	if err := os.WriteFile(filepath.Join(ws, "readable.txt"), []byte("observed"), 0o600); err != nil {
		t.Fatal(err)
	}
	closed := false
	d.events.Tap(func(_ string, event map[string]any) {
		if !closed && event["type"] == "FileRead" {
			closed = true
			_ = d.kern.Close()
		}
	})
	got := d.executeAction(sess, task, &action{Tool: "read", Path: "readable.txt"})
	if !strings.HasPrefix(got, "governance error: persist ToolCallCompleted:") {
		t.Fatalf("terminal persistence failure was hidden: %q", got)
	}
}

func TestTerminalRuntimeStagePersistenceFailureIsSurfaced(t *testing.T) {
	d, ws := newLoopDaemon(t)
	sess, _ := d.store.CreateSession(ws, "strict terminal stage")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "read")
	if err := os.WriteFile(filepath.Join(ws, "readable.txt"), []byte("observed"), 0o600); err != nil {
		t.Fatal(err)
	}
	closed := false
	d.events.Tap(func(_ string, event map[string]any) {
		if !closed && event["type"] == "ToolCallCompleted" {
			closed = true
			_ = d.kern.Close()
		}
	})
	got := d.executeAction(sess, task, &action{Tool: "read", Path: "readable.txt"})
	if !strings.Contains(got, "governance error: persist terminal runtime stage") {
		t.Fatalf("terminal stage persistence failure was hidden: %q", got)
	}
}

func TestCancelledBeforeRunContextRegistrationDoesNotStart(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	reasoner := &promptRecordingReasoner{steps: []string{`{"tool":"done","summary":"should not run"}`}}
	d.SetReasoner(reasoner)
	sess, _ := d.store.CreateSession(ws, "cancel before start")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "cancel me")
	if _, err := d.sched.Cancel(task.TaskID); err != nil {
		t.Fatal(err)
	}
	d.withTaskContext(task.TaskID, func(ctx context.Context) {
		d.runTaskContext(ctx, sess, task)
	})
	if len(reasoner.prompts) != 0 {
		t.Fatalf("cancelled task reached reasoner: %#v", reasoner.prompts)
	}
	current, _ := d.sched.Get(task.TaskID)
	if current.Status != "cancelled" {
		t.Fatalf("task status = %s, want cancelled", current.Status)
	}
}

func TestProjectSessionItemsPrefersLifecycleOverLegacyCommand(t *testing.T) {
	events := []itemAuditEvent{
		{EventID: "1", SessionID: "s", TaskID: "t", Type: "ToolCallRequested", Timestamp: "a", Payload: map[string]any{"call_id": "call-1", "tool": "run", "arguments": map[string]any{"executable": "go", "argc": 2}}},
		{EventID: "2", SessionID: "s", TaskID: "t", Type: "RuntimeStageChanged", Timestamp: "b", Payload: map[string]any{"call_id": "call-1", "stage": "tool.requested", "sequence": 1}},
		{EventID: "3", SessionID: "s", TaskID: "t", Type: "ToolCallStarted", Timestamp: "c", Payload: map[string]any{"call_id": "call-1", "tool": "run"}},
		{EventID: "4", SessionID: "s", TaskID: "t", Type: "CommandStarted", Timestamp: "d", Payload: map[string]any{"command_id": "legacy-1", "command": "go test"}},
		{EventID: "5", SessionID: "s", TaskID: "t", Type: "CommandExited", Timestamp: "e", Payload: map[string]any{"command_id": "legacy-1", "exit_code": 0}},
		{EventID: "6", SessionID: "s", TaskID: "t", Type: "ToolCallCompleted", Timestamp: "f", Payload: map[string]any{"call_id": "call-1", "tool": "run", "output_preview": "ok"}},
	}
	items := projectSessionItems("s", events)
	toolEvents, commandEvents, stages := 0, 0, 0
	for _, event := range items {
		if event.Item != nil && event.Item.Type == "tool_call" {
			toolEvents++
		}
		if event.Item != nil && event.Item.Type == "command_execution" {
			commandEvents++
		}
		if event.Type == "runtime.stage_changed" {
			stages++
		}
	}
	if toolEvents != 3 || commandEvents != 0 || stages != 1 {
		t.Fatalf("projection tool=%d command=%d stages=%d: %#v", toolEvents, commandEvents, stages, items)
	}
}

func TestProjectSessionItemsKeepsLegacyCommandWithoutLifecycle(t *testing.T) {
	events := []itemAuditEvent{
		{EventID: "1", SessionID: "s", TaskID: "t", Type: "CommandStarted", Payload: map[string]any{"command_id": "legacy-1"}},
		{EventID: "2", SessionID: "s", TaskID: "t", Type: "CommandExited", Payload: map[string]any{"command_id": "legacy-1", "exit_code": 0}},
	}
	items := projectSessionItems("s", events)
	commandEvents := 0
	for _, event := range items {
		if event.Item != nil && event.Item.Type == "command_execution" {
			commandEvents++
		}
	}
	if commandEvents != 2 {
		t.Fatalf("legacy projection changed: %#v", items)
	}
}

func TestLegacyToolResultsKeepFailureStatus(t *testing.T) {
	tests := []struct {
		output string
		status string
	}{
		{"ok", "completed"},
		{"error: unavailable", "failed"},
		{"workflow failed: step 2", "failed"},
		{"DENIED: policy", "denied"},
		{"requires approval (not granted): risky", "denied"},
	}
	for _, test := range tests {
		if got := classifyLegacyToolResult(test.output); got.status != test.status {
			t.Errorf("%q status = %s, want %s", test.output, got.status, test.status)
		}
	}
}

func TestArtifactReadIsBoundedAndPaged(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "artifact paging")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "read artifact")
	if err := os.WriteFile(filepath.Join(ws, "large.txt"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	var artifactID, callID string
	d.events.Tap(func(sessionID string, event map[string]any) {
		if event["type"] != "ToolCallCompleted" {
			return
		}
		payload := event["payload"].(map[string]any)
		callID, _ = payload["call_id"].(string)
		if ids, ok := payload["artifact_ids"].([]string); ok && len(ids) > 0 {
			artifactID = ids[0]
		}
	})
	if got := d.executeAction(sess, task, &action{Tool: "read", Path: "large.txt"}); got != "0123456789" {
		t.Fatalf("read = %q", got)
	}
	params, _ := json.Marshal(map[string]any{
		"session_id": sess.SessionID, "task_id": task.TaskID, "call_id": callID,
		"artifact_id": artifactID, "offset": 3, "limit": 4,
	})
	got, err := d.handleArtifactRead(params)
	if err != nil {
		t.Fatal(err)
	}
	result := got.(map[string]any)
	chunk, _ := base64.StdEncoding.DecodeString(result["content_base64"].(string))
	if string(chunk) != "3456" || result["eof"] != false {
		t.Fatalf("paged artifact = %#v chunk=%q", result, chunk)
	}
	bad, _ := json.Marshal(map[string]any{"session_id": sess.SessionID, "artifact_id": artifactID, "limit": maxArtifactReadBytes + 1})
	if _, err := d.handleArtifactRead(bad); err == nil {
		t.Fatal("oversized artifact read limit accepted")
	}
}
