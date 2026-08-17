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

type errNotFound struct{}

func (errNotFound) Error() string { return "not found" }
