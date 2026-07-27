//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

func replaceWithUIProcess(binary string, args []string) (int, error) {
	argv := append([]string{binary}, args...)
	return 0, syscall.Exec(binary, argv, os.Environ())
}
