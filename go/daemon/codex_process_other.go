//go:build !unix

package daemon

import (
	"os"
	"os/exec"
)

func configureCodexCLICommand(cmd *exec.Cmd) {}

func killCodexCLICommand(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
