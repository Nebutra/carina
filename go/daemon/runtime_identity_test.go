package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func runtimeIdentityFixture(t *testing.T) (localruntime.Spec, *Daemon) {
	t.Helper()
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	workspace, err := localruntime.ResolveWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := localruntime.EnsureSpec(home, workspace, localruntime.SpecOptions{Mode: localruntime.ModeWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		runtimeSpec:      &spec,
		runtimeLease:     &runtimeLease{state: runtimeState{InstanceID: "runtime_process", Epoch: 7}},
		stateDir:         spec.Paths.StateDir,
		socketPath:       spec.Paths.SocketPath,
		started:          time.Now().UTC(),
		runtimeLifecycle: localruntime.LifecycleRunning,
	}
	return spec, d
}

func TestRuntimeIdentityMismatchIsTyped(t *testing.T) {
	spec, d := runtimeIdentityFixture(t)
	if err := d.validateExpectedRuntimeIdentity(spec.Workspace.ID, spec.RuntimeID, "runtime_process"); err != nil {
		t.Fatal(err)
	}
	err := d.validateExpectedRuntimeIdentity("wrong", spec.RuntimeID, "runtime_process")
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeRuntimeIdentityMismatch {
		t.Fatalf("error = %#v", err)
	}
}

func TestRuntimeDescriptorLifecyclePersistsStoppedEntry(t *testing.T) {
	spec, d := runtimeIdentityFixture(t)
	if err := d.publishRuntimeDescriptor(localruntime.LifecycleRunning, spec.Paths.SocketPath); err != nil {
		t.Fatal(err)
	}
	running, err := localruntime.LoadDescriptor(spec.Paths.DescriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	if running.Lifecycle != localruntime.LifecycleRunning || running.Epoch != "runtime_process" || running.PID != os.Getpid() {
		t.Fatalf("running descriptor = %+v", running)
	}
	if err := d.publishRuntimeDescriptor(localruntime.LifecycleStopped, spec.Paths.SocketPath); err != nil {
		t.Fatal(err)
	}
	stopped, err := localruntime.LoadDescriptor(spec.Paths.DescriptorPath)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Lifecycle != localruntime.LifecycleStopped || stopped.StoppedAt == nil {
		t.Fatalf("stopped descriptor = %+v", stopped)
	}
}

func TestWorkspaceRuntimeRejectsMismatchedPersistedSession(t *testing.T) {
	spec, _ := runtimeIdentityFixture(t)
	store, err := sessionstore.Open(spec.Paths.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	otherRoot := t.TempDir()
	sess, err := store.CreateSessionModeForWorkspace(spec.Workspace.ID, otherRoot, "safe-edit", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	err = validateRuntimeSessions(&spec, store.List())
	if err == nil || !strings.Contains(err.Error(), sess.SessionID) || !strings.Contains(err.Error(), "workspace mismatch") {
		t.Fatalf("error = %v", err)
	}
}
