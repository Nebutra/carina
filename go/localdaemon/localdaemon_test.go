package localdaemon

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
)

func TestEnsureReachableAlreadyUp(t *testing.T) {
	origDial, origSpawn := Dial, Spawn
	t.Cleanup(func() { Dial, Spawn = origDial, origSpawn })

	spawns := 0
	Spawn = func(string) error { spawns++; return nil }
	Dial = func(string) (*rpc.Client, error) {
		// Non-nil without a real conn is enough for the success branch;
		// EnsureReachable returns it as-is. Use a zero value carefully —
		// callers would Close; we only check err/spawn count.
		return &rpc.Client{}, nil
	}

	c, err := EnsureReachable("/tmp/example.sock")
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
	if spawns != 0 {
		t.Fatalf("spawn calls = %d, want 0", spawns)
	}
}

func TestEnsureReachableSpawnsOnceThenSucceeds(t *testing.T) {
	origDial, origSpawn := Dial, Spawn
	t.Cleanup(func() { Dial, Spawn = origDial, origSpawn })

	spawns := 0
	dials := 0
	Spawn = func(string) error { spawns++; return nil }
	Dial = func(string) (*rpc.Client, error) {
		dials++
		if dials == 1 {
			return nil, rpc.ErrDaemonUnreachable
		}
		return &rpc.Client{}, nil
	}

	c, err := EnsureReachable("/tmp/example.sock")
	if err != nil {
		t.Fatal(err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
	if spawns != 1 {
		t.Fatalf("spawn calls = %d, want 1", spawns)
	}
}

func TestEnsureReachableNonUnreachableDoesNotSpawn(t *testing.T) {
	origDial, origSpawn := Dial, Spawn
	t.Cleanup(func() { Dial, Spawn = origDial, origSpawn })

	want := errors.New("permission denied")
	spawns := 0
	Spawn = func(string) error { spawns++; return nil }
	Dial = func(string) (*rpc.Client, error) { return nil, want }

	_, err := EnsureReachable("/tmp/example.sock")
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want wrap of %v", err, want)
	}
	if spawns != 0 {
		t.Fatalf("spawn calls = %d, want 0", spawns)
	}
}

func TestEnsureReachableSpawnFailure(t *testing.T) {
	origDial, origSpawn := Dial, Spawn
	t.Cleanup(func() { Dial, Spawn = origDial, origSpawn })

	spawnErr := errors.New("exec: not found")
	Spawn = func(string) error { return spawnErr }
	Dial = func(string) (*rpc.Client, error) { return nil, rpc.ErrDaemonUnreachable }

	_, err := EnsureReachable("/tmp/example.sock")
	if !errors.Is(err, spawnErr) {
		t.Fatalf("err = %v, want spawn error", err)
	}
}

func TestEnsureReachableDeadline(t *testing.T) {
	origDial, origSpawn, origDeadline := Dial, Spawn, ReachableDeadline
	t.Cleanup(func() {
		Dial, Spawn, ReachableDeadline = origDial, origSpawn, origDeadline
	})
	ReachableDeadline = 250 * time.Millisecond
	Spawn = func(string) error { return nil }
	Dial = func(string) (*rpc.Client, error) { return nil, rpc.ErrDaemonUnreachable }

	start := time.Now()
	_, err := EnsureReachable("/tmp/example.sock")
	if err == nil || !errors.Is(err, rpc.ErrDaemonUnreachable) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("deadline took too long: %v", elapsed)
	}
}

func runtimeSpecFixture(t *testing.T) localruntime.Spec {
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
	spec, err := localruntime.EnsureSpec(home, workspace, localruntime.SpecOptions{
		Mode:   localruntime.ModeWorkspace,
		Config: localruntime.ConfigIdentity{Fingerprint: "cfg1_test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func matchingDescription(spec localruntime.Spec) RuntimeDescription {
	return RuntimeDescription{
		Mode: string(spec.Mode), WorkspaceID: spec.Workspace.ID,
		WorkspaceRoot: spec.Workspace.CanonicalRoot, RuntimeID: spec.RuntimeID,
		Epoch: "runtime_process", ProcessEpoch: 1, PID: 4242,
		SocketPath: spec.Paths.SocketPath, StateDir: spec.Paths.StateDir,
		RuntimeDir: spec.Paths.RuntimeDir, ConfigFingerprint: spec.Config.Fingerprint,
		Lifecycle: localruntime.LifecycleRunning,
	}
}

func TestConnectOrStartConcurrentCallersSpawnOnce(t *testing.T) {
	spec := runtimeSpecFixture(t)
	origDial, origSpawn, origHandshake, origDeadline := Dial, SpawnRuntime, RuntimeHandshake, ReachableDeadline
	t.Cleanup(func() {
		Dial, SpawnRuntime, RuntimeHandshake, ReachableDeadline = origDial, origSpawn, origHandshake, origDeadline
	})
	ReachableDeadline = time.Second
	var running atomic.Bool
	var spawns atomic.Int32
	Dial = func(string) (*rpc.Client, error) {
		if !running.Load() {
			return nil, rpc.ErrDaemonUnreachable
		}
		return &rpc.Client{}, nil
	}
	SpawnRuntime = func(got localruntime.Spec) error {
		spawns.Add(1)
		if err := writeOwnershipRecord(got.Paths.OwnerPath, ownershipRecord{
			Owner: OwnershipMarker, PID: 4242, Socket: got.Paths.SocketPath,
			WorkspaceID: got.Workspace.ID, RuntimeID: got.RuntimeID,
		}); err != nil {
			return err
		}
		running.Store(true)
		return nil
	}
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return matchingDescription(spec), nil
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, _, err := ConnectOrStart(spec)
			if client == nil && err == nil {
				err = errors.New("nil client")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if spawns.Load() != 1 {
		t.Fatalf("spawn calls = %d, want 1", spawns.Load())
	}
}

func TestConnectOrStartReachableMismatchFailsClosed(t *testing.T) {
	spec := runtimeSpecFixture(t)
	origDial, origSpawn, origHandshake := Dial, SpawnRuntime, RuntimeHandshake
	t.Cleanup(func() { Dial, SpawnRuntime, RuntimeHandshake = origDial, origSpawn, origHandshake })
	Dial = func(string) (*rpc.Client, error) { return &rpc.Client{}, nil }
	spawns := 0
	SpawnRuntime = func(localruntime.Spec) error { spawns++; return nil }
	RuntimeHandshake = func(*rpc.Client, localruntime.Spec) (RuntimeDescription, error) {
		return RuntimeDescription{}, &rpc.Error{Code: rpc.CodeRuntimeIdentityMismatch, Message: "wrong runtime"}
	}
	if _, _, err := ConnectOrStart(spec); err == nil {
		t.Fatal("identity mismatch accepted")
	}
	if spawns != 0 {
		t.Fatalf("mismatched reachable endpoint triggered %d spawn(s)", spawns)
	}
}

func TestReleaseRuntimeOwnershipVerifiesCurrentProcess(t *testing.T) {
	spec := runtimeSpecFixture(t)
	record := ownershipRecord{
		Owner: OwnershipMarker, PID: 4242, Socket: spec.Paths.SocketPath,
		WorkspaceID: spec.Workspace.ID, RuntimeID: spec.RuntimeID,
		Epoch: "runtime_process", StartedAt: time.Now().UTC(),
	}
	if err := writeOwnershipRecord(spec.Paths.OwnerPath, record); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseRuntimeOwnership(spec, 4243); err == nil {
		t.Fatal("mismatched process removed runtime ownership")
	}
	if _, err := os.Stat(spec.Paths.OwnerPath); err != nil {
		t.Fatalf("mismatched release changed owner record: %v", err)
	}
	if err := ReleaseRuntimeOwnership(spec, 4242); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spec.Paths.OwnerPath); !os.IsNotExist(err) {
		t.Fatalf("owner record still exists: %v", err)
	}
}
