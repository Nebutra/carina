package daemon

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/history"
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

func TestHistoryRecentScopesToAuthoritativeSessionWorkspace(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	first, _ := d.store.CreateSession(ws, "safe-edit")
	second, _ := d.store.CreateSession(ws, "safe-edit")
	other, _ := d.store.CreateSession(t.TempDir(), "safe-edit")

	for _, entry := range []history.Entry{
		{Text: "first session", SessionID: first.SessionID, WorkspaceRoot: first.WorkspaceRoot},
		{Text: "second session", SessionID: second.SessionID, WorkspaceRoot: second.WorkspaceRoot},
		{Text: "other workspace", SessionID: other.SessionID, WorkspaceRoot: other.WorkspaceRoot},
	} {
		if err := d.history.AppendScoped(entry); err != nil {
			t.Fatal(err)
		}
	}

	workspaceResult, err := d.handleHistoryRecent(mustJSON(t, map[string]any{
		"limit": 10, "scope": "workspace", "session_id": first.SessionID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	workspaceEntries := workspaceResult.(map[string]any)["entries"].([]string)
	if got := strings.Join(workspaceEntries, "\n"); !strings.Contains(got, "first session") ||
		!strings.Contains(got, "second session") || strings.Contains(got, "other workspace") {
		t.Fatalf("workspace history was not isolated: %v", workspaceEntries)
	}

	sessionResult, err := d.handleHistoryRecent(mustJSON(t, map[string]any{
		"limit": 10, "scope": "session", "session_id": first.SessionID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := sessionResult.(map[string]any)["entries"].([]string); len(got) != 1 || got[0] != "first session" {
		t.Fatalf("session history was not isolated: %v", got)
	}
}
