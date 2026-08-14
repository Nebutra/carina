//go:build unix

package daemon

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	grokProcessGroupKillAttempts = 4
	grokProcessGroupKillInterval = 5 * time.Millisecond
)

func startGrokCLIReasonerCommand(cmd *exec.Cmd) error {
	cmd.Cancel = func() error { return terminateGrokCLIReasonerCommand(cmd) }
	return cmd.Start()
}

func releaseGrokCLIReasonerCommand(*exec.Cmd) {}

func terminateGrokCLIReasonerCommand(cmd *exec.Cmd) error {
	return terminateGrokCLIReasonerCommandWith(cmd, killCLIReasonerCommand)
}

func terminateGrokCLIReasonerCommandWith(cmd *exec.Cmd, kill func(*exec.Cmd) error) error {
	firstErr := kill(cmd)
	if firstErr != nil {
		return firstErr
	}
	for attempt := 1; attempt < grokProcessGroupKillAttempts; attempt++ {
		time.Sleep(grokProcessGroupKillInterval)
		_ = kill(cmd)
	}
	return nil
}

func grokProcessStateMatchesCarinaKill(state *os.ProcessState) bool {
	status, ok := state.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == syscall.SIGKILL
}
