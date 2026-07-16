package daemon

import (
	"encoding/json"
	"testing"

	"github.com/Nebutra/carina/go/kernel"
)

func TestMemoryControllerReadVerifyRollbackAndConflict(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSessionMode(workspace, "safe-edit", "never")
	d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "never", nil)
	firstAny, err := d.governedMemoryReplace(sess.SessionID, "memory", "", "op-1", "test", []string{"one"})
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(map[string]any)
	secondAny, err := d.governedMemoryReplace(sess.SessionID, "memory", first["revision"].(string), "op-2", "test", []string{"two"})
	if err != nil {
		t.Fatal(err)
	}
	second := secondAny.(map[string]any)
	verified, err := d.handleMemoryVerify(mustJSON(t, map[string]any{"session_id": sess.SessionID, "target": "memory", "revision": second["revision"]}))
	if err != nil || !verified.(map[string]any)["valid"].(bool) {
		t.Fatalf("verify=%v err=%v", verified, err)
	}
	rolled, err := d.handleMemoryRollback(mustJSON(t, map[string]any{"session_id": sess.SessionID, "target": "memory", "revision": first["revision"], "expected_revision": second["revision"], "idempotency_key": "rollback-1"}))
	if err != nil {
		t.Fatal(err)
	}
	if rolled.(map[string]any)["revision"] != first["revision"] {
		t.Fatalf("rollback=%v", rolled)
	}
	if _, err := d.governedMemoryReplace(sess.SessionID, "memory", second["revision"].(string), "op-conflict", "test", []string{"three"}); err == nil {
		t.Fatal("stale expected revision was accepted")
	}
}

func TestMemoryControllerHandoffRequiresExternalizeAndCopiesAuthority(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	source, _ := d.store.CreateSessionMode(workspace, "safe-edit", "never")
	target, _ := d.store.CreateSessionMode(t.TempDir(), "safe-edit", "never")
	d.kern.InitSessionFull(source.SessionID, workspace, "safe-edit", "never", nil)
	d.kern.InitSessionFull(target.SessionID, target.WorkspaceRoot, "safe-edit", "never", nil)
	if _, err := d.governedMemoryReplace(source.SessionID, "memory", "", "source-1", "test", []string{"handoff fact"}); err != nil {
		t.Fatal(err)
	}
	out, err := d.handleMemoryHandoff(mustJSON(t, map[string]any{"source_session_id": source.SessionID, "target_session_id": target.SessionID, "target": "memory", "idempotency_key": "handoff-1"}))
	if err != nil {
		t.Fatal(err)
	}
	if decision, pending := out.(map[string]any)["decision"]; pending {
		t.Fatalf("handoff was not authorized: %v", decision)
	}
	read, err := d.memoryRead(target.SessionID, "memory")
	if err != nil {
		t.Fatal(err)
	}
	entries := read["entries"].([]string)
	if len(entries) != 1 || entries[0] != "handoff fact" {
		t.Fatalf("target entries=%v", entries)
	}
}

func TestMemoryControllerRecoversPreparedRevisionByAuditOutcome(t *testing.T) {
	stateDir := t.TempDir()
	d := newDaemonAt(t, stateDir)
	sess, _ := d.store.CreateSession(t.TempDir(), "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, sess.WorkspaceRoot, "safe-edit", nil)
	scope := memoryScopeFromSession(sess)
	_ = d.memory.restore(scope, "memory", []string{"old"})
	pending, _ := d.memoryVersions.prepare(sess.SessionID, scope, "memory", []string{"uncommitted"}, []string{"old"}, "test", "")
	_ = d.memory.restore(scope, "memory", []string{"uncommitted"})
	_ = pending
	_ = d.Close()
	d = newDaemonAt(t, stateDir)
	state, _ := d.memory.list(scope, "memory")
	if len(state.Entries) != 1 || state.Entries[0] != "old" {
		t.Fatalf("uncommitted recovery=%v", state.Entries)
	}
	_ = d.memory.restore(scope, "memory", []string{"committed"})
	committed, _ := d.memoryVersions.prepare(sess.SessionID, scope, "memory", []string{"committed"}, []string{"old"}, "test", "")
	if err := d.recordChecked(sess.SessionID, "MemoryWritten", "", "go", map[string]any{"target": "memory", "revision": committed.Revision, "status": "committed"}, ""); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	d = newDaemonAt(t, stateDir)
	defer d.Close()
	state, _ = d.memory.list(scope, "memory")
	if len(state.Entries) != 1 || state.Entries[0] != "committed" {
		t.Fatalf("committed recovery=%v", state.Entries)
	}
	if row, ok := d.memoryVersions.find(scope, "memory", committed.Revision); !ok || !row.Published {
		t.Fatalf("revision not published after recovery: %+v", row)
	}
}

func TestMemoryRevisionCommittedRequiresStructuredCommitEvent(t *testing.T) {
	revision := "memrev_123"
	tests := []struct {
		name   string
		events []map[string]any
		want   bool
	}{
		{name: "committed", events: []map[string]any{{"type": "MemoryWritten", "payload": map[string]any{"revision": revision, "status": "committed"}}}, want: true},
		{name: "successful write", events: []map[string]any{{"type": "MemoryWritten", "payload": map[string]any{"revision": revision, "success": true}}}, want: true},
		{name: "revision only mentioned in another event", events: []map[string]any{{"type": "TaskCompleted", "payload": map[string]any{"message": "published " + revision}}}},
		{name: "write was not committed", events: []map[string]any{{"type": "MemoryWritten", "payload": map[string]any{"revision": revision, "status": "failed"}}}},
		{name: "different revision", events: []map[string]any{{"type": "MemoryWritten", "payload": map[string]any{"revision": "memrev_other", "status": "committed"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.events)
			if err != nil {
				t.Fatal(err)
			}
			if got := memoryRevisionCommitted(raw, revision); got != tt.want {
				t.Fatalf("memoryRevisionCommitted() = %v, want %v", got, tt.want)
			}
		})
	}
	if memoryRevisionCommitted([]byte("not-json "+revision), revision) {
		t.Fatal("malformed audit log was accepted")
	}
}

func TestMemoryRollbackApprovalResumesFrozenOperation(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	d.kern.InitSessionFull(sess.SessionID, workspace, "safe-edit", "on_request", nil)
	scope := memoryScopeFromSession(sess)
	if err := d.memory.restore(scope, "memory", []string{"first"}); err != nil {
		t.Fatal(err)
	}
	first, err := d.memoryVersions.prepare(sess.SessionID, scope, "memory", []string{"first"}, nil, "seed", "seed-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.memoryVersions.publish(first.DocumentID, first.Revision); err != nil {
		t.Fatal(err)
	}
	if err := d.memory.restore(scope, "memory", []string{"second"}); err != nil {
		t.Fatal(err)
	}
	second, err := d.memoryVersions.prepare(sess.SessionID, scope, "memory", []string{"second"}, []string{"first"}, "seed", "seed-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.memoryVersions.publish(second.DocumentID, second.Revision); err != nil {
		t.Fatal(err)
	}

	out, err := d.handleMemoryRollback(mustJSON(t, map[string]any{"session_id": sess.SessionID, "target": "memory", "revision": first.Revision, "expected_revision": second.Revision, "idempotency_key": "rollback-approved"}))
	if err != nil {
		t.Fatal(err)
	}
	decision := out.(map[string]any)["decision"].(*kernel.Decision)
	if decision.Decision != "requires_approval" {
		t.Fatalf("decision=%+v", decision)
	}
	approved, err := d.handleApprove(mustJSON(t, map[string]any{"session_id": sess.SessionID, "decision_id": decision.DecisionID, "approver": "operator"}))
	if err != nil {
		t.Fatal(err)
	}
	if approved.(map[string]any)["result"] == nil {
		t.Fatalf("approval did not resume rollback: %#v", approved)
	}
	read, err := d.memoryRead(sess.SessionID, "memory")
	if err != nil {
		t.Fatal(err)
	}
	entries := read["entries"].([]string)
	if len(entries) != 1 || entries[0] != "first" {
		t.Fatalf("entries after approval=%v", entries)
	}
}

func TestMemoryHandoffApprovalPreservesIndependentWriteGate(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	source, _ := d.store.CreateSessionMode(workspace, "safe-edit", "on_request")
	target, _ := d.store.CreateSessionMode(t.TempDir(), "safe-edit", "on_request")
	d.kern.InitSessionFull(source.SessionID, source.WorkspaceRoot, "safe-edit", "on_request", nil)
	d.kern.InitSessionFull(target.SessionID, target.WorkspaceRoot, "safe-edit", "on_request", nil)
	if err := d.memory.restore(memoryScopeFromSession(source), "memory", []string{"shared fact"}); err != nil {
		t.Fatal(err)
	}

	out, err := d.handleMemoryHandoff(mustJSON(t, map[string]any{"source_session_id": source.SessionID, "target_session_id": target.SessionID, "target": "memory", "idempotency_key": "handoff-approved"}))
	if err != nil {
		t.Fatal(err)
	}
	externalize := out.(map[string]any)["decision"].(*kernel.Decision)
	if externalize.Capability != "MemoryExternalize" || externalize.Decision != "requires_approval" {
		t.Fatalf("externalize decision=%+v", externalize)
	}
	firstApproval, err := d.handleApprove(mustJSON(t, map[string]any{"session_id": source.SessionID, "decision_id": externalize.DecisionID, "approver": "operator"}))
	if err != nil {
		t.Fatal(err)
	}
	writeResult := firstApproval.(map[string]any)["result"].(map[string]any)
	write := writeResult["decision"].(*kernel.Decision)
	if write.Capability != "MemoryWrite" || write.Decision != "requires_approval" {
		t.Fatalf("write decision=%+v", write)
	}
	if _, err := d.handleApprove(mustJSON(t, map[string]any{"session_id": target.SessionID, "decision_id": write.DecisionID, "approver": "operator"})); err != nil {
		t.Fatal(err)
	}
	read, err := d.memoryRead(target.SessionID, "memory")
	if err != nil {
		t.Fatal(err)
	}
	entries := read["entries"].([]string)
	if len(entries) != 1 || entries[0] != "shared fact" {
		t.Fatalf("target entries=%v", entries)
	}
}
