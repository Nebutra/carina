package tui

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func TestConnectionControllerPrepareRejectsChangedSourceTargetAtSameGeneration(t *testing.T) {
	attachStarted := make(chan struct{})
	releaseAttach := make(chan struct{})
	server := rpc.NewServer()
	var once sync.Once
	server.Register("session.attach", func(_ json.RawMessage) (any, error) {
		once.Do(func() {
			close(attachStarted)
			<-releaseAttach
		})
		return map[string]any{"events": []any{}, "from": 0, "cursor": 0}, nil
	})
	server.RegisterStream("session.events.stream", func(_ json.RawMessage, _ *rpc.Subscription) error { return nil })
	dir, err := os.MkdirTemp("", "carina-rebind-snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "runtime.sock")
	go func() { _ = server.ListenUnix(socket) }()
	defer server.Close()
	waitForRebindSocket(t, socket)

	source := ConnectionTarget{Socket: "/source.sock", WorkspaceRoot: "/source", StateDir: "/source-state"}
	controller := NewConnectionController(source)
	errCh := make(chan error, 1)
	go func() {
		_, err := controller.PrepareTarget(ConnectionTarget{Socket: socket, SessionID: "sess_target"})
		errCh <- err
	}()
	<-attachStarted
	controller.adoptCreated("sess_created")
	close(releaseAttach)
	if err := <-errCh; err == nil {
		t.Fatal("prepare accepted a stale source target snapshot")
	}
	got, generation := controller.targetState(source)
	if got.SessionID != "sess_created" || generation != 1 {
		t.Fatalf("source target changed unexpectedly: target=%+v generation=%d", got, generation)
	}
}

func TestConnectionControllerPrepareFailurePreservesSourceTarget(t *testing.T) {
	source := ConnectionTarget{Socket: "/source.sock", SessionID: "sess_source", WorkspaceRoot: "/source", StateDir: "/source-state"}
	controller := NewConnectionController(source)
	_, generation := controller.targetState(source)

	target := ConnectionTarget{Socket: filepath.Join(t.TempDir(), "missing.sock"), SessionID: "sess_target", WorkspaceRoot: "/target", StateDir: "/target-state"}
	if _, err := controller.PrepareTarget(target); err == nil {
		t.Fatal("prepare unexpectedly succeeded")
	}
	got, gotGeneration := controller.targetState(source)
	if !sameConnectionTarget(got, source) || gotGeneration != generation {
		t.Fatalf("source changed after failed prepare: target=%+v generation=%d want=%+v/%d", got, gotGeneration, source, generation)
	}
}

func TestConnectionControllerPublishesOnlyPreparedTarget(t *testing.T) {
	socket, stop := startRebindRPCServer(t, false)
	defer stop()
	source := ConnectionTarget{Socket: "/source.sock", SessionID: "sess_source", WorkspaceRoot: "/source", StateDir: "/source-state"}
	target := ConnectionTarget{Socket: socket, SessionID: "sess_target", WorkspaceRoot: "/target", StateDir: "/target-state"}
	controller := NewConnectionController(source)
	_, generation := controller.targetState(source)

	token, err := controller.PrepareTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	got, gotGeneration := controller.targetState(source)
	if !sameConnectionTarget(got, source) || gotGeneration != generation {
		t.Fatalf("prepare published destination early: %+v/%d", got, gotGeneration)
	}
	if err := controller.CommitPrepared(token); err != nil {
		t.Fatal(err)
	}
	got, gotGeneration = controller.targetState(source)
	if !sameConnectionTarget(got, target) || gotGeneration <= generation {
		t.Fatalf("commit did not publish destination: %+v/%d", got, gotGeneration)
	}
	prepared := controller.takePrepared(gotGeneration)
	if prepared == nil {
		t.Fatal("committed prepared clients were not available to the connection loop")
	}
	prepared.close()
}

func TestConnectionControllerAbortCommittedTargetRestoresCompleteSourceWithCollidingSessionID(t *testing.T) {
	socket, stop := startRebindRPCServer(t, false)
	defer stop()
	source := ConnectionTarget{
		Socket: "/source.sock", SessionID: "sess_same", WorkspaceRoot: "/source",
		StateDir: "/source-state", AutoStart: true,
	}
	target := ConnectionTarget{
		Socket: socket, SessionID: "sess_same", WorkspaceRoot: "/target",
		StateDir: "/target-state",
	}
	controller := NewConnectionController(source)
	token, err := controller.PrepareTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitPrepared(token); err != nil {
		t.Fatal(err)
	}
	committed, committedGeneration := controller.targetState(source)
	if !sameConnectionTarget(committed, target) {
		t.Fatalf("destination not committed: %+v", committed)
	}
	if controller.takePrepared(committedGeneration) == nil {
		t.Fatal("destination clients were not adopted")
	}

	restoredGeneration := controller.AbortPrepared(token)
	restored, generation := controller.targetState(source)
	if !sameConnectionTarget(restored, source) {
		t.Fatalf("abort restored mixed target: got=%+v want=%+v", restored, source)
	}
	if generation != restoredGeneration || generation <= committedGeneration {
		t.Fatalf("rollback generation=%d returned=%d committed=%d", generation, restoredGeneration, committedGeneration)
	}
}

func TestConnectionControllerAcknowledgeDropsRollbackReceipt(t *testing.T) {
	socket, stop := startRebindRPCServer(t, false)
	defer stop()
	controller := NewConnectionController(ConnectionTarget{Socket: "/source.sock", SessionID: "sess_source"})
	token, err := controller.PrepareTarget(ConnectionTarget{Socket: socket, SessionID: "sess_target"})
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitPrepared(token); err != nil {
		t.Fatal(err)
	}
	controller.AcknowledgePrepared(token)
	if _, ok := controller.prepared[token]; ok {
		t.Fatal("acknowledged rollback receipt was retained")
	}
}

func TestConnectionControllerAttachFailurePreservesSource(t *testing.T) {
	socket, stop := startRebindRPCServer(t, true)
	defer stop()
	source := ConnectionTarget{Socket: "/source.sock", SessionID: "sess_source", WorkspaceRoot: "/source"}
	controller := NewConnectionController(source)
	_, generation := controller.targetState(source)
	_, err := controller.PrepareTarget(ConnectionTarget{Socket: socket, SessionID: "sess_target", WorkspaceRoot: "/target"})
	if err == nil {
		t.Fatal("attach failure unexpectedly prepared destination")
	}
	got, gotGeneration := controller.targetState(source)
	if !sameConnectionTarget(got, source) || gotGeneration != generation {
		t.Fatalf("attach failure changed source: %+v/%d", got, gotGeneration)
	}
}

func startRebindRPCServer(t *testing.T, failAttach bool) (string, func()) {
	t.Helper()
	server := rpc.NewServer()
	server.Register("session.attach", func(_ json.RawMessage) (any, error) {
		if failAttach {
			return nil, errors.New("attach rejected")
		}
		return map[string]any{"events": []any{}, "from": 0, "cursor": 0}, nil
	})
	server.RegisterStream("session.events.stream", func(_ json.RawMessage, _ *rpc.Subscription) error { return nil })
	dir, err := os.MkdirTemp("", "carina-rebind-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "runtime.sock")
	go func() { _ = server.ListenUnix(socket) }()
	waitForRebindSocket(t, socket)
	return socket, func() {
		_ = server.Close()
		_ = os.RemoveAll(dir)
	}
}

func waitForRebindSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socket, 20*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("rebind RPC server did not start")
}
