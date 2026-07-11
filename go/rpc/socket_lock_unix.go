//go:build darwin || linux

package rpc

import (
	"os"

	"golang.org/x/sys/unix"
)

func acquireSocketLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func releaseSocketLock(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
