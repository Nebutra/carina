package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	run(t, d, "init", "-q")
	run(t, d, "config", "user.email", "test@example.com")
	run(t, d, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(d, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, d, "add", "a.txt")
	run(t, d, "commit", "-qm", "base")
	return d
}
func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

func TestLifecycleAndRecovery(t *testing.T) {
	r := repo(t)
	state := t.TempDir()
	m, _ := New(state)
	rec, err := m.Create("task-1", r, "HEAD", "feature/task-1", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rec.Path); err != nil {
		t.Fatal(err)
	}
	recovered, _ := New(state)
	got, err := recovered.Get("task-1")
	if err != nil || got.Commit == "" {
		t.Fatalf("recovery: %+v %v", got, err)
	}
	if err := recovered.Cleanup("task-1", "session-1", false); err != nil {
		t.Fatal(err)
	}
}
func TestDirtyCleanupAndLockConflict(t *testing.T) {
	r := repo(t)
	m, _ := New(t.TempDir())
	rec, err := m.Create("task-2", r, "HEAD", "", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Lock(rec.ID, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Lock(rec.ID, "two"); err == nil {
		t.Fatal("expected lock conflict")
	}
	if err := os.WriteFile(filepath.Join(rec.Path, "dirty"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Cleanup(rec.ID, "one", false); err == nil {
		t.Fatal("expected dirty refusal")
	}
	if err := m.Cleanup(rec.ID, "one", true); err != nil {
		t.Fatal(err)
	}
}
func TestRejectsTraversalAndBadRef(t *testing.T) {
	r := repo(t)
	m, _ := New(t.TempDir())
	if _, err := m.Create("../escape", r, "HEAD", "", "x"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := m.Create("badref", r, "refs/heads/missing", "", "x"); err == nil {
		t.Fatal("expected ref rejection")
	}
}
