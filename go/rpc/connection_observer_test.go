package rpc

import (
	"net"
	"sync"
	"testing"
	"time"
)

type recordingConnectionObserver struct {
	mu     sync.Mutex
	opens  []Origin
	closes []Origin
}

func (o *recordingConnectionObserver) ConnectionOpened(origin Origin) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.opens = append(o.opens, origin)
}

func (o *recordingConnectionObserver) ConnectionClosed(origin Origin) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closes = append(o.closes, origin)
}

func TestConnectionObserverReceivesExactlyOneOpenAndClose(t *testing.T) {
	server := NewServer()
	observer := &recordingConnectionObserver{}
	server.SetConnectionObserver(observer)
	serverConn, clientConn := net.Pipe()
	done := make(chan struct{})
	go func() {
		server.serve(serverConn, OriginLocal)
		close(done)
	}()
	if err := clientConn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server connection did not close")
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.opens) != 1 || observer.opens[0] != OriginLocal {
		t.Fatalf("opens = %v", observer.opens)
	}
	if len(observer.closes) != 1 || observer.closes[0] != OriginLocal {
		t.Fatalf("closes = %v", observer.closes)
	}
}
