//go:build !unix && !windows

package provider

import (
	"errors"
	"os"
	"os/exec"
	"time"
)

func startGrokBuildProbeCommand(cmd *exec.Cmd) error {
	if cmd == nil {
		return errors.New("nil Grok Build probe command")
	}
	cmd.Cancel = func() error { return terminateGrokBuildProbeCommand(cmd) }
	cmd.WaitDelay = 100 * time.Millisecond
	return cmd.Start()
}

func terminateGrokBuildProbeCommand(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}

func releaseGrokBuildProbeCommand(*exec.Cmd) {}
