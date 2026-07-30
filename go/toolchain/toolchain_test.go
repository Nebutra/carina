package toolchain

import (
	"context"
	"errors"
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

func TestRunContextCancellation(t *testing.T) {
	tc := New(toolsDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tc.RunContext(ctx, []string{"sleep", "30"}, t.TempDir(), time.Minute, nil, false)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunContext error = %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunContext did not stop promptly after cancellation")
	}
}

func TestNewAndDir(t *testing.T) {
	tc := New("/explicit/dir")
	if tc.Dir() != "/explicit/dir" {
		t.Fatalf("Dir mismatch: %s", tc.Dir())
	}
}

func TestNewResolvesPatchNativeDirectoryFromPath(t *testing.T) {
	t.Setenv("CARINA_TOOLS_DIR", "")
	dir := t.TempDir()
	patchNative := filepath.Join(dir, "carina-patch-native")
	if err := os.WriteFile(patchNative, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	tc := New("")
	if tc.Dir() != dir {
		t.Fatalf("Dir = %q, want installed tools directory %q", tc.Dir(), dir)
	}
}

func TestNativeToolsBesideInstalledDaemon(t *testing.T) {
	dir := t.TempDir()
	daemon := filepath.Join(dir, "carina-daemon")
	patchNative := filepath.Join(dir, "carina-patch-native")
	for _, path := range []string{daemon, patchNative} {
		if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got := nativeToolsBeside(daemon); got != dir {
		t.Fatalf("nativeToolsBeside = %q, want %q", got, dir)
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
