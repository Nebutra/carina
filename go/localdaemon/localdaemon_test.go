package localdaemon

import (
	"errors"
	"testing"
	"time"

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
