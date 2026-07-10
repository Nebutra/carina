package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func TestGatewayClientWorkerHelloAndConcurrentCalls(t *testing.T) {
	server, rawURL, token := startWorkerGateway(t, func(params json.RawMessage) (any, error) {
		var request struct {
			Value int `json:"value"`
		}
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		return map[string]int{"value": request.Value}, nil
	})
	defer server.Close()
	client, err := dialGateway(rawURL, token)
	if err != nil {
		t.Fatalf("dialGateway: %v", err)
	}
	defer client.Close()

	const calls = 24
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			var response struct {
				Value int `json:"value"`
			}
			if err := client.Call("worker.echo", map[string]int{"value": i}, &response); err != nil {
				errs <- err
				return
			}
			if response.Value != i {
				errs <- fmt.Errorf("response value = %d, want %d", response.Value, i)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestGatewayClientDisconnectFailsAllPendingCalls(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	server, rawURL, token := startWorkerGateway(t, func(json.RawMessage) (any, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		return map[string]bool{"ok": true}, nil
	})
	defer server.Close()
	client, err := dialGateway(rawURL, token)
	if err != nil {
		t.Fatalf("dialGateway: %v", err)
	}
	defer client.Close()

	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- client.Call("worker.echo", map[string]any{}, nil) }()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("gateway did not receive pending call")
	}
	_ = client.conn.Close()
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if err == nil || !strings.Contains(err.Error(), "gateway read") {
				t.Fatalf("pending call error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("pending call was not failed after disconnect")
		}
	}
	close(release)
}

func TestGatewayClientWSSConfigurationAndHello(t *testing.T) {
	helloSeen := make(chan rpc.HelloRequest, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Error("TLS response writer does not support hijacking")
			return
		}
		conn, rw, err := hijacker.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		key := r.Header.Get("Sec-WebSocket-Key")
		fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", websocketClientAccept(key))
		if err := rw.Flush(); err != nil {
			t.Errorf("flush upgrade: %v", err)
			return
		}
		opcode, payload, err := readMaskedClientFrame(rw.Reader)
		if err != nil || opcode != 0x1 {
			t.Errorf("read hello: opcode=%d err=%v", opcode, err)
			return
		}
		var request rpc.Request
		if err := json.Unmarshal(payload, &request); err != nil {
			t.Errorf("decode hello request: %v", err)
			return
		}
		var hello rpc.HelloRequest
		if err := json.Unmarshal(request.Params, &hello); err != nil {
			t.Errorf("decode hello params: %v", err)
			return
		}
		helloSeen <- hello
		response := rpc.Response{JSONRPC: "2.0", ID: request.ID, Result: rpc.HelloResponse{
			Version: "1", ProtocolVersion: rpc.GatewayProtocolVersion, ServerVersion: "test",
			Role: rpc.RoleWorker, Scopes: []rpc.Scope{rpc.ScopeRead, rpc.ScopeWorker, rpc.ScopeStream},
		}}
		raw, _ := json.Marshal(response)
		if err := writeUnmaskedServerFrame(conn, 0x1, raw); err != nil {
			t.Errorf("write hello: %v", err)
			return
		}
		// Keep the upgraded connection alive until the client closes it.
		_, _, _ = readMaskedClientFrame(rw.Reader)
	})
	server := httptest.NewUnstartedServer(handler)
	server.EnableHTTP2 = false
	server.StartTLS()
	defer server.Close()
	tlsConfig := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	rawURL := "wss" + strings.TrimPrefix(server.URL, "https") + "/gateway"
	client, err := dialGatewayWithOptions(rawURL, "secret-token", gatewayDialOptions{tlsConfig: tlsConfig})
	if err != nil {
		t.Fatalf("dial wss gateway: %v", err)
	}
	hello := <-helloSeen
	if hello.Token != "secret-token" || hello.Role != rpc.RoleWorker ||
		!hasGatewayScopes(hello.Scopes, rpc.ScopeWorker, rpc.ScopeRead, rpc.ScopeStream) {
		t.Fatalf("hello = %+v", hello)
	}
	_ = client.Close()
}

func startWorkerGateway(t *testing.T, echo rpc.Handler) (*rpc.Server, string, string) {
	t.Helper()
	server := rpc.NewServer()
	if err := server.RegisterMethod(rpc.MethodDescriptor{Method: "gateway.hello", Scope: rpc.ScopeRead, Remote: true}, func(params json.RawMessage) (any, error) {
		var request rpc.HelloRequest
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		return rpc.BuildHelloResponse(request, "test", server.MethodDescriptors())
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.RegisterMethod(rpc.MethodDescriptor{Method: "worker.echo", Scope: rpc.ScopeWorker, Remote: true}, echo); err != nil {
		t.Fatal(err)
	}
	issuer, err := rpc.NewGatewayTokenIssuer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := issuer.Issue("worker-test", rpc.RoleWorker, []rpc.Scope{rpc.ScopeRead, rpc.ScopeWorker, rpc.ScopeStream}, time.Minute, "ws")
	if err != nil {
		t.Fatal(err)
	}
	address := freeWorkerAddress(t)
	go func() {
		_ = server.ListenWebSocketWithOptions(address, rpc.WebSocketOptions{Path: "/gateway", TokenVerifier: issuer})
	}()
	waitWorkerAddress(t, address)
	return server, "ws://" + address + "/gateway", token
}

func freeWorkerAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func waitWorkerAddress(t *testing.T, address string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 20*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("gateway did not listen on %s", address)
}

func readMaskedClientFrame(reader *bufio.Reader) (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, nil, err
	}
	opcode := header[0] & 0x0F
	size := uint64(header[1] & 0x7F)
	switch size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(reader, ext[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(ext[0])<<8 | uint64(ext[1])
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(reader, ext[:]); err != nil {
			return 0, nil, err
		}
		for _, b := range ext {
			size = size<<8 | uint64(b)
		}
	}
	if header[1]&0x80 == 0 {
		return 0, nil, fmt.Errorf("client frame was not masked")
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return 0, nil, err
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return opcode, payload, nil
}

func writeUnmaskedServerFrame(conn net.Conn, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	if len(payload) < 126 {
		header = append(header, byte(len(payload)))
	} else {
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}
