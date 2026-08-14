//go:build unix

package provider

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	grokBuildProbeKillAttempts = 4
	grokBuildProbeKillInterval = 5 * time.Millisecond
)

func startGrokBuildProbeCommand(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("nil Grok Build probe command")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return terminateGrokBuildProbeCommand(cmd) }
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd.Start()
}

func terminateGrokBuildProbeCommand(cmd *exec.Cmd) error {
	firstErr := killGrokBuildProbeProcessGroup(cmd)
	if firstErr != nil {
		return firstErr
	}
	for attempt := 1; attempt < grokBuildProbeKillAttempts; attempt++ {
		time.Sleep(grokBuildProbeKillInterval)
		_ = killGrokBuildProbeProcessGroup(cmd)
	}
	return nil
}

func killGrokBuildProbeProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func releaseGrokBuildProbeCommand(*exec.Cmd) {}
