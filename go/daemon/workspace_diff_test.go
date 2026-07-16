package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestCollectWorkspaceDiffTrackedUntrackedBinaryAndNoIndexWrite(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "config", "user.email", "test@example.com")
	gitTest(t, root, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "add", "tracked.txt")
	gitTest(t, root, "commit", "-qm", "base")
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("after\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("hello\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{'a', 0, 'b'}, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-secret")
	if err := os.WriteFile(outside, []byte("must-not-follow"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, ".git", "index")
	before, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	res, err := collectWorkspaceDiff(root)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() {
		t.Fatal("read-only diff mutated the git index")
	}
	seen := map[string]workspaceDiffFile{}
	for _, f := range res.Files {
		seen[f.Path] = f
	}
	if !strings.Contains(seen["tracked.txt"].Diff, "-before") || !strings.Contains(seen["tracked.txt"].Diff, "+after") {
		t.Fatalf("tracked diff=%q", seen["tracked.txt"].Diff)
	}
	if !seen["new.txt"].Untracked || !strings.Contains(seen["new.txt"].Diff, "+hello") {
		t.Fatalf("untracked=%+v", seen["new.txt"])
	}
	if !seen["binary.bin"].Binary || seen["binary.bin"].Diff != "" {
		t.Fatalf("binary leaked: %+v", seen["binary.bin"])
	}
	if strings.Contains(seen["link"].Diff, "must-not-follow") {
		t.Fatalf("untracked symlink was followed: %+v", seen["link"])
	}
}

func TestCollectWorkspaceDiffCapsLargeUntrackedFile(t *testing.T) {
	root := t.TempDir()
	gitTest(t, root, "init", "-q")
	large := strings.Repeat("x", workspaceDiffFileLimit+1024)
	if err := os.WriteFile(filepath.Join(root, "large.txt"), []byte(large), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := collectWorkspaceDiff(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || !res.Files[0].Truncated || len(res.Files[0].Diff) > workspaceDiffFileLimit {
		t.Fatalf("limit failed: %+v", res)
	}
}
