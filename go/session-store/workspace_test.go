package sessionstore

import "testing"

func TestWorkspaceScopedSessionCreationPreservesStableIdentity(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const workspaceID = "ws1_test"
	parent, err := store.CreateSessionModeForWorkspace(workspaceID, "/workspace", "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	child, err := store.CreateSubSessionForWorkspace(workspaceID, "/workspace", "read-only", "on_request", parent.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if parent.WorkspaceID != workspaceID || child.WorkspaceID != workspaceID {
		t.Fatalf("workspace ids = parent %q child %q, want %q", parent.WorkspaceID, child.WorkspaceID, workspaceID)
	}
	if child.ParentID != parent.SessionID || child.Depth != 1 {
		t.Fatalf("child lineage = %+v", child)
	}
}

func TestWorkspaceScopedSessionCreationRequiresIdentity(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSessionModeForWorkspace("", "/workspace", "", ""); err == nil {
		t.Fatal("empty workspace id accepted")
	}
}
