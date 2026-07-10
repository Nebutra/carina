package rpc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortSockDir returns a short-path temp dir for a unix socket test, using
// os.MkdirTemp with a terse prefix rather than t.TempDir() (which nests
// under a directory named after the full test function name and can push a
// socket path past the ~104-byte unix socket path limit on macOS/BSD).
// Cleaned up via t.Cleanup like t.TempDir().
func shortSockDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestListenUnixLockBlocksSecondInstance proves the P1.8 cross-process
// mutual-exclusion fix: when a second Server.ListenUnix call targets the
// same socket path while a first instance is still listening, the second
// call must fail with ErrSocketInUse rather than silently os.Remove-ing the
// path the first instance is bound to and rebinding over it. Before this
// fix, ListenUnix unconditionally called os.Remove(path) with no
// cross-process lock, so two `carina` invocations racing to auto-start
// carina-daemon on a fresh machine could each spawn a daemon, and the
// second one would orphan the first's live listener with no error surfaced
// to either process.
func TestListenUnixLockBlocksSecondInstance(t *testing.T) {
	sock := filepath.Join(shortSockDir(t), "d.sock")

	s1 := NewServer()
	errCh := make(chan error, 1)
	go func() { errCh <- s1.ListenUnix(sock) }()
	defer s1.Close()
	waitSock(t, sock)

	s2 := NewServer()
	err := s2.ListenUnix(sock)
	if err == nil {
		s2.Close()
		t.Fatal("second ListenUnix on the same socket path must fail, got nil error")
	}
	if !errors.Is(err, ErrSocketInUse) {
		t.Fatalf("second ListenUnix error = %v, want it to wrap ErrSocketInUse", err)
	}

	// The first instance must still be alive and serving — the second
	// instance's failed attempt must not have unlinked/damaged its socket.
	select {
	case err := <-errCh:
		t.Fatalf("first instance's ListenUnix returned unexpectedly: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := Dial(sock); err != nil {
		t.Fatalf("first instance's socket must still accept connections after a second instance's failed bind attempt: %v", err)
	}
}

// TestListenUnixRebindsStaleSocket proves the fix does not regress the
// legitimate case: a socket path left over from a daemon that exited
// without cleaning up (process killed, crash) has no live lock holder, so a
// fresh ListenUnix must still succeed by reclaiming the stale path — only a
// socket with a live lock holder blocks a second bind.
func TestListenUnixRebindsStaleSocket(t *testing.T) {
	sock := filepath.Join(shortSockDir(t), "d.sock")

	s1 := NewServer()
	if err := func() error {
		errCh := make(chan error, 1)
		go func() { errCh <- s1.ListenUnix(sock) }()
		waitSock(t, sock)
		return s1.Close()
	}(); err != nil {
		t.Fatalf("first instance setup: %v", err)
	}

	s2 := NewServer()
	go func() { _ = s2.ListenUnix(sock) }()
	defer s2.Close()
	waitSock(t, sock)

	if _, err := Dial(sock); err != nil {
		t.Fatalf("second instance must be able to reclaim a stale socket after the first cleanly closed: %v", err)
	}
}
