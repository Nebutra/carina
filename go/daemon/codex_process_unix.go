//go:build unix

package daemon

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureCLIReasonerCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killCLIReasonerCommand(cmd) }
	cmd.WaitDelay = 100 * time.Millisecond
}

func killCLIReasonerCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
