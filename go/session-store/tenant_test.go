package sessionstore

import "testing"

func TestSharedWorkspaceRootAcrossTenantsRejected(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSessionModeForTenant("org_a", "/repo", "safe-edit", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSessionModeForTenant("org_b", "/repo", "safe-edit", ""); err == nil {
		t.Fatal("two tenants must not share a workspace_root")
	}
	if _, err := s.CreateSessionModeForTenant("org_a", "/repo", "safe-edit", ""); err != nil {
		t.Fatalf("same tenant may reuse a workspace: %v", err)
	}
}

func TestVisibleHidesForeignTenant(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateSessionModeForTenant("org_a", "/a", "safe-edit", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Visible(a.SessionID, "org_b"); ok {
		t.Fatal("foreign tenant must not see the session")
	}
	got, ok := s.Visible(a.SessionID, "org_a")
	if !ok || got.SessionID != a.SessionID {
		t.Fatalf("own tenant must see the session: ok=%v got=%+v", ok, got)
	}
	if len(s.ListByTenant("org_b")) != 0 || len(s.ListByTenant("org_a")) != 1 {
		t.Fatalf("list leaked tenants: b=%d a=%d", len(s.ListByTenant("org_b")), len(s.ListByTenant("org_a")))
	}
}

func TestPathOwnedByOtherTenant(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSessionModeForTenant("org_a", "/tenants/a", "safe-edit", ""); err != nil {
		t.Fatal(err)
	}
	if !s.PathOwnedByOtherTenant("org_b", "/tenants/a/src/main.go") {
		t.Fatal("org_b must not be granted org_a's tree")
	}
	if s.PathOwnedByOtherTenant("org_a", "/tenants/a/src/main.go") {
		t.Fatal("org_a owns its own tree")
	}
}
