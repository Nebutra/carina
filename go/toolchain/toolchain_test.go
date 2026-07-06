package toolchain

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func toolsDir(t *testing.T) string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	dir := filepath.Join(root, "zig", "zig-out", "bin")
	if _, err := os.Stat(filepath.Join(dir, "carina-scan")); err != nil {
		t.Skip("zig tools not built")
	}
	return dir
}

func TestNewAndDir(t *testing.T) {
	tc := New("/explicit/dir")
	if tc.Dir() != "/explicit/dir" {
		t.Fatalf("Dir mismatch: %s", tc.Dir())
	}
}

func TestScanGrepRun(t *testing.T) {
	tc := New(toolsDir(t))
	if !tc.Available() {
		t.Fatal("tools should be available")
	}
	ws := t.TempDir()
	os.WriteFile(filepath.Join(ws, "a.go"), []byte("package p\n// TODO here\n"), 0o600)

	files, err := tc.Scan(ws)
	if err != nil || len(files) == 0 {
		t.Fatalf("scan: %v files=%d", err, len(files))
	}

	matches, err := tc.Grep("TODO", ws)
	if err != nil || len(matches) == 0 {
		t.Fatalf("grep: %v matches=%d", err, len(matches))
	}
	if matches[0].Line == 0 {
		t.Fatal("grep match should carry a line number")
	}

	res, err := tc.Run([]string{"echo", "hello"}, ws, 5*time.Second, nil, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("echo should exit 0, got %d", res.ExitCode)
	}
}

func TestRunTimeout(t *testing.T) {
	tc := New(toolsDir(t))
	res, err := tc.Run([]string{"sleep", "5"}, t.TempDir(), 300*time.Millisecond, nil, false)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.TimedOut {
		t.Fatal("expected timeout")
	}
}
