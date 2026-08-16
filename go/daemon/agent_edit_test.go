package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func readAuditEvents(t *testing.T, d *Daemon, sessionID string) []map[string]any {
	t.Helper()
	raw, err := d.kern.ReadEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var evs []map[string]any
	if err := json.Unmarshal(raw, &evs); err != nil {
		t.Fatal(err)
	}
	return evs
}

func countEventType(evs []map[string]any, typ string) int {
	n := 0
	for _, event := range evs {
		if event["type"] == typ {
			n++
		}
	}
	return n
}

func firstPatchID(evs []map[string]any) string {
	for _, event := range evs {
		if event["type"] != "PatchProposed" && event["type"] != "PatchApplied" {
			continue
		}
		payload, _ := event["payload"].(map[string]any)
		if id, _ := payload["patch_id"].(string); id != "" {
			return id
		}
	}
	return ""
}

func TestAgentLoopEditUniqueSpanAppliesThroughPatch(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	if !d.tools.Available() {
		t.Skip("zig tools not built")
	}
	target := filepath.Join(ws, "hello.txt")
	if err := os.WriteFile(target, []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"read","path":"hello.txt","intent":"inspect"}`,
		`{"tool":"edit","path":"hello.txt","old":"hello","new":"hello\n// added","intent":"annotate"}`,
		`{"tool":"done","summary":"annotated hello.txt"}`,
	}})
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "annotate hello.txt")
	d.runTask(sess, task)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello\n// added\n" {
		t.Fatalf("file = %q", got)
	}
	evs := readAuditEvents(t, d, sess.SessionID)
	if countEventType(evs, "PatchProposed") == 0 || countEventType(evs, "PatchApplied") == 0 {
		t.Fatalf("edit must produce patch events, saw %v", evs)
	}
	patchID := firstPatchID(evs)
	if patchID == "" {
		t.Fatal("missing patch_id")
	}
	if _, err := d.handlePatchRollbackPreview(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "patch_id": patchID,
	})); err != nil {
		t.Fatalf("rollback preview: %v", err)
	}
	if _, err := d.handlePatchRollback(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "patch_id": patchID,
	})); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "hello\n" {
		t.Fatalf("rollback file = %q", restored)
	}
}

func TestAgentLoopEditDuplicateSpanLeavesWorkspaceUnchanged(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	target := filepath.Join(ws, "dup.txt")
	if err := os.WriteFile(target, []byte("foo foo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"read","path":"dup.txt","intent":"inspect"}`,
		`{"tool":"edit","path":"dup.txt","old":"foo","new":"bar","intent":"rename"}`,
		`{"tool":"done","summary":"could not edit"}`,
	}})
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "rename foo")
	d.runTask(sess, task)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "foo foo\n" {
		t.Fatalf("duplicate span mutated workspace: %q", got)
	}
	if countEventType(readAuditEvents(t, d, sess.SessionID), "PatchProposed") != 0 {
		t.Fatal("non-unique edit must not propose a patch")
	}
}

func TestAgentLoopEditUnreadAndStaleRefuse(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	target := filepath.Join(ws, "stale.txt")
	if err := os.WriteFile(target, []byte("orig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"edit","path":"stale.txt","old":"orig","new":"next","intent":"blind"}`,
		`{"tool":"read","path":"stale.txt","intent":"inspect"}`,
		`{"tool":"done","summary":"stopped after unread"}`,
	}})
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "blind edit")
	d.runTask(sess, task)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "orig\n" {
		t.Fatalf("unread edit mutated workspace: %q", got)
	}
	if countEventType(readAuditEvents(t, d, sess.SessionID), "PatchProposed") != 0 {
		t.Fatal("unread edit must not propose a patch")
	}
}

func TestAgentLoopEditStaleSinceLastRead(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	target := filepath.Join(ws, "live.txt")
	if err := os.WriteFile(target, []byte("orig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&staleAfterReadReasoner{path: target, next: []byte("drifted\n")})
	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "edit after drift")
	d.runTask(sess, task)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "drifted\n" {
		t.Fatalf("stale edit should leave the drifted bytes, got %q", got)
	}
	if countEventType(readAuditEvents(t, d, sess.SessionID), "PatchProposed") != 0 {
		t.Fatal("stale edit must not propose a patch")
	}
}

type staleAfterReadReasoner struct {
	path string
	next []byte
	n    int
}

func (r *staleAfterReadReasoner) Name() string { return "stale-after-read" }
func (r *staleAfterReadReasoner) Think(_ context.Context, _ string) (string, error) {
	r.n++
	switch r.n {
	case 1:
		return `{"tool":"read","path":"live.txt","intent":"inspect"}`, nil
	case 2:
		if err := os.WriteFile(r.path, r.next, 0o600); err != nil {
			return "", err
		}
		return `{"tool":"edit","path":"live.txt","old":"orig","new":"next","intent":"after drift"}`, nil
	default:
		return `{"tool":"done","summary":"stale refused"}`, nil
	}
}
