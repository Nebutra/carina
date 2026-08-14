//go:build windows

package daemon

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestGrokWindowsJobTerminationKillsDescendants(t *testing.T) {
	pidFile := t.TempDir() + `\child.pid`
	cmd := exec.Command(os.Args[0], "-test.run=^TestGrokWindowsProcessTreeHelper$", "--", "parent")
	cmd.Env = append(os.Environ(),
		"GO_WANT_GROK_WINDOWS_HELPER=1",
		"CARINA_GROK_WINDOWS_CHILD_PID_FILE="+pidFile,
	)
	if err := startGrokCLIReasonerCommand(cmd); err != nil {
		t.Fatal(err)
	}
	defer releaseGrokCLIReasonerCommand(cmd)

	childPID := waitForGrokWindowsChildPID(t, pidFile)
	if err := terminateGrokCLIReasonerCommand(cmd); err != nil {
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	if waitErr == nil {
		t.Fatal("terminated Grok Build fixture exited successfully")
	}
	if !grokProcessWasKilledByCarina(nil, waitErr) {
		t.Fatalf("Job termination was not recognized: %v", waitErr)
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
		t.Fatalf("Grok Build descendant pid %d survived Job termination", childPID)
	}
}

func TestGrokWindowsNaturalExitOneIsNotCarinaTermination(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/d", "/c", "exit /b 1")
	err := cmd.Run()
	if err == nil {
		t.Fatal("fixture unexpectedly succeeded")
	}
	if grokProcessWasKilledByCarina(nil, err) {
		t.Fatal("natural exit status 1 was mistaken for Carina termination")
	}
}

func TestGrokWindowsProcessTreeHelper(t *testing.T) {
	if os.Getenv("GO_WANT_GROK_WINDOWS_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if mode == "child" {
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode != "parent" {
		os.Exit(2)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestGrokWindowsProcessTreeHelper$", "--", "child")
	child.Env = os.Environ()
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	pidFile := os.Getenv("CARINA_GROK_WINDOWS_CHILD_PID_FILE")
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		os.Exit(4)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForGrokWindowsChildPID(t *testing.T, path string) int {
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
	t.Fatal("Grok Build descendant pid was not published")
	return 0
}
