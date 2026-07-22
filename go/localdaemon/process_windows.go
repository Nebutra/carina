//go:build windows

package localdaemon

import "os/exec"

func configureDetachedProcess(_ *exec.Cmd) {}
