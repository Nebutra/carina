//go:build darwin || linux

package browser

import "syscall"

func execChromeProcess(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env)
}
