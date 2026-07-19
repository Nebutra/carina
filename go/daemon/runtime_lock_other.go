//go:build !darwin && !linux

package daemon

import (
	"errors"
	"os"
)

func acquireStateLock(_ *os.File) error {
	return errors.New("state-directory locking is unsupported on this platform")
}

func releaseStateLock(_ *os.File) error { return nil }
