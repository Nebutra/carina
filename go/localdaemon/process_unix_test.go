//go:build !windows

package localdaemon

import (
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
)

func TestConfigureDetachedProcessStartsNewSession(t *testing.T) {
	cmd := exec.Command("carina-daemon")
	configureDetachedProcess(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setsid {
		t.Fatal("detached daemon must start in a new Unix session")
	}
}

func TestStopRuntimeRefusesPIDReuseExecutableMismatch(t *testing.T) {
	spec, child, _ := stopRuntimeProcessFixture(t)
	description := matchingDescription(spec)
	description.PID = child.Process.Pid
	writeStopRuntimeOwner(t, spec, description, "/not/the/owned/executable")
	stubRuntimeEndpoint(t, description)

	if _, err := StopRuntime(spec, true); err == nil || !strings.Contains(err.Error(), "process executable mismatch") {
		t.Fatalf("StopRuntime error = %v", err)
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("unrelated process was signalled: %v", err)
	}
}

func TestStopRuntimeRequiresForceForActiveObligations(t *testing.T) {
	spec, child, executable := stopRuntimeProcessFixture(t)
	description := matchingDescription(spec)
	description.PID = child.Process.Pid
	description.Obligations = []string{"task:running"}
	writeStopRuntimeOwner(t, spec, description, executable)
	stubRuntimeEndpoint(t, description)

	if _, err := StopRuntime(spec, false); err == nil || !strings.Contains(err.Error(), "active obligations") {
		t.Fatalf("StopRuntime error = %v", err)
	}
	if err := child.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("process stopped without force: %v", err)
	}
	if _, err := StopRuntime(spec, true); err != nil {
		t.Fatalf("forced StopRuntime: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SIGTERM child exited without signal status")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forced StopRuntime did not signal process")
	}
}

func stopRuntimeProcessFixture(t *testing.T) (localruntime.Spec, *exec.Cmd, string) {
	t.Helper()
	executable, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep executable unavailable")
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(executable, "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState != nil {
			return
		}
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	})
	return runtimeSpecFixture(t), child, executable
}

func writeStopRuntimeOwner(t *testing.T, spec localruntime.Spec, description RuntimeDescription, executable string) {
	t.Helper()
	if err := writeOwnershipRecord(spec.Paths.OwnerPath, ownershipRecord{
		Owner: OwnershipMarker, PID: description.PID, Socket: spec.Paths.SocketPath,
		Executable: executable, WorkspaceID: spec.Workspace.ID, RuntimeID: spec.RuntimeID,
		Epoch: description.Epoch, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func stubRuntimeEndpoint(t *testing.T, description RuntimeDescription) {
	t.Helper()
	originalDial, originalHandshake := Dial, RuntimeHandshake
	t.Cleanup(func() { Dial, RuntimeHandshake = originalDial, originalHandshake })
	Dial = func(string) (*rpc.Client, error) { return &rpc.Client{}, nil }
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return description, nil
	}
}
