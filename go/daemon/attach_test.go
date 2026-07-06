package daemon

import (
	"encoding/json"
	"testing"
)

// TestSessionAttachCursor: attach catches up from a cursor and, on re-attach,
// returns only the events appended since — the reconnect/tail primitive.
func TestSessionAttachCursor(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	attach := func(since int) (int, int) {
		t.Helper()
		res, err := d.handleSessionAttach(mustJSON(t, map[string]any{
			"session_id": sess.SessionID, "since": since}))
		if err != nil {
			t.Fatalf("attach(since=%d): %v", since, err)
		}
		m := res.(map[string]any)
		return len(m["events"].([]json.RawMessage)), m["cursor"].(int)
	}

	// Initial catch-up establishes a cursor.
	_, cur0 := attach(0)

	// Append two events.
	d.record(sess.SessionID, "TaskCreated", "t1", "go", map[string]any{"n": 1}, "")
	d.record(sess.SessionID, "TaskCreated", "t2", "go", map[string]any{"n": 2}, "")

	// Re-attaching from the old cursor yields exactly the two new events.
	n, cur1 := attach(cur0)
	if n != 2 {
		t.Fatalf("expected 2 new events after cursor, got %d", n)
	}
	if cur1 != cur0+2 {
		t.Fatalf("cursor should advance by 2: %d -> %d", cur0, cur1)
	}

	// Caught up: attaching from the latest cursor yields nothing.
	n, cur2 := attach(cur1)
	if n != 0 || cur2 != cur1 {
		t.Fatalf("caught-up attach should be empty and stable, got n=%d cursor=%d", n, cur2)
	}

	// A cursor beyond the log is clamped (no negative slice), not an error.
	if n, _ := attach(cur1 + 1000); n != 0 {
		t.Fatalf("over-far cursor should yield 0 events, got %d", n)
	}
}
