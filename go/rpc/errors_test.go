package rpc

import (
	"errors"
	"testing"
)

// TestDialWrapsErrDaemonUnreachable pins P1.5(b)'s typed-error requirement:
// Dial's returned error must wrap rpc.ErrDaemonUnreachable so callers can
// errors.Is() instead of string-matching the "(is the daemon running? ...)"
// suffix. Dialing a socket path that cannot possibly exist (a fresh temp
// dir with no listener) exercises the real net.DialTimeout failure path.
func TestDialWrapsErrDaemonUnreachable(t *testing.T) {
	dir := t.TempDir()
	_, err := Dial(dir + "/no-such-daemon.sock")
	if err == nil {
		t.Fatal("expected a dial error against a nonexistent socket")
	}
	if !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("Dial error does not wrap ErrDaemonUnreachable: %v", err)
	}
}
