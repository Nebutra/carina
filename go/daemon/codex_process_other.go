//go:build !unix

package daemon

import (
	"os"
	"os/exec"
)

func configureCLIReasonerCommand(cmd *exec.Cmd) {}

func killCLIReasonerCommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
