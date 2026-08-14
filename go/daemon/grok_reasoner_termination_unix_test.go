//go:build unix

package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestGrokProcessWasKilledByCarinaOnUnix(t *testing.T) {
	cmd := exec.CommandContext(context.Background(), "sh", "-c", "exec sleep 30")
	configureCLIReasonerCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	killErr := killCLIReasonerCommand(cmd)
	waitErr := cmd.Wait()
	if !grokProcessWasKilledByCarina(killErr, waitErr) {
		t.Fatalf("killErr=%v waitErr=%v, want Carina kill", killErr, waitErr)
	}
	if grokProcessWasKilledByCarina(os.ErrProcessDone, waitErr) {
		t.Fatal("failed kill must not authorize ignoring the wait error")
	}
}

func TestGrokTerminationSweepsProcessGroupBeforeWait(t *testing.T) {
	cmd := &exec.Cmd{}
	calls := 0
	err := terminateGrokCLIReasonerCommandWith(cmd, func(got *exec.Cmd) error {
		if got != cmd {
			t.Fatal("termination changed command identity")
		}
		calls++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != grokProcessGroupKillAttempts {
		t.Fatalf("kill calls=%d, want %d bounded sweeps", calls, grokProcessGroupKillAttempts)
	}

	calls = 0
	err = terminateGrokCLIReasonerCommandWith(cmd, func(*exec.Cmd) error {
		calls++
		return os.ErrProcessDone
	})
	if !errors.Is(err, os.ErrProcessDone) || calls != 1 {
		t.Fatalf("initial kill failure err=%v calls=%d", err, calls)
	}
}

func TestGrokProcessDoesNotMistakeNaturalUnixExitForKill(t *testing.T) {
	waitErr := exec.Command("sh", "-c", "exit 1").Run()
	if waitErr == nil {
		t.Fatal("fixture exited successfully")
	}
	if grokProcessWasKilledByCarina(nil, waitErr) {
		t.Fatal("natural exit was mistaken for Carina process termination")
	}
}

func TestGrokProcessDoesNotMistakeAnotherUnixSignalForCarinaKill(t *testing.T) {
	cmd := exec.Command("sh", "-c", "exec sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatal(err)
	}
	waitErr := cmd.Wait()
	if grokProcessWasKilledByCarina(nil, waitErr) {
		t.Fatal("external signal was mistaken for Carina SIGKILL termination")
	}
}
