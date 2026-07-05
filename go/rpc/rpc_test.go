package rpc

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestServerClientRoundTrip(t *testing.T) {
	s := NewServer()
	s.Register("echo", func(params json.RawMessage) (any, error) {
		var p struct {
			Msg string `json:"msg"`
		}
		_ = json.Unmarshal(params, &p)
		return map[string]string{"echo": p.Msg}, nil
	})
	s.Register("boom", func(_ json.RawMessage) (any, error) {
		return nil, &Error{Code: CodeInternalError, Message: "kaboom"}
	})

	sock := filepath.Join(t.TempDir(), "s.sock")
	go func() { _ = s.ListenUnix(sock) }()
	defer s.Close()
	waitSock(t, sock)

	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var out struct {
		Echo string `json:"echo"`
	}
	if err := c.Call("echo", map[string]any{"msg": "hi"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	if out.Echo != "hi" {
		t.Fatalf("expected echo hi, got %q", out.Echo)
	}

	// Unknown method -> method-not-found error.
	if err := c.Call("nope", map[string]any{}, nil); err == nil {
		t.Fatal("unknown method should error")
	}
	// Handler error is surfaced.
	if err := c.Call("boom", map[string]any{}, nil); err == nil {
		t.Fatal("handler error should surface")
	}
}

func TestStreamNotifications(t *testing.T) {
	s := NewServer()
	s.RegisterStream("sub", func(_ json.RawMessage, sub *Subscription) error {
		go func() {
			time.Sleep(20 * time.Millisecond)
			_ = sub.Notify("event", map[string]string{"type": "ping"})
		}()
		return nil
	})
	sock := filepath.Join(t.TempDir(), "s2.sock")
	go func() { _ = s.ListenUnix(sock) }()
	defer s.Close()
	waitSock(t, sock)

	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Call("sub", map[string]any{}, &struct{}{}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	method, params, err := c.ReadNotification()
	if err != nil {
		t.Fatalf("read notification: %v", err)
	}
	if method != "event" {
		t.Fatalf("expected event notification, got %q", method)
	}
	var ev struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(params, &ev)
	if ev.Type != "ping" {
		t.Fatalf("expected ping, got %q", ev.Type)
	}
}

func TestTCPRoundTrip(t *testing.T) {
	s := NewServer()
	s.Register("ping", func(_ json.RawMessage) (any, error) { return map[string]bool{"ok": true}, nil })
	s.MarkRemoteSafe("ping") // TCP transport is now origin-restricted

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	_ = ln.Close()
	go func() { _ = s.ListenTCP(addr) }()
	defer s.Close()
	for i := 0; i < 100; i++ {
		if conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	c, err := DialTCP(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.OnNotify(func(_ string, _ json.RawMessage) {})

	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.Call("ping", map[string]any{}, &out); err != nil || !out.OK {
		t.Fatalf("ping over tcp: %v %+v", err, out)
	}
}

func TestClientNilCloser(t *testing.T) {
	c := NewClient(nil, nil, nil)
	if err := c.Close(); err != nil {
		t.Fatalf("close with nil closer should be nil, got %v", err)
	}
}

func TestDialErrors(t *testing.T) {
	if _, err := Dial("/nonexistent/pi.sock"); err == nil {
		t.Fatal("dial of missing socket should error")
	}
	if _, err := DialTCP("127.0.0.1:1"); err == nil {
		t.Fatal("dial of dead port should error")
	}
}

func waitSock(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("socket never came up")
}
