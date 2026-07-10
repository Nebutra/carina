package daemon

import (
	"errors"
	"strings"
)

const workerAuthenticationError = "worker authentication failed"

func (d *Daemon) authenticateWorker(workerID, credential string) error {
	if d.pool == nil || !d.pool.Authenticate(strings.TrimSpace(workerID), strings.TrimSpace(credential)) {
		return errors.New(workerAuthenticationError)
	}
	return nil
}
