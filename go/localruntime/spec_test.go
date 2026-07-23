package localruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testWorkspace(t *testing.T) Workspace {
	t.Helper()
	workspace, err := ResolveWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return workspace
}

func TestEnsureSpecPersistsRuntimeIDAndUpdatesConfig(t *testing.T) {
	home := t.TempDir()
	workspace := testWorkspace(t)
	first, err := EnsureSpec(home, workspace, SpecOptions{
		Config:    ConfigIdentity{Fingerprint: "one", Sources: map[string]string{"socket": "default"}},
		IdleGrace: 5 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureSpec(home, workspace, SpecOptions{
		Config:    ConfigIdentity{Fingerprint: "two", Sources: map[string]string{"socket": "env"}},
		IdleGrace: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.RuntimeID != second.RuntimeID {
		t.Fatalf("runtime id changed: %s -> %s", first.RuntimeID, second.RuntimeID)
	}
	if second.Config.Fingerprint != "two" || second.IdleGraceMS != time.Minute.Milliseconds() {
		t.Fatalf("updated spec = %+v", second)
	}
	if info, err := os.Stat(second.Paths.RuntimeDir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("runtime dir mode = %v err=%v", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(second.Paths.SpecPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("spec mode = %v err=%v", info.Mode().Perm(), err)
	}
}

func TestEnsureSpecRejectsWorkspaceMismatch(t *testing.T) {
	home := t.TempDir()
	first := testWorkspace(t)
	spec, err := EnsureSpec(home, first, SpecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second := testWorkspace(t)
	paths := spec.Paths
	if _, err := EnsureSpec(home, second, SpecOptions{Paths: paths}); err == nil || !strings.Contains(err.Error(), "workspace mismatch") {
		t.Fatalf("workspace mismatch err = %v", err)
	}
}

func TestLoadSpecRejectsUnsafeSymlink(t *testing.T) {
	home := t.TempDir()
	workspace := testWorkspace(t)
	spec, err := EnsureSpec(home, workspace, SpecOptions{})
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(spec.Paths.RuntimeDir, "real-spec.json")
	if err := os.Rename(spec.Paths.SpecPath, real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, spec.Paths.SpecPath); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpec(spec.Paths.SpecPath); err == nil || !strings.Contains(err.Error(), "unsafe metadata") {
		t.Fatalf("unsafe symlink err = %v", err)
	}
}

func TestLoadSpecRejectsFutureVersionWithoutMutatingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "spec.json")
	raw := []byte(`{"version":2,"mode":"workspace"}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSpec(path); err == nil {
		t.Fatal("future spec version accepted")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("future spec was mutated: %q err=%v", got, err)
	}
}

func TestResolveUsesWorkspaceDefaultsAndPreservesOverrides(t *testing.T) {
	home := t.TempDir()
	workspaceRoot := t.TempDir()
	resolved, err := ResolveWithManaged(home, workspaceRoot, ModeWorkspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Config.Socket != resolved.Spec.Paths.SocketPath || resolved.Config.StateDir != resolved.Spec.Paths.StateDir {
		t.Fatalf("config/spec paths diverged: config=%+v spec=%+v", resolved.Config, resolved.Spec.Paths)
	}
	if resolved.Provenance.KeySources["socket"] != "workspace_default" || resolved.Provenance.KeySources["state_dir"] != "workspace_default" {
		t.Fatalf("provenance = %+v", resolved.Provenance)
	}

	t.Setenv("CARINA_SOCKET", filepath.Join(home, "custom.sock"))
	t.Setenv("CARINA_STATE_DIR", filepath.Join(home, "custom-state"))
	overridden, err := ResolveWithManaged(home, workspaceRoot, ModeWorkspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if overridden.Config.Socket != filepath.Join(home, "custom.sock") || overridden.Config.StateDir != filepath.Join(home, "custom-state") {
		t.Fatalf("overrides lost: %+v", overridden.Config)
	}
	if overridden.Provenance.KeySources["socket"] != "environment" || overridden.Provenance.KeySources["state_dir"] != "environment" {
		t.Fatalf("override provenance = %+v", overridden.Provenance)
	}
}

func TestDefaultSocketPathUsesBoundedWorkspaceKey(t *testing.T) {
	home := filepath.Join(string(os.PathSeparator), "Users", "carina-test")
	workspace := Workspace{
		ID:            WorkspaceID("/a/very/long/workspace/path/that/must/not/appear/in/the/socket/name"),
		CanonicalRoot: "/a/very/long/workspace/path/that/must/not/appear/in/the/socket/name",
	}
	paths := DefaultPaths(home, workspace)
	base := filepath.Base(paths.SocketPath)
	if len(base) != len("rt1_")+24+len(".sock") {
		t.Fatalf("socket base %q is not bounded", base)
	}
	if strings.Contains(paths.SocketPath, workspace.ID) {
		t.Fatalf("socket path contains the full workspace ID: %s", paths.SocketPath)
	}
	if len(paths.SocketPath) >= 104 {
		t.Fatalf("macOS-compatible socket path is too long: %d bytes: %s", len(paths.SocketPath), paths.SocketPath)
	}
}
