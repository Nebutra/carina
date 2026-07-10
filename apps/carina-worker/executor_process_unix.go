//go:build darwin || linux

package main

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func configureExecutorCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killExecutorProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
