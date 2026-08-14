package daemon

import (
	"errors"
	"os/exec"
)

func grokProcessWasKilledByCarina(killErr, waitErr error) bool {
	if killErr != nil {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) || exitErr.ProcessState == nil {
		return false
	}
	return grokProcessStateMatchesCarinaKill(exitErr.ProcessState)
}
