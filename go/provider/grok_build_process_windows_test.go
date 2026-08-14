//go:build windows

package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestGrokBuildProbeWindowsJobTerminationKillsDescendants(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=^TestGrokBuildProbeWindowsProcessTreeHelper$", "--", "parent")
	cmd.Env = append(os.Environ(),
		"GO_WANT_GROK_BUILD_PROBE_WINDOWS_HELPER=1",
		"CARINA_GROK_BUILD_PROBE_CHILD_PID_FILE="+pidPath,
	)
	if err := startGrokBuildProbeCommand(cmd); err != nil {
		t.Fatal(err)
	}
	defer releaseGrokBuildProbeCommand(cmd)

	childPID := waitForGrokBuildProbeWindowsChildPID(t, pidPath)
	if err := terminateGrokBuildProbeCommand(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("terminated Grok Build probe fixture exited successfully")
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
		t.Fatalf("Grok Build probe descendant pid %d survived Job termination", childPID)
	}
}

func TestGrokBuildProbeWindowsProcessTreeHelper(t *testing.T) {
	if os.Getenv("GO_WANT_GROK_BUILD_PROBE_WINDOWS_HELPER") != "1" {
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
	child := exec.Command(os.Args[0], "-test.run=^TestGrokBuildProbeWindowsProcessTreeHelper$", "--", "child")
	child.Env = os.Environ()
	if err := child.Start(); err != nil {
		os.Exit(3)
	}
	pidPath := os.Getenv("CARINA_GROK_BUILD_PROBE_CHILD_PID_FILE")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(child.Process.Pid)), 0o600); err != nil {
		os.Exit(4)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func waitForGrokBuildProbeWindowsChildPID(t *testing.T, path string) int {
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
	t.Fatal("Grok Build probe descendant pid was not published")
	return 0
}
