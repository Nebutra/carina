//go:build !unix

package toolchain

import "os/exec"

func configureCommandProcess(cmd *exec.Cmd) {}

func killCommandProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
