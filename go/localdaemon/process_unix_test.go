//go:build !windows

package localdaemon

import (
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/product"
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

func TestConnectOrStartReplacesOwnedIdleIncompatibleRuntime(t *testing.T) {
	spec, child, executable := stopRuntimeProcessFixture(t)
	oldDescription := matchingDescription(spec)
	oldDescription.PID = child.Process.Pid
	writeStopRuntimeOwner(t, spec, oldDescription, executable)

	var phase atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- child.Wait()
		phase.Store(1)
	}()
	origDial, origSpawn, origHandshake, origDescribe, origDeadline := Dial, SpawnRuntime, RuntimeHandshake, RuntimeDescribe, ReachableDeadline
	t.Cleanup(func() {
		Dial, SpawnRuntime, RuntimeHandshake, RuntimeDescribe, ReachableDeadline = origDial, origSpawn, origHandshake, origDescribe, origDeadline
	})
	ReachableDeadline = 2 * time.Second
	Dial = func(string) (*rpc.Client, error) {
		if phase.Load() == 1 {
			return nil, rpc.ErrDaemonUnreachable
		}
		return &rpc.Client{}, nil
	}
	RuntimeDescribe = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return oldDescription, nil
	}
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		if phase.Load() < 2 {
			return RuntimeDescription{}, &RuntimeCompatibilityError{
				Description: oldDescription, MissingMethods: []string{"execution.start"},
			}
		}
		current := oldDescription
		current.PID = 5252
		current.Epoch = "runtime_current"
		return current, nil
	}
	spawns := 0
	SpawnRuntime = func(got localruntime.Spec) error {
		spawns++
		current := oldDescription
		current.PID = 5252
		current.Epoch = "runtime_current"
		writeStopRuntimeOwner(t, got, current, executable)
		phase.Store(2)
		return nil
	}

	client, description, err := ConnectOrStart(spec)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if spawns != 1 || description.Epoch != "runtime_current" {
		t.Fatalf("replacement spawns=%d description=%+v", spawns, description)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "signal") {
			t.Fatalf("old runtime exit = %v, want SIGTERM", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old incompatible runtime was not stopped")
	}
}

func TestConnectOrStartReplacesOwnedIdleVersionMismatch(t *testing.T) {
	spec, child, executable := stopRuntimeProcessFixture(t)
	oldDescription := matchingDescription(spec)
	oldDescription.PID = child.Process.Pid
	oldDescription.BinaryVersion = "0.0.1"
	writeStopRuntimeOwner(t, spec, oldDescription, executable)

	var phase atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- child.Wait()
		phase.Store(1)
	}()
	origDial, origSpawn, origHandshake, origDescribe, origDeadline := Dial, SpawnRuntime, RuntimeHandshake, RuntimeDescribe, ReachableDeadline
	t.Cleanup(func() {
		Dial, SpawnRuntime, RuntimeHandshake, RuntimeDescribe, ReachableDeadline = origDial, origSpawn, origHandshake, origDescribe, origDeadline
	})
	ReachableDeadline = 2 * time.Second
	Dial = func(string) (*rpc.Client, error) {
		if phase.Load() == 1 {
			return nil, rpc.ErrDaemonUnreachable
		}
		return &rpc.Client{}, nil
	}
	RuntimeDescribe = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return oldDescription, nil
	}
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		if phase.Load() < 2 {
			return RuntimeDescription{}, &RuntimeVersionMismatchError{
				Description: oldDescription, Observed: "0.0.1", Expected: product.Version,
			}
		}
		current := oldDescription
		current.PID = 5252
		current.Epoch = "runtime_current"
		current.BinaryVersion = product.Version
		return current, nil
	}
	spawns := 0
	SpawnRuntime = func(got localruntime.Spec) error {
		spawns++
		current := oldDescription
		current.PID = 5252
		current.Epoch = "runtime_current"
		current.BinaryVersion = product.Version
		writeStopRuntimeOwner(t, got, current, executable)
		phase.Store(2)
		return nil
	}

	client, description, err := ConnectOrStart(spec)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if spawns != 1 || description.Epoch != "runtime_current" || description.BinaryVersion != product.Version {
		t.Fatalf("replacement spawns=%d description=%+v", spawns, description)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "signal") {
			t.Fatalf("old runtime exit = %v, want SIGTERM", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old version-stale runtime was not stopped")
	}
}

func TestConnectOrStartReplacesOwnedIdleStaleConfiguration(t *testing.T) {
	spec, child, executable := stopRuntimeProcessFixture(t)
	oldDescription := matchingDescription(spec)
	oldDescription.ConfigFingerprint = "cfg1_stale"
	oldDescription.PID = child.Process.Pid
	writeStopRuntimeOwner(t, spec, oldDescription, executable)

	var phase atomic.Int32
	done := make(chan error, 1)
	go func() {
		done <- child.Wait()
		phase.Store(1)
	}()
	origDial, origSpawn, origHandshake, origDescribe, origDeadline := Dial, SpawnRuntime, RuntimeHandshake, RuntimeDescribe, ReachableDeadline
	t.Cleanup(func() {
		Dial, SpawnRuntime, RuntimeHandshake, RuntimeDescribe, ReachableDeadline = origDial, origSpawn, origHandshake, origDescribe, origDeadline
	})
	ReachableDeadline = 2 * time.Second
	Dial = func(string) (*rpc.Client, error) {
		if phase.Load() == 1 {
			return nil, rpc.ErrDaemonUnreachable
		}
		return &rpc.Client{}, nil
	}
	RuntimeDescribe = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return oldDescription, nil
	}
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		if phase.Load() < 2 {
			return RuntimeDescription{}, &RuntimeConfigurationMismatchError{
				Description:         oldDescription,
				ExpectedFingerprint: spec.Config.Fingerprint,
				ObservedFingerprint: oldDescription.ConfigFingerprint,
			}
		}
		current := matchingDescription(spec)
		current.PID = 5252
		current.Epoch = "runtime_current"
		return current, nil
	}
	spawns := 0
	SpawnRuntime = func(got localruntime.Spec) error {
		spawns++
		current := matchingDescription(got)
		current.PID = 5252
		current.Epoch = "runtime_current"
		writeStopRuntimeOwner(t, got, current, executable)
		phase.Store(2)
		return nil
	}

	client, description, err := ConnectOrStart(spec)
	if err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	if spawns != 1 || description.Epoch != "runtime_current" || description.ConfigFingerprint != spec.Config.Fingerprint {
		t.Fatalf("replacement spawns=%d description=%+v", spawns, description)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "signal") {
			t.Fatalf("old runtime exit = %v, want SIGTERM", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("old stale runtime was not stopped")
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
	originalDial, originalDescribe := Dial, RuntimeDescribe
	t.Cleanup(func() { Dial, RuntimeDescribe = originalDial, originalDescribe })
	Dial = func(string) (*rpc.Client, error) { return &rpc.Client{}, nil }
	RuntimeDescribe = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return description, nil
	}
}
