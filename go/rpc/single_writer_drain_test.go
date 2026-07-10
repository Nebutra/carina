package rpc

import (
	"bufio"
	"encoding/json"
	"net"
	"sync"
	"testing"
	"time"
)

// gatedConn wraps a net.Conn and lets a test deterministically pause a
// specific Write call mid-flight: the Nth call to Write blocks on release
// until the test explicitly lets it through, after first signaling started
// so the test knows the write has actually been entered (not merely
// scheduled). This replaces relying on OS/runtime scheduling luck (socket
// buffer sizes, net.Pipe's unbuffered-channel send fairness, goroutine
// scheduling) to force two independent writers to interleave — it makes the
// interleaving unconditional and reproducible.
type gatedConn struct {
	net.Conn
	mu       sync.Mutex
	n        int
	gateAt   int
	started  chan struct{}
	release  chan struct{}
	gateDone chan struct{}
	lastErr  error
}

func newGatedConn(c net.Conn, gateAt int) *gatedConn {
	return &gatedConn{Conn: c, gateAt: gateAt, started: make(chan struct{}), release: make(chan struct{}), gateDone: make(chan struct{})}
}

func (g *gatedConn) Write(b []byte) (int, error) {
	g.mu.Lock()
	g.n++
	n := g.n
	g.mu.Unlock()
	if n == g.gateAt {
		close(g.started)
		<-g.release
	}
	wn, err := g.Conn.Write(b)
	g.mu.Lock()
	g.lastErr = err
	g.mu.Unlock()
	if n == g.gateAt {
		close(g.gateDone)
	}
	return wn, err
}

// lastWriteErr reports the error (if any) returned by the most recently
// completed Write call, so a test can assert whether a specific gated write
// actually reached a still-open conn or raced a concurrent Close().
func (g *gatedConn) lastWriteErr() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastErr
}

// awaitGatedWriteDone blocks until the gated Write call (the gateAt-th one)
// has actually returned, so a test can inspect lastWriteErr() without a
// scheduling race against the goroutine that resumes it after release.
func (g *gatedConn) awaitGatedWriteDone(timeout time.Duration) bool {
	select {
	case <-g.gateDone:
		return true
	case <-time.After(timeout):
		return false
	}
}

// TestSingleWriterDrainPreservesEnqueueOrder is the enforcement mechanism for
// P1.8's single-writer-drain hardener. serveWithScopes (the per-connection
// request dispatch loop) calls `enc.Encode(s.dispatch(req))` directly with NO
// lock at all around it. Subscription.Notify (invoked by Bus.Publish from ANY
// other goroutine — the agent loop, the approval resolver, worker-report
// handlers, etc.) calls enc.Encode on the SAME *json.Encoder guarded only by
// Subscription's own private mu. These are two independent, asymmetrically
// guarded writers on one shared net.Conn: nothing coordinates a Notify call
// against serveWithScopes's own response write, so whichever one's
// underlying Write() completes first is what lands on the wire first —
// regardless of which was logically enqueued first.
//
// This test wraps the server's connection in gatedConn to deterministically
// pause the Notify call's own Write() call mid-flight (proving it was
// enqueued and is actively in progress, not merely scheduled), then issues
// an ordinary request on the same connection and lets serveWithScopes race
// to encode and write its response while the Notify's write is still held
// open. Single-writer-drain must guarantee the Notify frame — enqueued
// strictly before the request was even sent — reaches the wire before the
// response frame for a request sent later.
//
// The concrete governance failure (docs/plans/agent-cli-productization.md
// §P1.8): an approval-resolution event published from the daemon's record()
// call (essentially a Notify) must never be observed on the wire after an
// unrelated response for a request issued strictly later — a watching
// client (TUI, audit tail) reads wire order as event order.
//
// Expected to FAIL today: serveWithScopes's own enc.Encode(response) call
// takes no lock relative to a concurrently in-flight Notify, so it completes
// and lands on the wire while an earlier-enqueued Notify is still gated,
// producing wire order [response notify] instead of [notify response].
func TestSingleWriterDrainPreservesEnqueueOrder(t *testing.T) {
	s := NewServer()

	if err := s.RegisterMethod(MethodDescriptor{Method: "ping", Scope: ScopeRead, Remote: false}, func(_ json.RawMessage) (any, error) {
		return map[string]any{"kind": "response"}, nil
	}); err != nil {
		t.Fatal(err)
	}

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

	serverRaw, clientConn := net.Pipe()
	// gateAt=2: the subscribe ack is Write #1 (issued synchronously inside
	// serveWithScopes before we ever call Notify), so the Notify call below
	// is Write #2 — the first write this test actually gates.
	gated := newGatedConn(serverRaw, 2)
	go s.serve(gated, OriginLocal)

	writeLine := func(v any) {
		b, _ := json.Marshal(v)
		b = append(b, '\n')
		_, _ = clientConn.Write(b)
	}
	reader := bufio.NewReader(clientConn)
	readLine := func() []byte {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatalf("read line: %v", err)
		}
		return line
	}

	writeLine(Request{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "watch"})
	_ = readLine() // subscribe ack (gatedConn Write #1)

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

	notifyDone := make(chan struct{})
	go func() {
		_ = sub.Notify("event", map[string]any{"kind": "approval_resolved"})
		close(notifyDone)
	}()

	// Wait until the Notify's Write call has actually been entered and is
	// gated (proving it was enqueued and is in progress) before doing
	// anything else.
	select {
	case <-gated.started:
	case <-time.After(2 * time.Second):
		t.Fatal("Notify's Write was never entered — gating did not engage")
	}

	// While the Notify's write is held open, send an ordinary request on
	// the SAME connection. serveWithScopes reads it, dispatches it, and
	// calls enc.Encode(response) — Write #3 on this connection — with no
	// coordination against the still-gated Notify write.
	writeLine(Request{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "ping"})

	// Give serveWithScopes a bounded window to actually reach its own
	// Write() call for the ping response while the Notify write remains
	// gated — this is what exposes the missing coordination: nothing
	// prevents this second write from being issued and even completing
	// (on a real, non-gated connection) before the first is released.
	time.Sleep(50 * time.Millisecond)

	// Release the gated Notify write now — this models the Notify write
	// finally winning whatever contention it was in.
	close(gated.release)

	type frame struct {
		Method string `json:"method"`
		Params struct {
			Kind string `json:"kind"`
		} `json:"params"`
		Result struct {
			Kind string `json:"kind"`
		} `json:"result"`
	}
	var order []string
	for i := 0; i < 2; i++ {
		line := readLine()
		var f frame
		if err := json.Unmarshal(line, &f); err != nil {
			t.Fatalf("undecodable frame %d: %v: %s", i, err, line)
		}
		switch {
		case f.Method == "event" && f.Params.Kind == "approval_resolved":
			order = append(order, "notify")
		case f.Result.Kind == "response":
			order = append(order, "response")
		default:
			order = append(order, "unknown:"+string(line))
		}
	}

	<-notifyDone

	if len(order) != 2 || order[0] != "notify" || order[1] != "response" {
		t.Fatalf("single-writer-drain violated: expected wire order [notify response] (the Notify was enqueued and in-flight strictly before the ping request was even sent), got %v — a later request's response reached the wire ahead of an earlier, already-pending Notify, because serveWithScopes's own enc.Encode(response) call is not coordinated against concurrent Notify traffic on the same connection", order)
	}
}
