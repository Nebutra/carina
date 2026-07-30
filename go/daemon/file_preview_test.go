package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWorkspacePreviewPathRejectsEscapeAndSymlink(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(inside), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inside, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveWorkspacePreviewPath(root, "src/main.go")
	want, canonicalErr := filepath.EvalSymlinks(inside)
	if err != nil || canonicalErr != nil || got != want {
		t.Fatalf("resolve inside = %q, %v", got, err)
	}
	for _, path := range []string{"../secret", "/etc/passwd", "src/../src/main.go", " src/main.go"} {
		if _, err := resolveWorkspacePreviewPath(root, path); err == nil {
			t.Fatalf("resolveWorkspacePreviewPath(%q) unexpectedly succeeded", path)
		}
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveWorkspacePreviewPath(root, "link"); err == nil {
		t.Fatal("symlink escape unexpectedly succeeded")
	}
}

func TestReadWorkspacePreviewRejectsLargeAndBinaryFiles(t *testing.T) {
	root := t.TempDir()
	large := filepath.Join(root, "large.txt")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", maxWorkspaceFilePreviewBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkspacePreview(large); err == nil {
		t.Fatal("large preview unexpectedly succeeded")
	}
	binary := filepath.Join(root, "binary.dat")
	if err := os.WriteFile(binary, []byte{'a', 0, 'b'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkspacePreview(binary); err == nil {
		t.Fatal("binary preview unexpectedly succeeded")
	}
}
