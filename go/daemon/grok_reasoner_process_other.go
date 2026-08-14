//go:build !unix && !windows

package daemon

import (
	"os"
	"os/exec"
)

func startGrokCLIReasonerCommand(cmd *exec.Cmd) error {
	return cmd.Start()
}

func releaseGrokCLIReasonerCommand(*exec.Cmd) {}

func terminateGrokCLIReasonerCommand(cmd *exec.Cmd) error {
	return killCLIReasonerCommand(cmd)
}

func grokProcessStateMatchesCarinaKill(_ *os.ProcessState) bool {
	return false
}
