package toolchain

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// lookPath is exec.LookPath, overridable in tests.
var lookPath = exec.LookPath

// SandboxStatus is the honest OS-sandbox report: requested is a policy bit;
// applied is only true when this host can wrap the child.
type SandboxStatus struct {
	Requested bool   `json:"requested"`
	Available bool   `json:"available"`
	Applied   bool   `json:"applied"`
	Platform  string `json:"platform"`
	Helper    string `json:"helper,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// InspectSandbox reports whether an OS syscall sandbox can actually wrap
// commands on this host. It does not enable Linux landlock or Windows
// desktop confinement.
func InspectSandbox(requested bool) SandboxStatus {
	st := SandboxStatus{Requested: requested, Platform: runtime.GOOS}
	switch runtime.GOOS {
	case "darwin":
		st.Helper = "sandbox-exec"
		if _, err := lookPath("sandbox-exec"); err == nil {
			st.Available = true
		} else {
			st.Reason = "sandbox-exec is not on PATH"
		}
	case "linux":
		st.Helper = "bwrap"
		if _, err := lookPath("bwrap"); err == nil {
			st.Available = true
		} else {
			st.Reason = "bubblewrap (bwrap) is not on PATH"
		}
	default:
		st.Reason = "OS syscall sandbox is not implemented on " + runtime.GOOS
	}
	st.Applied = requested && st.Available
	if requested && !st.Available && st.Reason == "" {
		st.Reason = "OS sandbox was requested but cannot be applied"
	}
	return st
}

// sandboxProcessEnv is the carina-run process environment when --sandbox is
// on: host secrets stay out, HOME is the workspace, proxy extras still pass.
func sandboxProcessEnv(cwd string, extra []string) []string {
	path := os.Getenv("PATH")
	tmp := os.Getenv("TMPDIR")
	if tmp == "" {
		tmp = "/tmp"
	}
	env := []string{
		"PATH=" + path,
		"HOME=" + cwd,
		"TMPDIR=" + tmp,
	}
	if lang := os.Getenv("LANG"); lang != "" {
		env = append(env, "LANG="+lang)
	}
	for _, kv := range extra {
		if strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return env
}

func sandboxUnavailableError(st SandboxStatus) error {
	if st.Reason != "" {
		return fmt.Errorf("toolchain: OS sandbox requested but unavailable: %s", st.Reason)
	}
	return fmt.Errorf("toolchain: OS sandbox requested but unavailable on %s", st.Platform)
}
