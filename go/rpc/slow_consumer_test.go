package rpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type gatedWriter struct {
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (w *gatedWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestSubscriptionTryNotifyReportsBoundedQueueSaturation(t *testing.T) {
	done := make(chan struct{})
	writer := &gatedWriter{entered: make(chan struct{}), release: make(chan struct{})}
	cw := newConnWriter(json.NewEncoder(writer), done)
	sub := &Subscription{id: "s", w: cw, done: done}
	if err := sub.TryNotify("event", map[string]any{"n": 0}); err != nil {
		t.Fatal(err)
	}
	<-writer.entered
	for i := 0; i < cap(cw.queue); i++ {
		if err := sub.TryNotify("event", map[string]any{"n": i + 1}); err != nil {
			t.Fatalf("queue filled early at %d: %v", i, err)
		}
	}
	if err := sub.TryNotify("event", map[string]any{"overflow": true}); !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("want ErrSlowConsumer, got %v", err)
	}
	close(done)
	close(writer.release)
	<-cw.stopped
}

func TestSubscriptionCommitResultIsSingleUseAndNonBlocking(t *testing.T) {
	done := make(chan struct{})
	writer := &gatedWriter{entered: make(chan struct{}), release: make(chan struct{})}
	cw := newConnWriter(json.NewEncoder(writer), done)
	sub := &Subscription{id: "s", w: cw, done: done, requestID: json.RawMessage("9")}
	if err := sub.TryNotify("event", map[string]any{"n": 0}); err != nil {
		t.Fatal(err)
	}
	<-writer.entered
	for i := 0; i < cap(cw.queue); i++ {
		if err := sub.TryNotify("event", map[string]any{"n": i + 1}); err != nil {
			t.Fatalf("queue filled early at %d: %v", i, err)
		}
	}
	if err := sub.CommitResult(map[string]any{"committed": true}); !errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("full queue commit = %v, want ErrSlowConsumer", err)
	}
	if err := sub.CommitResult(map[string]any{"committed": true}); !errors.Is(err, ErrSubscriptionResponseCommitted) {
		t.Fatalf("duplicate commit = %v, want ErrSubscriptionResponseCommitted", err)
	}
	close(done)
	close(writer.release)
	<-cw.stopped
}

func TestSubscriptionCommitResultRejectsClosedConnection(t *testing.T) {
	done := make(chan struct{})
	close(done)
	cw := newConnWriter(json.NewEncoder(&strings.Builder{}), done)
	sub := &Subscription{id: "s", w: cw, done: done, requestID: json.RawMessage("10")}
	if err := sub.CommitResult(map[string]any{"committed": true}); err == nil || errors.Is(err, ErrSlowConsumer) {
		t.Fatalf("closed connection commit = %v", err)
	}
	if err := sub.CommitResult(map[string]any{"committed": true}); !errors.Is(err, ErrSubscriptionResponseCommitted) {
		t.Fatalf("duplicate closed commit = %v, want ErrSubscriptionResponseCommitted", err)
	}
	<-cw.stopped
}

func TestFailedStreamCommitClosesWithoutLegacyErrorEnqueue(t *testing.T) {
	s := NewServer()
	serverRaw, clientConn := net.Pipe()
	defer clientConn.Close()
	gated := newGatedConn(serverRaw, 1)
	handlerDone := make(chan error, 1)
	var subDone <-chan struct{}
	s.RegisterStream("sub.full", func(_ json.RawMessage, sub *Subscription) error {
		subDone = sub.Done()
		if err := sub.TryNotify("event", map[string]any{"n": 0}); err != nil {
			return err
		}
		<-gated.started
		for i := 0; i < cap(sub.w.queue); i++ {
			if err := sub.TryNotify("event", map[string]any{"n": i + 1}); err != nil {
				return err
			}
		}
		err := sub.CommitResult(map[string]any{"committed": true})
		_ = sub.Disconnect()
		handlerDone <- err
		return err
	})
	serveDone := make(chan struct{})
	go func() {
		s.serveWithScopes(gated, OriginLocal, nil)
		close(serveDone)
	}()

	request := Request{JSONRPC: "2.0", ID: json.RawMessage("12"), Method: "sub.full"}
	if err := json.NewEncoder(clientConn).Encode(request); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-handlerDone:
		if !errors.Is(err, ErrSlowConsumer) {
			t.Fatalf("full queue commit = %v, want ErrSlowConsumer", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream handler did not return after failed commit")
	}
	select {
	case <-subDone:
		// Reaching the connection teardown while the writer queue is still
		// full proves serveWithScopes did not attempt a blocking legacy error.
	case <-time.After(2 * time.Second):
		t.Fatal("server blocked enqueueing a legacy error after failed commit")
	}

	close(gated.release)
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not finish after failed commit disconnect")
	}
	_ = clientConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	line, err := bufio.NewReader(clientConn).ReadBytes('\n')
	if err == nil {
		var response Response
		if json.Unmarshal(line, &response) == nil && response.ID != nil {
			t.Fatalf("failed commit emitted a response frame: %s", line)
		}
	}
}
