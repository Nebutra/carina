package toolchain

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestInspectSandboxDoesNotClaimUnavailableHelper(t *testing.T) {
	prev := lookPath
	t.Cleanup(func() { lookPath = prev })
	lookPath = func(string) (string, error) { return "", errNotFound{} }

	off := InspectSandbox(false)
	if off.Requested || off.Applied || !off.Available && off.Requested {
		t.Fatalf("unrequested sandbox = %+v", off)
	}
	if off.Applied {
		t.Fatal("unrequested sandbox must not report applied")
	}

	on := InspectSandbox(true)
	if !on.Requested || on.Applied || on.Available {
		t.Fatalf("requested without helper must not apply: %+v", on)
	}
	if on.Reason == "" {
		t.Fatal("missing helper must explain why")
	}
}

func TestInspectSandboxReportsPlatformHelper(t *testing.T) {
	st := InspectSandbox(false)
	switch runtime.GOOS {
	case "darwin":
		if st.Helper != "sandbox-exec" {
			t.Fatalf("darwin helper = %q", st.Helper)
		}
	case "linux":
		if st.Helper != "bwrap" {
			t.Fatalf("linux helper = %q", st.Helper)
		}
	default:
		if st.Available {
			t.Fatalf("unsupported OS must not claim available: %+v", st)
		}
	}
}

func TestRunContextFailsClosedWhenSandboxRequestedButUnavailable(t *testing.T) {
	prev := lookPath
	t.Cleanup(func() { lookPath = prev })
	lookPath = func(string) (string, error) { return "", errNotFound{} }

	_, err := New("").RunContext(context.Background(), []string{"echo", "ok"}, t.TempDir(), 0, nil, true)
	if err == nil || !strings.Contains(err.Error(), "OS sandbox requested but unavailable") {
		t.Fatalf("want fail-closed sandbox error, got %v", err)
	}
}

func TestSandboxProcessEnvSetsHomeToWorkspace(t *testing.T) {
	t.Setenv("AWS_SECRET_ACCESS_KEY", "leak")
	cwd := t.TempDir()
	env := sandboxProcessEnv(cwd, []string{"HTTPS_PROXY=http://127.0.0.1:9"})
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	if got["HOME"] != cwd {
		t.Fatalf("HOME = %q, want workspace", got["HOME"])
	}
	if got["HTTPS_PROXY"] != "http://127.0.0.1:9" {
		t.Fatalf("proxy extra dropped: %#v", got)
	}
	if _, ok := got["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Fatal("host secrets must not be copied into the sandbox process env")
	}
}

type errNotFound struct{}

func (errNotFound) Error() string { return "not found" }
