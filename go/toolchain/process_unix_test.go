//go:build unix

package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunContextCancellationKillsChildProcessGroup(t *testing.T) {
	tc := New(toolsDir(t))
	marker := filepath.Join(t.TempDir(), "child-survived")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := tc.RunContext(ctx, []string{"sh", "-c", "(sleep 1; touch '" + marker + "') & wait"}, t.TempDir(), time.Minute, nil, false)
		done <- err
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled command did not exit")
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child process survived cancellation and created %s", marker)
	}
}
