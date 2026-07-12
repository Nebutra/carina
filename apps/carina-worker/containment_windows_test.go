//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestRuntimeProcessTreeContainmentAdvertisesJobGuard(t *testing.T) {
	if got := runtimeProcessTreeContainment(); got != "windows_job_v1" {
		t.Fatalf("runtimeProcessTreeContainment() = %q", got)
	}
}

func TestCommandExecutorCancellationKillsWindowsJobDescendants(t *testing.T) {
	t.Setenv("GO_WANT_CARINA_EXECUTOR_HELPER", "1")
	pidFile := t.TempDir() + "/child.pid"
	t.Setenv("CARINA_EXECUTOR_CHILD_PID_FILE", pidFile)
	executor := newCommandExecutor(os.Args[0], []string{"-test.run=TestCarinaExecutorHelperProcess", "--", "descendant"})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan executionResult, 1)
	go func() { done <- executor.Execute(ctx, json.RawMessage(`{"task_id":"task_windows_job"}`)) }()

	childPID := waitForWindowsChildPID(t, pidFile)
	cancel()
	select {
	case result := <-done:
		if result.Status != "failed" || !strings.Contains(result.Summary, "cancelled") {
			t.Fatalf("cancel result = %+v", result)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("executor Job did not terminate")
	}

	handle, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(childPID))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	result, err := windows.WaitForSingleObject(handle, 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if result != windows.WAIT_OBJECT_0 {
		t.Fatalf("executor descendant pid %d survived Job cancellation", childPID)
	}
}

func waitForWindowsChildPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("executor descendant pid was not published")
	return 0
}
