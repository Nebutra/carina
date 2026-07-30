package daemon

import (
	"encoding/json"
	"testing"

	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func TestSessionArchiveRejectsNonTerminalTaskAndThenCloses(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")
	params, _ := json.Marshal(map[string]any{"session_id": sess.SessionID})

	if _, err := d.handleSessionArchive(params); err == nil {
		t.Fatal("archive accepted a queued task")
	}
	if current, _ := d.store.Get(sess.SessionID); current.Status != "active" {
		t.Fatalf("failed archive changed status to %q", current.Status)
	}

	d.sched.SetStatus(task.RunID, "completed")
	result, err := d.handleSessionArchive(params)
	if err != nil {
		t.Fatal(err)
	}
	archived := result.(*sessionstore.Session)
	if archived.Status != "closed" {
		t.Fatalf("archive status = %q", archived.Status)
	}
	if _, err := d.handleSessionArchive(params); err != nil {
		t.Fatalf("idempotent archive: %v", err)
	}
}

func TestArchiveTerminalTaskStatusFailsClosed(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled", "degraded"} {
		if !archiveTerminalTaskStatus(status) {
			t.Fatalf("terminal status %q rejected", status)
		}
	}
	for _, status := range []string{"", "queued", "running", "paused", "interrupted", "waiting_input", "waiting_approval"} {
		if archiveTerminalTaskStatus(status) {
			t.Fatalf("non-terminal status %q accepted", status)
		}
	}
}
