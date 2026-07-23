package localruntime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWorkspaceExplicitCanonicalizesSymlink(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	workspace, err := ResolveWorkspace(link)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.CanonicalRoot != canonical || workspace.ID != WorkspaceID(canonical) {
		t.Fatalf("workspace = %+v, want root %q", workspace, canonical)
	}
}

func TestResolveWorkspaceDiscoversNearestMarker(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(repo, "packages", "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".carina"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, ".carina", "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(nested, "src")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(child); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	workspace, err := ResolveWorkspace("")
	if err != nil {
		t.Fatal(err)
	}
	canonical, _ := filepath.EvalSymlinks(nested)
	if workspace.CanonicalRoot != canonical {
		t.Fatalf("root = %q, want nearest nested marker %q", workspace.CanonicalRoot, canonical)
	}
}

func TestResolveWorkspaceRejectsMissingExplicitPath(t *testing.T) {
	if _, err := ResolveWorkspace(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing explicit workspace accepted")
	}
}

func TestWorkspaceIDDeterministicAndRootSensitive(t *testing.T) {
	a := WorkspaceID("/repo/a")
	if a != WorkspaceID("/repo/a") {
		t.Fatal("workspace id is not deterministic")
	}
	if a == WorkspaceID("/repo/b") {
		t.Fatal("different roots produced the same workspace id")
	}
}
