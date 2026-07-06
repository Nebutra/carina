package daemon

import (
	"encoding/json"
	"testing"

	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// TestSessionForkLineage: forking a session yields a new session that shares the
// workspace and is linked to the source as its parent.
func TestSessionForkLineage(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	parent, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(parent.SessionID, ws, "safe-edit", nil)

	raw, _ := json.Marshal(map[string]any{"session_id": parent.SessionID})
	res, err := d.handleSessionFork(raw)
	if err != nil {
		t.Fatal(err)
	}
	child := res.(*sessionstore.Session)
	if child.ParentID != parent.SessionID {
		t.Fatalf("fork should link to parent, got parent_id=%q", child.ParentID)
	}
	if child.WorkspaceRoot != ws {
		t.Fatalf("fork should share the workspace, got %q", child.WorkspaceRoot)
	}
	if child.SessionID == parent.SessionID {
		t.Fatal("fork must be a new session")
	}
}
