package rpc

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestServeWaitsForDrainBeforeClosingConn is the enforcement mechanism for
// the shutdown-window variant of the single-writer-drain problem
// (docs/plans/agent-cli-productization.md P1.8): a frame that was already
// accepted onto connWriter's queue by enqueue() — e.g. a Notify from an
// approval-resolution event, published concurrently with the client
// disconnecting — must actually reach the wire before serveWithScopes tears
// down the net.Conn, not be silently discarded by a conn.Close() racing
// drain()'s in-flight enc.Encode.
//
// enqueue() only promises the frame was accepted onto the channel, not that
// it reached the wire (see connWriter's doc comment). serveWithScopes's
// defers close(done) then conn.Close() without ever reading from
// w.stopped, so nothing guaranteed drain() finished flushing an
// already-queued frame before the connection was closed out from under it.
//
// This uses a real unix socket (not net.Pipe) and a half-close
// (CloseWrite) on the client side to model a realistic disconnect: the
// client stops sending (driving serveWithScopes's scanner to EOF, exactly
// like a real client hanging up) while its read side stays open and still
// able to receive whatever the server sends next — so the test can
// distinguish "the frame was never delivered because the peer was
// genuinely, unrecoverably gone" (not fixable, not this bug) from "the
// frame was never delivered because our own conn.Close() raced drain()'s
// in-flight Encode" (this bug: entirely self-inflicted, since the peer was
// still willing and able to receive it).
func TestServeWaitsForDrainBeforeClosingConn(t *testing.T) {
	dir, err := os.MkdirTemp("", "drain-shutdown")
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

	s := NewServer()
	var subMu sync.Mutex
	var activeSub *Subscription
	subReady := make(chan struct{})
	s.RegisterStream("watch", func(_ json.RawMessage, sub *Subscription) error {
		subMu.Lock()
		activeSub = sub
		subMu.Unlock()
		close(subReady)
		return nil
	})

	var gated *gatedConn
	accepted := make(chan struct{})
	serveDone := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		// gateAt=2: the subscribe ack is Write #1 (issued synchronously
		// inside serveWithScopes before the client ever disconnects), so
		// the Notify below is Write #2 — the first write this test gates.
		gated = newGatedConn(conn, 2)
		close(accepted)
		s.serve(gated, OriginLocal)
		close(serveDone)
	}()

	clientConn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}

	writeLine := func(v any) {
		b, _ := json.Marshal(v)
		b = append(b, '\n')
		_, _ = clientConn.Write(b)
	}
	reader := bufio.NewReader(clientConn)

	writeLine(Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "watch"})
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("read subscribe ack: %v", err)
	}

	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("connection never accepted")
	}
	select {
	case <-subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription never registered")
	}

	subMu.Lock()
	sub := activeSub
	subMu.Unlock()
	if sub == nil {
		t.Fatal("subscription not active")
	}

	notifyErrCh := make(chan error, 1)
	go func() {
		notifyErrCh <- sub.Notify("event", map[string]any{"kind": "approval_resolved"})
	}()

	// Wait until the Notify's Write call has actually been entered and is
	// gated (proving it was enqueued and is in progress, not merely
	// scheduled).
	select {
	case <-gated.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify's Write was never entered — gating did not engage")
	}

	// While the Notify write is held open, the client half-closes its
	// write side — this drives serveWithScopes's read loop to EOF (scanner
	// sees the peer stopped sending) and into its teardown defers
	// (close(done) then conn.Close() on the SAME server-side conn object
	// gatedConn wraps) concurrently with the still in-flight,
	// already-enqueued Notify write. The client's read side stays open, so
	// it can still receive the Notify frame if serveWithScopes does not
	// prematurely close the connection out from under drain().
	if err := clientConn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite: %v", err)
	}

	// Give serveWithScopes a bounded window to actually reach and run its
	// teardown defers — including conn.Close() — while the Notify write
	// remains gated. If serveWithScopes does not wait for drain() to
	// finish before closing the conn, conn.Close() races drain()'s
	// in-flight Encode here.
	time.Sleep(50 * time.Millisecond)

	// Release the gated Notify write now. On the buggy implementation,
	// serveWithScopes's conn.Close() has already run concurrently with
	// this point (the client's read side is still open and would happily
	// accept the frame), so the release write fails against our own
	// already-closed local socket — drain() swallows that error
	// (`_ = w.enc.Encode(frame)`), so Notify's own return value cannot
	// observe the loss: the frame is gone with no signal anywhere. The fix
	// must make serve() block until drain() has actually finished flushing
	// the already-enqueued frame (w.stopped) before conn.Close() runs, so
	// the release write above always still lands on a conn that is not yet
	// closed.
	close(gated.release)

	// Wait for the gated Write call itself to actually return (not just
	// for Notify/serve to return — Notify returns as soon as enqueue()
	// accepts the frame onto the channel, well before drain() calls Write,
	// so checking lastWriteErr() without this synchronization would race
	// the goroutine that resumes the gated write after release).
	if !gated.awaitGatedWriteDone(2 * time.Second) {
		t.Fatal("gated write never completed after release")
	}

	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("serve() never returned")
	}

	select {
	case err := <-notifyErrCh:
		if err != nil {
			t.Fatalf("Notify returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Notify never returned")
	}

	// The definitive check: did drain()'s Encode of the already-enqueued
	// Notify frame actually succeed against a live conn, or did it race a
	// concurrent conn.Close() and get silently dropped? If serveWithScopes
	// closes the connection before drain() finishes flushing an
	// already-enqueued frame, this gated write fails (e.g. "use of closed
	// network connection") — and neither Notify's return value nor
	// serve() returning gives any signal that happened.
	if err := gated.lastWriteErr(); err != nil {
		t.Fatalf("the already-enqueued Notify frame's Write failed (%v): serveWithScopes closed the connection before drain() finished flushing it — a frame enqueue() already reported as accepted was silently lost", err)
	}

	// Belt-and-suspenders: the client's still-open read side must actually
	// receive the frame's bytes on the wire, not just see a nil local
	// write error.
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("client never received the Notify frame on the wire: %v", err)
	}
	var got struct {
		Method string `json:"method"`
		Params struct {
			Kind string `json:"kind"`
		} `json:"params"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("undecodable Notify frame: %v: %s", err, line)
	}
	if got.Method != "event" || got.Params.Kind != "approval_resolved" {
		t.Fatalf("client received unexpected frame: %s", line)
	}
}
