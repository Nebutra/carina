//go:build windows

package localdaemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func configureDetachedProcess(_ *exec.Cmd) {}

func acquireRuntimeStartLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}

func releaseRuntimeStartLock(file *os.File) {
	if file != nil {
		_ = file.Close()
	}
}

func signalRuntimeProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func runtimeProcessExecutable(pid int) (string, error) {
	return "", fmt.Errorf("process executable verification is unsupported on Windows for pid %d", pid)
}
