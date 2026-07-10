//go:build !darwin && !linux

package main

import (
	"os"
	"os/exec"
)

func configureExecutorCommand(_ *exec.Cmd) {}

// Platforms without Unix process groups fall back to terminating the direct
// executor process. Operators on those platforms must ensure their executor
// does not leave detached descendants.
func killExecutorProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
