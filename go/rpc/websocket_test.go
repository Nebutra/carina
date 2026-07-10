package rpc

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestWebSocketGatewayRequiresTokenVerifierBeforeListen(t *testing.T) {
	s := NewServer()
	defer s.Close()
	err := s.ListenWebSocket("127.0.0.1:0", "/gateway", nil)
	if err == nil || !strings.Contains(err.Error(), "requires a token verifier") {
		t.Fatalf("ListenWebSocket without verifier error = %v", err)
	}
}

func TestWebSocketGatewayRoundTripAndRemotePolicy(t *testing.T) {
	s := NewServer()
	if err := s.RegisterMethod(MethodDescriptor{Method: "daemon.status", Scope: ScopeRead, Remote: true}, func(_ json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(MethodDescriptor{Method: "gateway.hello", Scope: ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		var req HelloRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		return BuildHelloResponse(req, "test", s.MethodDescriptors())
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(MethodDescriptor{Method: "worker.register", Scope: ScopeWorker, Remote: true}, func(_ json.RawMessage) (any, error) {
		return map[string]bool{"registered": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(MethodDescriptor{Method: "task.submit", Scope: ScopeWrite, Remote: false}, func(_ json.RawMessage) (any, error) {
		return map[string]bool{"submitted": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() {
		_ = s.ListenWebSocketWithOptions(addr, WebSocketOptions{Path: "/gateway", TokenVerifier: testWebSocketIssuer(t)})
	}()
	defer s.Close()
	waitTCP(t, addr)

	resp := wsCall(t, addr, "", Request{JSONRPC: "2.0", ID: rawID(t, 1), Method: "daemon.status", Params: mustJSON(t, map[string]any{})})
	if resp.Error != nil {
		t.Fatalf("daemon.status over websocket: %+v", resp.Error)
	}
	out, ok := resp.Result.(map[string]any)
	if !ok || out["ok"] != true {
		t.Fatalf("daemon.status result: %+v", resp.Result)
	}

	resp = wsCall(t, addr, "", Request{JSONRPC: "2.0", ID: rawID(t, 2), Method: "task.submit", Params: mustJSON(t, map[string]any{})})
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "method not available over remote transport") {
		t.Fatalf("local-only method should be refused over websocket, got %+v", resp.Error)
	}

	resp = wsCall(t, addr, "", Request{JSONRPC: "2.0", ID: rawID(t, 3), Method: "worker.register", Params: mustJSON(t, map[string]any{})})
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "method scope not negotiated") {
		t.Fatalf("worker method should require negotiated worker scope, got %+v", resp.Error)
	}
	resp = wsCallWithHello(t, addr, "", map[string]any{"role": "worker", "scopes": []string{"worker"}}, Request{JSONRPC: "2.0", ID: rawID(t, 4), Method: "worker.register", Params: mustJSON(t, map[string]any{})})
	if resp.Error != nil {
		t.Fatalf("worker role should call worker.register: %+v", resp.Error)
	}

	s.SetRemoteDisabled(true)
	resp = wsCall(t, addr, "", Request{JSONRPC: "2.0", ID: rawID(t, 5), Method: "gateway.hello", Params: mustJSON(t, map[string]any{"token": testWebSocketToken(t, RoleOperator, []Scope{ScopeRead})})})
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "remote access is disabled") {
		t.Fatalf("remote kill-switch should block websocket, got %+v", resp.Error)
	}
}

func TestWebSocketGatewayTokenScopes(t *testing.T) {
	s := NewServer()
	if err := s.RegisterMethod(MethodDescriptor{Method: "gateway.hello", Scope: ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		var req HelloRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		return BuildHelloResponse(req, "test", s.MethodDescriptors())
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(MethodDescriptor{Method: "daemon.status", Scope: ScopeRead, Remote: true}, func(_ json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(MethodDescriptor{Method: "worker.register", Scope: ScopeWorker, Remote: true}, func(_ json.RawMessage) (any, error) {
		return map[string]bool{"registered": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterMethod(MethodDescriptor{Method: "worker.revoke", Scope: ScopeAdmin, Remote: false}, func(_ json.RawMessage) (any, error) {
		return map[string]bool{"revoked": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	issuer, err := NewGatewayTokenIssuer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() {
		_ = s.ListenWebSocketWithOptions(addr, WebSocketOptions{Path: "/gateway", TokenVerifier: issuer})
	}()
	defer s.Close()
	waitTCP(t, addr)

	readToken := issueGatewayToken(t, issuer, RoleObserver, []Scope{ScopeRead})
	resp := wsCallWithHello(t, addr, "", map[string]any{"token": readToken}, Request{JSONRPC: "2.0", ID: rawID(t, 1), Method: "daemon.status", Params: mustJSON(t, map[string]any{})})
	if resp.Error != nil {
		t.Fatalf("read token should call daemon.status: %+v", resp.Error)
	}
	resp = wsCallWithHello(t, addr, "", map[string]any{"token": readToken}, Request{JSONRPC: "2.0", ID: rawID(t, 2), Method: "worker.register", Params: mustJSON(t, map[string]any{})})
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "method scope not negotiated") {
		t.Fatalf("read token should not call worker.register, got %+v", resp.Error)
	}

	workerToken := issueGatewayToken(t, issuer, RoleWorker, []Scope{ScopeWorker})
	resp = wsCallWithHello(t, addr, "", map[string]any{"token": workerToken}, Request{JSONRPC: "2.0", ID: rawID(t, 3), Method: "worker.register", Params: mustJSON(t, map[string]any{})})
	if resp.Error != nil {
		t.Fatalf("worker token should call worker.register: %+v", resp.Error)
	}

	adminToken := issueGatewayToken(t, issuer, RoleOperator, []Scope{ScopeRead, ScopeAdmin})
	resp = wsCallWithHello(t, addr, "", map[string]any{"token": adminToken}, Request{JSONRPC: "2.0", ID: rawID(t, 4), Method: "worker.revoke", Params: mustJSON(t, map[string]any{})})
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "method not available over remote transport") {
		t.Fatalf("admin token should not bypass remote=false, got %+v", resp.Error)
	}

	c := wsDial(t, addr, "")
	defer c.conn.Close()
	writeWSRequest(t, c, Request{JSONRPC: "2.0", ID: rawID(t, 5), Method: "gateway.hello", Params: mustJSON(t, map[string]any{})})
	payload, err := c.readText()
	if err != nil {
		t.Fatal(err)
	}
	var helloResp Response
	if err := json.Unmarshal(payload, &helloResp); err != nil {
		t.Fatal(err)
	}
	if helloResp.Error == nil || !strings.Contains(helloResp.Error.Message, "gateway token required") {
		t.Fatalf("missing token should fail hello, got %+v", helloResp.Error)
	}
}

func TestWebSocketStreamNotificationAfterHello(t *testing.T) {
	s := NewServer()
	if err := s.RegisterMethod(MethodDescriptor{Method: "gateway.hello", Scope: ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		var req HelloRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		return BuildHelloResponse(req, "test", s.MethodDescriptors())
	}); err != nil {
		t.Fatal(err)
	}

	releaseNotification := make(chan struct{})
	notifyErr := make(chan error, 1)
	release := func() {
		select {
		case <-releaseNotification:
		default:
			close(releaseNotification)
		}
	}
	defer release()

	if err := s.RegisterStreamMethod(MethodDescriptor{Method: "events.subscribe", Scope: ScopeStream, Remote: true}, func(_ json.RawMessage, sub *Subscription) error {
		go func() {
			<-releaseNotification
			notifyErr <- sub.Notify("events.update", map[string]string{"type": "ping"})
		}()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	addr := freeTCPAddr(t)
	go func() {
		_ = s.ListenWebSocketWithOptions(addr, WebSocketOptions{Path: "/gateway", TokenVerifier: testWebSocketIssuer(t)})
	}()
	defer s.Close()
	waitTCP(t, addr)

	c := wsDial(t, addr, "")
	defer c.conn.Close()
	if err := c.conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	defer c.conn.SetReadDeadline(time.Time{})

	writeWSRequest(t, c, Request{JSONRPC: "2.0", ID: rawID(t, 1), Method: "gateway.hello", Params: mustJSON(t, map[string]any{"token": testWebSocketToken(t, RoleOperator, []Scope{ScopeRead, ScopeStream})})})
	helloPayload, err := c.readText()
	if err != nil {
		t.Fatal(err)
	}
	var helloResp Response
	if err := json.Unmarshal(helloPayload, &helloResp); err != nil {
		t.Fatalf("decode websocket hello response %q: %v", string(helloPayload), err)
	}
	if helloResp.Error != nil {
		t.Fatalf("websocket hello failed: %+v", helloResp.Error)
	}
	helloResult, err := json.Marshal(helloResp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var hello HelloResponse
	if err := json.Unmarshal(helloResult, &hello); err != nil {
		t.Fatal(err)
	}
	foundStream := false
	for _, method := range hello.Methods {
		if method.Method == "events.subscribe" && method.Remote && method.Stream && method.Scope == ScopeStream {
			foundStream = true
			break
		}
	}
	if !foundStream {
		t.Fatalf("hello methods did not include remote stream descriptor: %+v", hello.Methods)
	}

	writeWSRequest(t, c, Request{JSONRPC: "2.0", ID: rawID(t, 2), Method: "events.subscribe", Params: mustJSON(t, map[string]any{})})
	subscribePayload, err := c.readText()
	if err != nil {
		t.Fatal(err)
	}
	var subscribeResp Response
	if err := json.Unmarshal(subscribePayload, &subscribeResp); err != nil {
		t.Fatalf("decode websocket subscribe response %q: %v", string(subscribePayload), err)
	}
	if subscribeResp.Error != nil {
		t.Fatalf("websocket subscribe failed: %+v", subscribeResp.Error)
	}
	out, ok := subscribeResp.Result.(map[string]any)
	if !ok || out["subscribed"] != true {
		t.Fatalf("subscribe result: %+v", subscribeResp.Result)
	}

	release()
	notificationPayload, err := c.readText()
	if err != nil {
		t.Fatal(err)
	}
	var note struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(notificationPayload, &note); err != nil {
		t.Fatalf("decode websocket notification %q: %v", string(notificationPayload), err)
	}
	if note.JSONRPC != "2.0" || note.Method != "events.update" {
		t.Fatalf("unexpected notification: %+v", note)
	}
	var event struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(note.Params, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "ping" {
		t.Fatalf("expected ping event, got %q", event.Type)
	}
	select {
	case err := <-notifyErr:
		if err != nil {
			t.Fatalf("stream notify: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream notify did not return")
	}
}

func TestWebSocketRequiresHelloBeforeDispatch(t *testing.T) {
	s := NewServer()
	if err := s.RegisterMethod(MethodDescriptor{Method: "daemon.status", Scope: ScopeRead, Remote: true}, func(_ json.RawMessage) (any, error) {
		return map[string]bool{"ok": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() {
		_ = s.ListenWebSocketWithOptions(addr, WebSocketOptions{Path: "/gateway", TokenVerifier: testWebSocketIssuer(t)})
	}()
	defer s.Close()
	waitTCP(t, addr)

	c := wsDial(t, addr, "")
	defer c.conn.Close()
	raw, err := json.Marshal(Request{JSONRPC: "2.0", ID: rawID(t, 1), Method: "daemon.status", Params: mustJSON(t, map[string]any{})})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writeText(raw); err != nil {
		t.Fatal(err)
	}
	payload, err := c.readText()
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidRequest {
		t.Fatalf("first non-hello frame should be invalid request, got %+v", resp.Error)
	}
}

func TestWebSocketOriginAllowlist(t *testing.T) {
	s := NewServer()
	if err := s.RegisterMethod(MethodDescriptor{Method: "gateway.hello", Scope: ScopeRead, Remote: true}, func(_ json.RawMessage) (any, error) {
		return map[string]string{"version": "1"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	addr := freeTCPAddr(t)
	go func() {
		_ = s.ListenWebSocketWithOptions(addr, WebSocketOptions{Path: "/gateway", AllowedOrigins: []string{"https://app.example"}, TokenVerifier: testWebSocketIssuer(t)})
	}()
	defer s.Close()
	waitTCP(t, addr)

	if status := wsHandshakeStatus(t, addr, "https://evil.example"); status != http.StatusForbidden {
		t.Fatalf("bad browser origin status = %d, want 403", status)
	}
	if status := wsHandshakeStatus(t, addr, ""); status != http.StatusSwitchingProtocols {
		t.Fatalf("native client without Origin status = %d, want 101", status)
	}
	resp := wsCall(t, addr, "https://app.example", Request{JSONRPC: "2.0", ID: rawID(t, 1), Method: "gateway.hello", Params: mustJSON(t, map[string]any{"token": testWebSocketToken(t, RoleOperator, []Scope{ScopeRead})})})
	if resp.Error != nil {
		t.Fatalf("allowed origin should call gateway.hello: %+v", resp.Error)
	}
}

type wsTestConn struct {
	conn net.Conn
	r    *bufio.Reader
}

func wsCall(t *testing.T, addr, origin string, req Request) Response {
	t.Helper()
	return wsCallWithHello(t, addr, origin, map[string]any{}, req)
}

func wsCallWithHello(t *testing.T, addr, origin string, helloParams map[string]any, req Request) Response {
	t.Helper()
	if _, ok := helloParams["token"]; !ok {
		role := RoleOperator
		scopes := []Scope{ScopeRead, ScopeWrite, ScopeAdmin, ScopeStream}
		if helloParams["role"] == string(RoleWorker) || helloParams["role"] == RoleWorker {
			role = RoleWorker
			scopes = []Scope{ScopeWorker}
		}
		helloParams["token"] = testWebSocketToken(t, role, scopes)
	}
	c := wsDial(t, addr, origin)
	defer c.conn.Close()
	if req.Method != "gateway.hello" {
		hello := Request{JSONRPC: "2.0", ID: rawID(t, 0), Method: "gateway.hello", Params: mustJSON(t, helloParams)}
		rawHello, err := json.Marshal(hello)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.writeText(rawHello); err != nil {
			t.Fatal(err)
		}
		payload, err := c.readText()
		if err != nil {
			t.Fatal(err)
		}
		var helloResp Response
		if err := json.Unmarshal(payload, &helloResp); err != nil {
			t.Fatalf("decode websocket hello response %q: %v", string(payload), err)
		}
		if helloResp.Error != nil {
			t.Fatalf("websocket hello failed: %+v", helloResp.Error)
		}
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writeText(raw); err != nil {
		t.Fatal(err)
	}
	payload, err := c.readText()
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(payload, &resp); err != nil {
		t.Fatalf("decode websocket response %q: %v", string(payload), err)
	}
	return resp
}

func testWebSocketIssuer(t *testing.T) *GatewayTokenIssuer {
	t.Helper()
	issuer, err := NewGatewayTokenIssuer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	return issuer
}

func testWebSocketToken(t *testing.T, role Role, scopes []Scope) string {
	t.Helper()
	token, _, err := testWebSocketIssuer(t).Issue("websocket-test", role, scopes, time.Minute, "ws")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func writeWSRequest(t *testing.T, c *wsTestConn, req Request) {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.writeText(raw); err != nil {
		t.Fatal(err)
	}
}

func issueGatewayToken(t *testing.T, issuer *GatewayTokenIssuer, role Role, scopes []Scope) string {
	t.Helper()
	token, _, err := issuer.Issue("test-client", role, scopes, time.Minute, "ws")
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func wsDial(t *testing.T, addr, origin string) *wsTestConn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	key := wsKey(t)
	fmt.Fprintf(conn, "GET /gateway HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", addr, key)
	if origin != "" {
		fmt.Fprintf(conn, "Origin: %s\r\n", origin)
	}
	fmt.Fprint(conn, "\r\n")
	resp, err := http.ReadResponse(r, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		t.Fatalf("websocket handshake status = %d", resp.StatusCode)
	}
	return &wsTestConn{conn: conn, r: r}
}

func wsHandshakeStatus(t *testing.T, addr, origin string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	fmt.Fprintf(conn, "GET /gateway HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n", addr, wsKey(t))
	if origin != "" {
		fmt.Fprintf(conn, "Origin: %s\r\n", origin)
	}
	fmt.Fprint(conn, "\r\n")
	resp, err := http.ReadResponse(r, &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func (c *wsTestConn) writeText(payload []byte) error {
	mask := [4]byte{0xCA, 0xFE, 0xBA, 0xBE}
	header := []byte{0x81}
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, 0x80|byte(n))
	case n <= 0xFFFF:
		header = append(header, 0x80|126, byte(n>>8), byte(n))
	default:
		header = append(header, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		header = append(header, ext[:]...)
	}
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

func (c *wsTestConn) readText() ([]byte, error) {
	for {
		opcode, payload, err := readServerFrame(c.r)
		if err != nil {
			return nil, err
		}
		if opcode == 0x1 {
			return payload, nil
		}
		if opcode == 0x8 {
			return nil, io.EOF
		}
	}
}

func readServerFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode := hdr[0] & 0x0F
	size := uint64(hdr[1] & 0x7F)
	switch size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(ext[:])
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

func freeTCPAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func waitTCP(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("tcp listener did not appear: %s", addr)
}

func wsKey(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

func rawID(t *testing.T, id int64) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
