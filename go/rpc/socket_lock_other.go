//go:build !darwin && !linux

package rpc

import (
	"fmt"
	"os"
)

func acquireSocketLock(*os.File) error {
	return fmt.Errorf("unix socket locking is unsupported on this platform")
}

func releaseSocketLock(*os.File) error { return nil }
