package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

const (
	gatewayDialTimeout = 10 * time.Second
	gatewayCallTimeout = 15 * time.Second
	maxGatewayFrame    = 16 << 20
)

var errGatewayClosed = errors.New("gateway connection closed")

type gatewayDialOptions struct {
	dialer    *net.Dialer
	tlsConfig *tls.Config
}

type gatewayClient struct {
	conn   net.Conn
	reader *bufio.Reader

	writeMu sync.Mutex

	pendingMu sync.Mutex
	nextID    int64
	pending   map[int64]chan gatewayCallResult
	closed    bool
	closeErr  error
	done      chan struct{}
	closeOnce sync.Once
}

type gatewayCallResult struct {
	result json.RawMessage
	err    error
}

type gatewayWireResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpc.Error      `json:"error"`
}

func dialGateway(rawURL, token string) (*gatewayClient, error) {
	return dialGatewayWithOptions(rawURL, token, gatewayDialOptions{})
}

func dialGatewayWithOptions(rawURL, token string, opts gatewayDialOptions) (*gatewayClient, error) {
	if err := validateGatewayURL(rawURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("gateway token is required")
	}
	u, _ := url.Parse(rawURL)
	dialer := opts.dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: gatewayDialTimeout}
	}
	address := u.Host
	if _, _, err := net.SplitHostPort(address); err != nil {
		if u.Scheme == "wss" {
			address = net.JoinHostPort(u.Hostname(), "443")
		} else {
			address = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayDialTimeout)
	defer cancel()

	var conn net.Conn
	var err error
	if u.Scheme == "wss" {
		config := opts.tlsConfig
		if config == nil {
			config = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			config = config.Clone()
			if config.MinVersion == 0 {
				config.MinVersion = tls.VersionTLS12
			}
		}
		if config.ServerName == "" {
			config.ServerName = u.Hostname()
		}
		tlsDialer := &tls.Dialer{NetDialer: dialer, Config: config}
		conn, err = tlsDialer.DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, fmt.Errorf("gateway dial: %w", err)
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = conn.Close()
		}
	}()
	_ = conn.SetDeadline(time.Now().Add(gatewayDialTimeout))
	reader := bufio.NewReader(conn)
	if err := performWebSocketUpgrade(conn, reader, u); err != nil {
		return nil, err
	}
	client := &gatewayClient{
		conn: conn, reader: reader, nextID: 1,
		pending: make(map[int64]chan gatewayCallResult), done: make(chan struct{}),
	}
	if err := client.performHello(token); err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	succeeded = true
	go client.readLoop()
	return client, nil
}

func performWebSocketUpgrade(conn net.Conn, reader *bufio.Reader, u *url.URL) error {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("gateway websocket key: %w", err)
	}
	key := base64.StdEncoding.EncodeToString(nonce[:])
	path := u.EscapedPath()
	if path == "" {
		path = "/gateway"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	req, err := http.NewRequest(http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("gateway websocket request: %w", err)
	}
	req.Host = u.Host
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	if err := req.Write(conn); err != nil {
		return fmt.Errorf("gateway websocket upgrade write: %w", err)
	}
	response, err := http.ReadResponse(reader, req)
	if err != nil {
		return fmt.Errorf("gateway websocket upgrade response: %w", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		_ = response.Body.Close()
		return fmt.Errorf("gateway websocket upgrade returned HTTP %d", response.StatusCode)
	}
	if !strings.EqualFold(strings.TrimSpace(response.Header.Get("Upgrade")), "websocket") ||
		!headerContainsToken(response.Header.Get("Connection"), "upgrade") {
		return fmt.Errorf("gateway websocket upgrade response is missing required headers")
	}
	wantAccept := websocketClientAccept(key)
	if response.Header.Get("Sec-WebSocket-Accept") != wantAccept {
		return fmt.Errorf("gateway websocket upgrade returned an invalid accept key")
	}
	return nil
}

func (c *gatewayClient) performHello(token string) error {
	params := rpc.HelloRequest{
		ProtocolVersion: rpc.GatewayProtocolVersion,
		ClientID:        "carina-worker",
		Role:            rpc.RoleWorker,
		Scopes:          []rpc.Scope{rpc.ScopeWorker, rpc.ScopeRead, rpc.ScopeStream},
		Token:           token,
		Capabilities:    []string{"lease_executor"},
		UserAgent:       "carina-worker",
	}
	rawParams, _ := json.Marshal(params)
	id, _ := json.Marshal(int64(1))
	req, _ := json.Marshal(rpc.Request{JSONRPC: "2.0", ID: id, Method: "gateway.hello", Params: rawParams})
	if err := c.writeText(req); err != nil {
		return fmt.Errorf("gateway hello write: %w", err)
	}
	for {
		opcode, payload, err := c.readFrame()
		if err != nil {
			return fmt.Errorf("gateway hello read: %w", err)
		}
		switch opcode {
		case 0x9:
			if err := c.writeControl(0xA, payload); err != nil {
				return err
			}
			continue
		case 0x8:
			return fmt.Errorf("gateway closed during hello")
		case 0x1:
		default:
			continue
		}
		var response gatewayWireResponse
		if err := json.Unmarshal(payload, &response); err != nil {
			return fmt.Errorf("gateway hello decode: %w", err)
		}
		if response.Error != nil {
			return fmt.Errorf("gateway hello: %w", response.Error)
		}
		var responseID int64
		if err := json.Unmarshal(response.ID, &responseID); err != nil || responseID != 1 {
			return fmt.Errorf("gateway hello returned an unexpected response id")
		}
		var hello rpc.HelloResponse
		if err := json.Unmarshal(response.Result, &hello); err != nil {
			return fmt.Errorf("gateway hello result: %w", err)
		}
		if hello.Role != rpc.RoleWorker || !hasGatewayScopes(hello.Scopes, rpc.ScopeWorker, rpc.ScopeRead, rpc.ScopeStream) {
			return fmt.Errorf("gateway hello did not grant worker/read/stream scopes")
		}
		return nil
	}
}

func (c *gatewayClient) Call(method string, params any, result any) error {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("gateway marshal params: %w", err)
	}
	c.pendingMu.Lock()
	if c.closed {
		err := c.closeErr
		c.pendingMu.Unlock()
		if err == nil {
			err = errGatewayClosed
		}
		return err
	}
	c.nextID++
	id := c.nextID
	responseCh := make(chan gatewayCallResult, 1)
	c.pending[id] = responseCh
	c.pendingMu.Unlock()

	rawID, _ := json.Marshal(id)
	request, err := json.Marshal(rpc.Request{JSONRPC: "2.0", ID: rawID, Method: method, Params: rawParams})
	if err != nil {
		c.removePending(id)
		return fmt.Errorf("gateway marshal request: %w", err)
	}
	if err := c.writeText(request); err != nil {
		c.shutdown(fmt.Errorf("gateway write: %w", err))
	}
	timer := time.NewTimer(gatewayCallTimeout)
	defer timer.Stop()
	select {
	case response := <-responseCh:
		if response.err != nil {
			return response.err
		}
		if result != nil && len(response.result) > 0 && string(response.result) != "null" {
			if err := json.Unmarshal(response.result, result); err != nil {
				return fmt.Errorf("gateway decode result: %w", err)
			}
		}
		return nil
	case <-timer.C:
		c.removePending(id)
		return fmt.Errorf("gateway call %s timed out", method)
	}
}

func (c *gatewayClient) Close() error {
	_ = c.writeControl(0x8, nil)
	c.shutdown(errGatewayClosed)
	return nil
}

func (c *gatewayClient) readLoop() {
	for {
		opcode, payload, err := c.readFrame()
		if err != nil {
			c.shutdown(fmt.Errorf("gateway read: %w", err))
			return
		}
		switch opcode {
		case 0x8:
			c.shutdown(errGatewayClosed)
			return
		case 0x9:
			if err := c.writeControl(0xA, payload); err != nil {
				c.shutdown(err)
				return
			}
			continue
		case 0xA:
			continue
		case 0x1:
		default:
			c.shutdown(fmt.Errorf("gateway unsupported websocket opcode %d", opcode))
			return
		}
		var response gatewayWireResponse
		if err := json.Unmarshal(payload, &response); err != nil {
			c.shutdown(fmt.Errorf("gateway response decode: %w", err))
			return
		}
		if len(response.ID) == 0 || string(response.ID) == "null" {
			continue // notification; lease workers currently do not subscribe.
		}
		var id int64
		if err := json.Unmarshal(response.ID, &id); err != nil {
			continue
		}
		c.pendingMu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.pendingMu.Unlock()
		if ch == nil {
			continue
		}
		if response.Error != nil {
			ch <- gatewayCallResult{err: response.Error}
		} else {
			ch <- gatewayCallResult{result: response.Result}
		}
	}
}

func (c *gatewayClient) shutdown(err error) {
	c.closeOnce.Do(func() {
		c.pendingMu.Lock()
		c.closed = true
		c.closeErr = err
		pending := c.pending
		c.pending = make(map[int64]chan gatewayCallResult)
		close(c.done)
		c.pendingMu.Unlock()
		_ = c.conn.Close()
		for _, ch := range pending {
			ch <- gatewayCallResult{err: err}
		}
	})
}

func (c *gatewayClient) removePending(id int64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}

func (c *gatewayClient) writeText(payload []byte) error {
	return c.writeFrame(0x1, payload)
}

func (c *gatewayClient) writeControl(opcode byte, payload []byte) error {
	if len(payload) > 125 {
		return fmt.Errorf("gateway websocket control frame too large")
	}
	return c.writeFrame(opcode, payload)
}

func (c *gatewayClient) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	header := []byte{0x80 | opcode}
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
	masked := make([]byte, n)
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

func (c *gatewayClient) readFrame() (byte, []byte, error) {
	var header [2]byte
	if _, err := io.ReadFull(c.reader, header[:]); err != nil {
		return 0, nil, err
	}
	if header[0]&0x80 == 0 {
		return 0, nil, fmt.Errorf("fragmented websocket frames are unsupported")
	}
	if header[0]&0x70 != 0 {
		return 0, nil, fmt.Errorf("gateway websocket extensions are unsupported")
	}
	opcode := header[0] & 0x0F
	if header[1]&0x80 != 0 {
		return 0, nil, fmt.Errorf("gateway sent a masked websocket frame")
	}
	size := uint64(header[1] & 0x7F)
	switch size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.reader, ext[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(ext[:])
	}
	if size > maxGatewayFrame {
		return 0, nil, fmt.Errorf("gateway websocket frame exceeded 16 MiB")
	}
	if opcode >= 0x8 && size > 125 {
		return 0, nil, fmt.Errorf("gateway websocket control frame exceeded 125 bytes")
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}

func websocketClientAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func headerContainsToken(value, token string) bool {
	for _, part := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func hasGatewayScopes(got []rpc.Scope, required ...rpc.Scope) bool {
	seen := make(map[rpc.Scope]bool, len(got))
	for _, scope := range got {
		seen[scope] = true
	}
	for _, scope := range required {
		if !seen[scope] {
			return false
		}
	}
	return true
}
