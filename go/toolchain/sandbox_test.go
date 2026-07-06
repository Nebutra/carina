package toolchain

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestSandboxConfinesWrites: a sandboxed command may write inside the workspace
// (cwd) but not outside it — the syscall-level safety net (macOS sandbox-exec).
func TestSandboxConfinesWrites(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS-only")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec unavailable")
	}
	tc := New(toolsDir(t))
	cwd := t.TempDir()
	outside := filepath.Join(os.Getenv("HOME"), "carina_sbx_test_"+filepath.Base(cwd))
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)

	// A write outside the workspace is blocked.
	escaped := filepath.Join(outside, "escaped.txt")
	if _, err := tc.Run([]string{"touch", escaped}, cwd, 5*time.Second, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(escaped); err == nil {
		t.Fatal("sandbox must block writes outside the workspace")
	}

	// A write inside the workspace is allowed.
	inside := filepath.Join(cwd, "ok.txt")
	if _, err := tc.Run([]string{"touch", inside}, cwd, 5*time.Second, nil, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inside); err != nil {
		t.Fatal("sandbox must allow writes inside the workspace")
	}
}
