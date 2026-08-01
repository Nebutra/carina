package daemon

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSessionListOrdersMostRecentFirst(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	older, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	// Ensure created_at ordering would put older first if unsorted by recency alone.
	time.Sleep(5 * time.Millisecond)
	newer, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := d.handleSessionList(nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(raw)
	var rows []map[string]any
	if err := json.Unmarshal(encoded, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("expected at least 2 sessions, got %d", len(rows))
	}
	// First row should be the newer session.
	firstID, _ := rows[0]["session_id"].(string)
	if firstID != newer.SessionID {
		t.Fatalf("expected newest session first: first=%s newer=%s older=%s payload=%s", firstID, newer.SessionID, older.SessionID, encoded)
	}
}
