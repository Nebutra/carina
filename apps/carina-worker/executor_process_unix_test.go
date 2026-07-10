//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCommandExecutorCancellationKillsProcessGroup(t *testing.T) {
	t.Setenv("GO_WANT_CARINA_EXECUTOR_HELPER", "1")
	pidFile := t.TempDir() + "/child.pid"
	t.Setenv("CARINA_EXECUTOR_CHILD_PID_FILE", pidFile)
	executor := newCommandExecutor(os.Args[0], []string{"-test.run=TestCarinaExecutorHelperProcess", "--", "descendant"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan executionResult, 1)
	go func() {
		done <- executor.Execute(ctx, json.RawMessage(`{"task_id":"task_process_group"}`))
	}()

	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(raw)))
			if err == nil && childPID > 0 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		cancel()
		t.Fatal("executor descendant pid was not published")
	}
	cancel()
	select {
	case result := <-done:
		if result.Status != "failed" || !strings.Contains(result.Summary, "cancelled") {
			t.Fatalf("cancel result = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor process group did not terminate")
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("executor descendant pid %d survived cancellation", childPID)
}
