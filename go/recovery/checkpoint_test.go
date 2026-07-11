package recovery

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreRequiresConfirmation(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a"), []byte("one"), 0600)
	s := Store{filepath.Join(t.TempDir(), "c")}
	cp, err := s.Create("s", root, "before")
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "a"), []byte("two"), 0600)
	_ = os.WriteFile(filepath.Join(root, "extra"), []byte("x"), 0600)
	if err := Restore(cp, root, false); err == nil {
		t.Fatal("destructive restore accepted without confirmation")
	}
	if err := Restore(cp, root, true); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(root, "a"))
	if string(raw) != "one" {
		t.Fatalf("got %q", raw)
	}
	if _, err := os.Stat(filepath.Join(root, "extra")); !os.IsNotExist(err) {
		t.Fatal("extra file not removed")
	}
}
