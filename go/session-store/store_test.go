package sessionstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateGetListPersist(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.CreateSession("/repo", "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Status != "active" || sess.PermissionProfile != "safe-edit" {
		t.Fatalf("unexpected session: %+v", sess)
	}
	got, ok := s.Get(sess.SessionID)
	if !ok || got.WorkspaceRoot != "/repo" {
		t.Fatalf("Get: %+v ok=%v", got, ok)
	}
	if len(s.List()) != 1 {
		t.Fatal("List should have 1 session")
	}
}

func TestPersistenceAndRecovery(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	sess, _ := s.CreateSession("/repo", "safe-edit")
	closed, _ := s.CreateSession("/repo2", "read-only")
	s.SetStatus(closed.SessionID, "closed")

	// Reopen: sessions must reload; only the active one is recoverable.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get(sess.SessionID); !ok {
		t.Fatal("active session should reload")
	}
	rec := s2.Recoverable()
	if len(rec) != 1 || rec[0].SessionID != sess.SessionID {
		t.Fatalf("only the active session should be recoverable, got %+v", rec)
	}
}

func TestSetStatusUnknown(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, err := s.SetStatus("sess_missing", "closed"); err == nil {
		t.Fatal("SetStatus of unknown session should error")
	}
}

func TestEventLogAppendAndRead(t *testing.T) {
	s, _ := Open(t.TempDir())
	sess, _ := s.CreateSession("/repo", "safe-edit")
	payload, _ := json.Marshal(map[string]string{"k": "v"})
	if err := s.AppendEvent(Event{SessionID: sess.SessionID, Type: "TaskCreated", Payload: payload}); err != nil {
		t.Fatal(err)
	}
	events, err := s.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "TaskCreated" || events[0].EventID == "" {
		t.Fatalf("unexpected events: %+v", events)
	}
	// Unknown session -> empty, no error.
	empty, err := s.ReadEvents("sess_none")
	if err != nil || len(empty) != 0 {
		t.Fatalf("expected empty events, got %+v err=%v", empty, err)
	}
}

func TestCreateSessionDefaultProfile(t *testing.T) {
	s, _ := Open(t.TempDir())
	sess, err := s.CreateSession("/repo", "")
	if err != nil || sess.PermissionProfile != "safe-edit" {
		t.Fatalf("empty profile should default to safe-edit, got %q (%v)", sess.PermissionProfile, err)
	}
}

func TestReadEventsCorrupt(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	sess, _ := s.CreateSession("/repo", "safe-edit")
	// Write a corrupt line directly.
	if err := os.MkdirAll(filepath.Join(dir, "events"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "events", sess.SessionID+".events.jsonl")
	if err := os.WriteFile(path, []byte("{not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadEvents(sess.SessionID); err == nil {
		t.Fatal("corrupt event log should error")
	}
}

func TestLoadsLegacyUnstampedSessionRow(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"session_id": "sess_legacy", "workspace_id": "ws_1", "status": "active", "permission_profile": "safe-edit"}`)
	if err := os.WriteFile(filepath.Join(dir, "sessions", "sess_legacy.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("sess_legacy"); !ok {
		t.Fatal("legacy unstamped session row must still load")
	}
}

func TestPersistStampsSessionVersion(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	sess, err := s.CreateSession("/repo", "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "sessions", sess.SessionID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &row); err != nil || row.Version != SessionVersion {
		t.Fatalf("persisted row version = %d (err=%v), want %d", row.Version, err, SessionVersion)
	}
}

func TestFutureVersionSessionRowQuarantinedNotLoaded(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	future := `{"version": 2, "session_id": "sess_future", "status": "active"}`
	path := filepath.Join(dir, "sessions", "sess_future.json")
	if err := os.WriteFile(path, []byte(future), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("sess_future"); ok {
		t.Fatal("future-version session row must not be trusted")
	}
	moved, err := filepath.Glob(path + ".v2.*.quarantine")
	if err != nil || len(moved) != 1 {
		t.Fatalf("future row must be quarantined, got %v err=%v", moved, err)
	}
	kept, err := os.ReadFile(moved[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != future {
		t.Fatalf("quarantine must preserve original bytes: %s", kept)
	}
}

func TestCorruptSessionRowQuarantinedNotSkipped(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "sessions", "sess_bad.json")
	if err := os.WriteFile(path, []byte(`{"session_id": "sess_bad", "depth": "not-a-number"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	moved, err := filepath.Glob(path + ".v*.quarantine")
	if err != nil || len(moved) != 1 {
		t.Fatalf("corrupt row must be quarantined (not skip-then-overwrite), got %v err=%v", moved, err)
	}
}

func TestLoadIgnoresNonJSON(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	s.CreateSession("/repo", "safe-edit")
	// Drop a stray non-JSON file into the sessions dir; load must ignore it.
	os.WriteFile(filepath.Join(dir, "sessions", "notes.txt"), []byte("hi"), 0o600)
	os.WriteFile(filepath.Join(dir, "sessions", "bad.json"), []byte("{corrupt"), 0o600)
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.List()) != 1 {
		t.Fatalf("expected 1 valid session, got %d", len(s2.List()))
	}
}

func TestAppendEventDirCollision(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	sess, _ := s.CreateSession("/repo", "safe-edit")
	// Occupy the events path with a regular file so MkdirAll fails.
	if err := os.WriteFile(filepath.Join(dir, "events"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(Event{SessionID: sess.SessionID, Type: "TaskCreated"}); err == nil {
		t.Fatal("AppendEvent should error when events dir cannot be created")
	}
}

func TestNewIDPrefix(t *testing.T) {
	id := NewID("sess")
	if len(id) < 6 || id[:5] != "sess_" {
		t.Fatalf("bad id: %s", id)
	}
	if NewID("x") == NewID("x") {
		t.Fatal("ids should be unique")
	}
}
