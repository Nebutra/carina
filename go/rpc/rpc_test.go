package rpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		return nil, &Error{Code: -32010, Message: "kaboom", Data: map[string]any{"code": "cursor_expired"}}
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
	// Typed handler errors preserve their application code and recovery data.
	if err := c.Call("boom", map[string]any{}, nil); err == nil {
		t.Fatal("handler error should surface")
	} else {
		var rpcErr *Error
		if !errors.As(err, &rpcErr) {
			t.Fatalf("expected rpc error, got %T: %v", err, err)
		}
		if rpcErr.Code != -32010 {
			t.Fatalf("rpc error code = %d, want -32010", rpcErr.Code)
		}
		data, ok := rpcErr.Data.(map[string]any)
		if !ok || data["code"] != "cursor_expired" {
			t.Fatalf("rpc error data = %#v", rpcErr.Data)
		}
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

func TestStreamReturnsSubscriptionIdentityAndCatchUpCursor(t *testing.T) {
	s := NewServer()
	s.RegisterStream("sub.cursor", func(_ json.RawMessage, sub *Subscription) error {
		sub.SetResult(map[string]any{"subscription_id": sub.ID(), "cursor": 12, "replayed": 3})
		return nil
	})
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("carina-cursor-%d.sock", time.Now().UnixNano()))
	defer os.Remove(sock)
	go func() { _ = s.ListenUnix(sock) }()
	defer s.Close()
	waitSock(t, sock)
	c, err := Dial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var got struct {
		SubscriptionID string `json:"subscription_id"`
		Cursor         int    `json:"cursor"`
		Replayed       int    `json:"replayed"`
	}
	if err := c.Call("sub.cursor", map[string]any{"since": 9}, &got); err != nil {
		t.Fatal(err)
	}
	if got.SubscriptionID == "" || got.Cursor != 12 || got.Replayed != 3 {
		t.Fatalf("unexpected stream result: %+v", got)
	}
}

func TestStreamCommittedResponseSkipsLegacyPostHandlerResponse(t *testing.T) {
	s := NewServer()
	s.RegisterStream("sub.commit", func(_ json.RawMessage, sub *Subscription) error {
		return sub.CommitResult(map[string]any{"subscription_id": sub.ID(), "committed": true})
	})
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	go s.serveWithScopes(serverConn, OriginLocal, nil)

	request := Request{JSONRPC: "2.0", ID: json.RawMessage("7"), Method: "sub.commit"}
	if err := json.NewEncoder(clientConn).Encode(request); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.NewDecoder(clientConn).Decode(&response); err != nil {
		t.Fatal(err)
	}
	result, ok := response.Result.(map[string]any)
	if !ok || result["committed"] != true {
		t.Fatalf("committed response = %#v", response)
	}

	if err := clientConn.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if err := json.NewDecoder(clientConn).Decode(&response); err == nil {
		t.Fatalf("legacy response followed committed response: %#v", response)
	} else if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
		t.Fatalf("second response read = %v, want timeout", err)
	}
}

func TestFailedStreamCommitDoesNotBlockOnLegacyErrorResponse(t *testing.T) {
	done := make(chan struct{})
	writer := &gatedWriter{entered: make(chan struct{}), release: make(chan struct{})}
	cw := newConnWriter(json.NewEncoder(writer), done)
	sub := &Subscription{id: "s", w: cw, done: done, requestID: json.RawMessage("11")}
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
	if !sub.responseWasAttempted() {
		t.Fatal("failed commit did not claim the response boundary")
	}
	close(done)
	close(writer.release)
	<-cw.stopped
}

func TestNotificationListenersCoexistAndCancelIndependently(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer serverSide.Close()
	client := NewClient(clientSide, clientSide, clientSide)
	defer client.Close()
	var mu sync.Mutex
	var first, second, legacy int
	client.OnNotify(func(string, json.RawMessage) { mu.Lock(); legacy++; mu.Unlock() })
	cancelFirst := client.AddNotificationListener(func(string, json.RawMessage) { mu.Lock(); first++; mu.Unlock() })
	client.AddNotificationListener(func(string, json.RawMessage) { mu.Lock(); second++; mu.Unlock() })
	go func() {
		reader := bufio.NewReader(serverSide)
		for i := 0; i < 2; i++ {
			line, _ := reader.ReadBytes('\n')
			var req Request
			_ = json.Unmarshal(line, &req)
			note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "event", "params": map[string]any{"n": i}})
			_, _ = serverSide.Write(append(note, '\n'))
			resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
			_, _ = serverSide.Write(append(resp, '\n'))
		}
	}()
	if err := client.Call("one", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	cancelFirst()
	cancelFirst()
	if err := client.Call("two", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if first != 1 || second != 2 || legacy != 2 {
		t.Fatalf("listener calls first=%d second=%d legacy=%d", first, second, legacy)
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

func TestTCPRejectsNonLoopbackListenAddress(t *testing.T) {
	s := NewServer()
	defer s.Close()
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", ":0"} {
		if err := s.ListenTCP(addr); err == nil || !strings.Contains(err.Error(), "restricted to explicit loopback") {
			t.Fatalf("ListenTCP(%q) error = %v, want loopback restriction", addr, err)
		}
	}
}

func TestDescriptorStrictMode(t *testing.T) {
	s := NewServer()
	s.Register("legacy", func(_ json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	})
	if err := s.RegisterMethod(MethodDescriptor{
		Method:    "classified",
		Scope:     ScopeRead,
		Remote:    true,
		Advertise: true,
	}, func(_ json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	s.RequireDescriptors(true)

	if resp := s.dispatch(Request{Method: "classified"}); resp.Error != nil {
		t.Fatalf("classified method should run: %+v", resp.Error)
	}
	if resp := s.dispatch(Request{Method: "legacy"}); resp.Error == nil {
		t.Fatal("strict mode should reject unclassified registered handlers")
	}
	if resp := s.dispatch(Request{Method: "missing"}); resp.Error == nil || resp.Error.Message != "method not found: missing" {
		t.Fatalf("strict mode should keep unknown methods as method-not-found, got %+v", resp.Error)
	}
	if ok, _ := s.remoteAuthorized("classified", OriginRemote); !ok {
		t.Fatal("descriptor remote=true should allow remote access")
	}
}

func TestDynamicScopeResolver(t *testing.T) {
	s := NewServer()
	if err := s.RegisterMethodDynamic(MethodDescriptor{
		Method: "mixed.patch",
		Scope:  ScopeWrite,
	}, func(_ json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	}, func(params json.RawMessage) (Scope, error) {
		var p struct {
			Admin bool `json:"admin"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return "", err
		}
		if p.Admin {
			return ScopeAdmin, nil
		}
		return ScopeWrite, nil
	}); err != nil {
		t.Fatal(err)
	}
	scope, dynamic, err := s.ResolveScope("mixed.patch", mustJSON(t, map[string]bool{"admin": false}))
	if err != nil || !dynamic || scope != ScopeWrite {
		t.Fatalf("write scope: scope=%s dynamic=%v err=%v", scope, dynamic, err)
	}
	scope, dynamic, err = s.ResolveScope("mixed.patch", mustJSON(t, map[string]bool{"admin": true}))
	if err != nil || !dynamic || scope != ScopeAdmin {
		t.Fatalf("admin scope: scope=%s dynamic=%v err=%v", scope, dynamic, err)
	}
	descs := s.MethodDescriptors()
	if len(descs) != 1 || !descs[0].DynamicScope {
		t.Fatalf("descriptor should advertise dynamic scope: %+v", descs)
	}
}

func TestGatewayScopeNegotiation(t *testing.T) {
	role, scopes, notes, err := NegotiateScopes(RoleOperator, []Scope{ScopeAdmin, ScopeRead, ScopeWorker})
	if err != nil {
		t.Fatal(err)
	}
	if role != RoleOperator || len(scopes) != 2 || scopes[0] != ScopeRead || scopes[1] != ScopeAdmin {
		t.Fatalf("unexpected negotiation: role=%s scopes=%v", role, scopes)
	}
	if notes != nil {
		t.Fatalf("explicit scopes should not add notes: %v", notes)
	}
	role, scopes, notes, err = NegotiateScopes("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if role != RoleObserver || len(scopes) != 2 || scopes[0] != ScopeRead || scopes[1] != ScopeStream || len(notes) == 0 {
		t.Fatalf("default negotiation mismatch: role=%s scopes=%v notes=%v", role, scopes, notes)
	}
	if _, _, _, err := NegotiateScopes(Role("root"), nil); err == nil {
		t.Fatal("unsupported role should fail")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestClientNilCloser(t *testing.T) {
	c := NewClient(nil, nil, nil)
	if err := c.Close(); err != nil {
		t.Fatalf("close with nil closer should be nil, got %v", err)
	}
}

func TestDialErrors(t *testing.T) {
	if _, err := Dial("/nonexistent/carina.sock"); err == nil {
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
