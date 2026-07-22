//go:build !windows

package localdaemon

import (
	"os/exec"
	"testing"
)

func TestConfigureDetachedProcessStartsNewSession(t *testing.T) {
	cmd := exec.Command("carina-daemon")
	configureDetachedProcess(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("detached daemon must start in a new Unix session")
	}
}
