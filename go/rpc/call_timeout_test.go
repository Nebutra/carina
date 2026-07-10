package rpc

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCallTimesOutWhenServerAcceptsButNeverResponds proves the fix for
// go/rpc.Client.Call hanging indefinitely: a connection that accepts the
// request but never writes a response (the daemon's handler wedged/
// deadlocked, or the single-writer drain loop itself stalled) must cause
// Call to return a distinct, non-nil error within a bounded time — never
// block forever. This is exactly `carina doctor`'s failure mode: dial
// succeeds, but the daemon-side handler never answers.
func TestCallTimesOutWhenServerAcceptsButNeverResponds(t *testing.T) {
	dir, err := os.MkdirTemp("", "ct")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "d.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		close(accepted)
		// Accept the connection and read the request, but never write a
		// response — simulates a wedged daemon-side handler.
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		<-time.After(5 * time.Second) // outlive the test
		conn.Close()
	}()

	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetCallTimeout(200 * time.Millisecond)

	<-accepted

	start := time.Now()
	err = c.Call("doctor", map[string]any{}, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Call must return an error when the server never responds, got nil")
	}
	if !errors.Is(err, ErrCallTimeout) {
		t.Fatalf("Call error = %v, want it to wrap ErrCallTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Call took %v to time out, want well under its ~200ms deadline (bounded, not indefinite)", elapsed)
	}
}
