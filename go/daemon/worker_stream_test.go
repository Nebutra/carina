package daemon_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/daemon"
	"github.com/Nebutra/carina/go/rpc"
)

// TestRemoteWorkerAndEventStream covers PRD §8.6 / Phase 3: a worker joins
// over TCP and heartbeats, and a client streams live session events.
func TestRemoteWorkerAndEventStream(t *testing.T) {
	repoRoot := repoRoot(t)
	kernelBin := firstExisting(
		os.Getenv("CARINA_KERNEL_BIN"),
		filepath.Join(repoRoot, "target/release/carina-kernel-service"),
		filepath.Join(repoRoot, "target/debug/carina-kernel-service"),
	)
	if kernelBin == "" {
		t.Skip("carina-kernel-service not built")
	}
	stateDir := t.TempDir()
	ws := t.TempDir()

	d, err := daemon.New(daemon.Options{StateDir: stateDir, KernelBin: kernelBin, ToolsDir: filepath.Join(repoRoot, "zig/zig-out/bin")})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	sock := shortSocket(t)
	go func() { _ = d.Run(sock) }()
	waitForSocket(t, sock)

	// Bring up TCP on a free port for the remote worker.
	tcpAddr := freeAddr(t)
	go func() { _ = d.RunTCP(tcpAddr) }()
	waitForDial(t, tcpAddr)

	// --- remote worker over TCP ---
	wc, err := rpc.DialTCP(tcpAddr)
	if err != nil {
		t.Fatalf("worker dial: %v", err)
	}
	defer wc.Close()
	var reg struct {
		WorkerID         string `json:"worker_id"`
		WorkerCredential string `json:"worker_credential"`
	}
	if err := wc.Call("worker.register", map[string]any{"name": "ci-1", "kind": "ci"}, &reg); err != nil {
		t.Fatalf("worker.register: %v", err)
	}
	if reg.WorkerID == "" || reg.WorkerCredential == "" {
		t.Fatalf("expected worker id and credential, got %+v", reg)
	}
	if err := wc.Call("worker.heartbeat", map[string]any{"worker_id": reg.WorkerID, "worker_credential": reg.WorkerCredential}, &struct{}{}); err != nil {
		t.Fatalf("worker.heartbeat: %v", err)
	}
	var workers []map[string]any
	if err := wc.Call("worker.list", map[string]any{}, &workers); err != nil {
		t.Fatal(err)
	}
	// local + ci-1 = at least 2.
	if len(workers) < 2 {
		t.Fatalf("expected >=2 workers, got %d", len(workers))
	}

	// --- live event stream ---
	sc, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer sc.Close()
	var sess struct {
		SessionID string `json:"session_id"`
	}
	if err := sc.Call("session.create", map[string]any{"workspace_root": ws, "profile": "safe-edit"}, &sess); err != nil {
		t.Fatal(err)
	}

	// Subscriber on its own connection.
	streamConn, err := rpc.Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer streamConn.Close()
	if err := streamConn.Call("session.events.stream", map[string]any{"session_id": sess.SessionID}, &struct{}{}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	got := make(chan string, 8)
	go func() {
		for {
			method, params, err := streamConn.ReadNotification()
			if err != nil {
				return
			}
			if method == "event" {
				var ev struct {
					Type string `json:"type"`
				}
				_ = json.Unmarshal(params, &ev)
				got <- ev.Type
			}
		}
	}()

	// Trigger events by submitting a task.
	if err := sc.Call("task.submit", map[string]any{"session_id": sess.SessionID, "prompt": "hi"}, &struct{}{}); err != nil {
		t.Fatal(err)
	}

	select {
	case typ := <-got:
		if typ == "" {
			t.Fatal("received an empty event")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event streamed within 5s")
	}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitForDial(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(fmt.Sprintf("tcp %s never came up", addr))
}
