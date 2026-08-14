//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const grokNaturalExitHelperEnv = "CARINA_TEST_GROK_NATURAL_EXIT"

func TestGrokProcessWasKilledByCarinaOnWindows(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >NUL")
	if err := startGrokCLIReasonerCommand(cmd); err != nil {
		t.Fatal(err)
	}
	defer releaseGrokCLIReasonerCommand(cmd)
	killErr := terminateGrokCLIReasonerCommand(cmd)
	waitErr := cmd.Wait()
	if !grokProcessWasKilledByCarina(killErr, waitErr) {
		t.Fatalf("killErr=%v waitErr=%v, want Carina kill", killErr, waitErr)
	}
	if exitErr, ok := waitErr.(*exec.ExitError); !ok || uint32(exitErr.ExitCode()) != grokWindowsCarinaExitCode {
		t.Fatalf("waitErr=%v, want Carina Job termination code", waitErr)
	}
}

func TestGrokProcessDoesNotMistakeNaturalWindowsExitOneForKill(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "exiting")
	cmd := exec.Command(os.Args[0], "-test.run=^TestGrokNaturalExitHelper$")
	cmd.Env = append(os.Environ(), grokNaturalExitHelperEnv+"="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatal("natural-exit helper did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The marker is written immediately before os.Exit. Give Windows time to
	// publish the natural exit before attempting the same kill used in production.
	time.Sleep(50 * time.Millisecond)
	killErr := killCLIReasonerCommand(cmd)
	waitErr := cmd.Wait()
	if killErr == nil {
		t.Fatal("fixture was killed before its natural exit")
	}
	if exitErr, ok := waitErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("waitErr=%v, want natural exit code 1", waitErr)
	}
	if grokProcessWasKilledByCarina(killErr, waitErr) {
		t.Fatal("natural Windows exit 1 was mistaken for Carina process termination")
	}
}

func TestGrokNaturalExitHelper(t *testing.T) {
	marker := os.Getenv(grokNaturalExitHelperEnv)
	if marker == "" {
		return
	}
	if err := os.WriteFile(marker, []byte("ready"), 0o600); err != nil {
		os.Exit(2)
	}
	os.Exit(1)
}
