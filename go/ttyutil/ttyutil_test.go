package ttyutil

import (
	"os"
	"testing"
)

func TestIsTTYFalseForRegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTTY(f) {
		t.Fatal("a regular file must not report as a TTY")
	}
}

func TestIsTTYFalseForPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if IsTTY(r) || IsTTY(w) {
		t.Fatal("a pipe must not report as a TTY")
	}
}
