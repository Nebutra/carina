package daemon

import (
	"strings"
	"testing"
)

func TestHistoryRecentAndSubmitAppends(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)

	// A direct entry and a submitted prompt both land in the shared history.
	if err := d.history.Append("earlier prompt"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "prompt": "build the widget"})); err != nil {
		t.Fatal(err)
	}

	res, err := d.handleHistoryRecent(mustJSON(t, map[string]any{"limit": 10}))
	if err != nil {
		t.Fatal(err)
	}
	entries := res.(map[string]any)["entries"].([]string)
	joined := strings.Join(entries, "\n")
	if !strings.Contains(joined, "earlier prompt") || !strings.Contains(joined, "build the widget") {
		t.Fatalf("shared history missing entries: %v", entries)
	}
}

func TestHistoryRecentEmpty(t *testing.T) {
	d, _ := newLoopDaemon(t)
	defer d.Close()
	res, err := d.handleHistoryRecent(nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["count"] != 0 {
		t.Fatalf("fresh history should be empty, got %v", res)
	}
}
