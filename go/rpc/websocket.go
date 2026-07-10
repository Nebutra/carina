package rpc

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const defaultGatewayWebSocketPath = "/gateway"

// GatewayTokenVerifier verifies signed, scoped Gateway capability tokens.
type GatewayTokenVerifier interface {
	Verify(token, transport string) (GatewayTokenClaims, error)
}

type WebSocketOptions struct {
	Path           string
	AllowedOrigins []string
	TokenVerifier  GatewayTokenVerifier
}

// ListenWebSocket serves the JSON-RPC control plane over WebSocket text frames.
// It is a Gateway skeleton: callers get the same descriptor/origin policy as
// TCP remote callers, and no new authority is created by the transport.
func (s *Server) ListenWebSocket(addr, path string, allowedOrigins []string) error {
	return s.ListenWebSocketWithOptions(addr, WebSocketOptions{Path: path, AllowedOrigins: allowedOrigins})
}

func (s *Server) ListenWebSocketWithOptions(addr string, opts WebSocketOptions) error {
	if strings.TrimSpace(opts.Path) == "" {
		opts.Path = defaultGatewayWebSocketPath
	}
	if opts.TokenVerifier == nil {
		return fmt.Errorf("rpc: websocket gateway requires a token verifier")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("rpc: listen websocket %s: %w", addr, err)
	}
	s.mu.Lock()
	s.listeners = append(s.listeners, ln)
	s.mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc(opts.Path, func(w http.ResponseWriter, r *http.Request) {
		s.handleWebSocketUpgrade(w, r, opts)
	})
	err = (&http.Server{Handler: mux}).Serve(ln)
	if err == nil || strings.Contains(err.Error(), "use of closed network connection") {
		return nil
	}
	return err
}

func (s *Server) handleWebSocketUpgrade(w http.ResponseWriter, r *http.Request, opts WebSocketOptions) {
	if opts.TokenVerifier == nil {
		http.Error(w, "websocket gateway authentication unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" && !webSocketOriginAllowed(origin, opts.AllowedOrigins) {
		http.Error(w, "websocket origin not allowed", http.StatusForbidden)
		return
	}
	if !headerHasToken(r.Header, "Connection", "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" || r.Header.Get("Sec-WebSocket-Version") != "13" {
		http.Error(w, "unsupported websocket handshake", http.StatusBadRequest)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijack unavailable", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	accept := websocketAccept(key)
	_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\n")
	_, _ = fmt.Fprintf(rw, "Upgrade: websocket\r\n")
	_, _ = fmt.Fprintf(rw, "Connection: Upgrade\r\n")
	_, _ = fmt.Fprintf(rw, "Sec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}
	ws := newWebSocketConn(conn, rw.Reader)
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	scopes, err := s.requireWebSocketHello(ws, opts.TokenVerifier)
	if err != nil {
		_ = ws.Close()
		return
	}
	_ = ws.SetReadDeadline(time.Time{})
	s.serveWithScopes(ws, OriginRemote, scopes)
}

func (s *Server) requireWebSocketHello(conn net.Conn, verifier GatewayTokenVerifier) ([]Scope, error) {
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(conn)
	if !scanner.Scan() {
		return nil, fmt.Errorf("websocket: gateway.hello required")
	}
	var req Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeParseError, Message: err.Error()}})
		return nil, err
	}
	if req.Method != "gateway.hello" {
		_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeInvalidRequest, Message: "gateway.hello required before method dispatch"}})
		return nil, fmt.Errorf("websocket: first method was %s", req.Method)
	}
	if ok, reason := s.remoteAuthorized(req.Method, OriginRemote); !ok {
		_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeMethodNotFound, Message: reason}})
		return nil, fmt.Errorf("websocket: %s", reason)
	}
	var hello HelloRequest
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &hello); err != nil {
			_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeParseError, Message: err.Error()}})
			return nil, err
		}
	}
	if verifier == nil {
		err := fmt.Errorf("gateway token verifier unavailable")
		_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeInvalidRequest, Message: err.Error()}})
		return nil, err
	}
	if strings.TrimSpace(hello.Token) == "" {
		err := fmt.Errorf("gateway token required")
		_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeInvalidRequest, Message: err.Error()}})
		return nil, err
	}
	claims, err := verifier.Verify(hello.Token, "ws")
	if err != nil {
		_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeInvalidRequest, Message: "gateway token invalid"}})
		return nil, err
	}
	if hello.Role != "" && hello.Role != claims.Role {
		err := fmt.Errorf("gateway token role mismatch")
		_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeInvalidRequest, Message: err.Error()}})
		return nil, err
	}
	scopes, err := IntersectScopes(claims.Scopes, hello.Scopes)
	if err != nil {
		_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeInvalidRequest, Message: err.Error()}})
		return nil, err
	}
	hello.Role = claims.Role
	hello.Scopes = scopes
	req.Params, _ = json.Marshal(hello)
	resp := s.dispatch(req)
	if result, ok := resp.Result.(HelloResponse); ok {
		result.Role = hello.Role
		result.Scopes = scopes
		if result.Auth == nil {
			result.Auth = map[string]any{}
		}
		result.Auth["grant_type"] = "gateway_token"
		result.Auth["transport"] = "ws"
		resp.Result = result
	}
	_ = enc.Encode(resp)
	if resp.Error != nil {
		return nil, resp.Error
	}
	return scopes, nil
}

func headerHasToken(h http.Header, key, token string) bool {
	for _, value := range h.Values(key) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func webSocketOriginAllowed(origin string, allowed []string) bool {
	for _, candidate := range allowed {
		candidate = strings.TrimSpace(candidate)
		if strings.EqualFold(origin, candidate) {
			return true
		}
	}
	return false
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

type webSocketConn struct {
	net.Conn
	r        *bufio.Reader
	readBuf  []byte
	writeMu  sync.Mutex
	writeBuf []byte
}

func newWebSocketConn(conn net.Conn, r *bufio.Reader) *webSocketConn {
	if r == nil {
		r = bufio.NewReader(conn)
	}
	return &webSocketConn{Conn: conn, r: r}
}

func (c *webSocketConn) Read(p []byte) (int, error) {
	for len(c.readBuf) == 0 {
		opcode, payload, err := c.readFrame()
		if err != nil {
			return 0, err
		}
		switch opcode {
		case 0x1: // text
			c.readBuf = append(payload, '\n')
		case 0x8: // close
			_ = c.writeFrame(0x8, nil)
			return 0, io.EOF
		case 0x9: // ping
			_ = c.writeFrame(0xA, payload)
		case 0xA: // pong
		default:
			return 0, fmt.Errorf("websocket: unsupported opcode %d", opcode)
		}
	}
	n := copy(p, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func (c *webSocketConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	total := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			c.writeBuf = append(c.writeBuf, p...)
			return total, nil
		}
		c.writeBuf = append(c.writeBuf, p[:i]...)
		if err := c.writeFrameLocked(0x1, c.writeBuf); err != nil {
			return 0, err
		}
		c.writeBuf = c.writeBuf[:0]
		p = p[i+1:]
	}
	return total, nil
}

func (c *webSocketConn) Close() error {
	_ = c.writeFrame(0x8, nil)
	return c.Conn.Close()
}

func (c *webSocketConn) SetDeadline(t time.Time) error {
	return c.Conn.SetDeadline(t)
}

func (c *webSocketConn) SetReadDeadline(t time.Time) error {
	return c.Conn.SetReadDeadline(t)
}

func (c *webSocketConn) SetWriteDeadline(t time.Time) error {
	return c.Conn.SetWriteDeadline(t)
}

func (c *webSocketConn) readFrame() (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(c.r, hdr[:]); err != nil {
		return 0, nil, err
	}
	if hdr[0]&0x80 == 0 {
		return 0, nil, fmt.Errorf("websocket: fragmented frames are not supported")
	}
	opcode := hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	if !masked {
		return 0, nil, fmt.Errorf("websocket: client frames must be masked")
	}
	size := uint64(hdr[1] & 0x7F)
	switch size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(c.r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(ext[:])
	}
	if size > 16*1024*1024 {
		return 0, nil, fmt.Errorf("websocket: frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func (c *webSocketConn) writeFrame(opcode byte, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.writeFrameLocked(opcode, payload)
}

func (c *webSocketConn) writeFrameLocked(opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, byte(n))
	case n <= 0xFFFF:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		header = append(header, 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		header = append(header, ext[:]...)
	}
	if _, err := c.Conn.Write(header); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	_, err := c.Conn.Write(payload)
	return err
}
