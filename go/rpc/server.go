// Package rpc implements the Carina JSON-RPC 2.0 transport. Framing is
// newline-delimited JSON over a unix socket or TCP (docs/rpc-api.md).
// Beyond request/response it supports server-initiated notifications, used
// for streaming session events to subscribers.
package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
)

const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInternalError  = -32603
)

// Origin identifies which transport a request arrived on. Local (unix socket)
// is trusted; Remote (TCP) is restricted to an explicit read/observe allowlist
// and can be cut off entirely with the kill-switch.
type Origin int

const (
	OriginLocal Origin = iota
	OriginRemote
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Handler processes a single method call.
type Handler func(params json.RawMessage) (any, error)

// StreamHandler attaches a long-lived subscription to a connection.
type StreamHandler func(params json.RawMessage, sub *Subscription) error

// Subscription pushes server notifications to one connection.
type Subscription struct {
	mu   sync.Mutex
	enc  *json.Encoder
	done chan struct{}
}

// Notify sends a server notification (no id) to the subscriber.
func (s *Subscription) Notify(method string, params any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return fmt.Errorf("subscription closed")
	default:
	}
	return s.enc.Encode(Response{JSONRPC: "2.0", Result: nil, Error: nil, ID: nil}.withNotify(method, params))
}

// Done reports when the subscriber disconnected.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// notification is encoded instead of Response for server-initiated messages.
type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

func (Response) withNotify(method string, params any) notification {
	return notification{JSONRPC: "2.0", Method: method, Params: params}
}

type Server struct {
	mu             sync.RWMutex
	handlers       map[string]Handler
	streams        map[string]StreamHandler
	descriptors    map[string]MethodDescriptor
	listeners      []net.Listener
	remoteSafe     map[string]bool // methods a Remote origin may call
	remoteDisabled bool            // kill-switch: refuse all Remote calls
	strictMethods  bool            // refuse registered handlers without descriptors
}

func NewServer() *Server {
	return &Server{
		handlers:    make(map[string]Handler),
		streams:     make(map[string]StreamHandler),
		descriptors: make(map[string]MethodDescriptor),
		remoteSafe:  make(map[string]bool),
	}
}

// MarkRemoteSafe allowlists a method for the Remote (TCP) transport. Anything
// not marked is local-only (mutating/side-effecting methods stay off remote).
func (s *Server) MarkRemoteSafe(methods ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range methods {
		s.remoteSafe[m] = true
	}
}

// SetRemoteDisabled toggles the remote kill-switch: when on, every Remote call
// is refused regardless of the allowlist.
func (s *Server) SetRemoteDisabled(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.remoteDisabled = on
}

// RequireDescriptors makes the server fail closed for registered methods that
// lack a MethodDescriptor. Daemon control planes should enable this after
// registering their catalog; small tests can keep the legacy Register behavior.
func (s *Server) RequireDescriptors(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.strictMethods = on
}

// remoteAuthorized reports whether a method may run for the given origin.
func (s *Server) remoteAuthorized(method string, origin Origin) (bool, string) {
	if origin == OriginLocal {
		return true, ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.remoteDisabled {
		return false, "remote access is disabled (kill-switch)"
	}
	if desc, ok := s.descriptors[method]; ok {
		if desc.Remote {
			return true, ""
		}
		return false, "method not available over remote transport: " + method
	}
	if !s.remoteSafe[method] {
		return false, "method not available over remote transport: " + method
	}
	return true, ""
}

func (s *Server) Register(method string, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

func (s *Server) RegisterStream(method string, h StreamHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streams[method] = h
}

func (s *Server) RegisterMethod(desc MethodDescriptor, h Handler) error {
	normalized, err := desc.normalized(false)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.descriptors[normalized.Method]; ok {
		return fmt.Errorf("rpc method already registered: %s", normalized.Method)
	}
	if _, ok := s.handlers[normalized.Method]; ok {
		return fmt.Errorf("rpc method already registered: %s", normalized.Method)
	}
	if _, ok := s.streams[normalized.Method]; ok {
		return fmt.Errorf("rpc method already registered: %s", normalized.Method)
	}
	s.handlers[normalized.Method] = h
	s.descriptors[normalized.Method] = normalized
	if normalized.Remote {
		s.remoteSafe[normalized.Method] = true
	}
	return nil
}

func (s *Server) RegisterStreamMethod(desc MethodDescriptor, h StreamHandler) error {
	normalized, err := desc.normalized(true)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.descriptors[normalized.Method]; ok {
		return fmt.Errorf("rpc method already registered: %s", normalized.Method)
	}
	if _, ok := s.handlers[normalized.Method]; ok {
		return fmt.Errorf("rpc method already registered: %s", normalized.Method)
	}
	if _, ok := s.streams[normalized.Method]; ok {
		return fmt.Errorf("rpc method already registered: %s", normalized.Method)
	}
	s.streams[normalized.Method] = h
	s.descriptors[normalized.Method] = normalized
	if normalized.Remote {
		s.remoteSafe[normalized.Method] = true
	}
	return nil
}

func (s *Server) ListenUnix(socketPath string) error {
	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("rpc: listen %s: %w", socketPath, err)
	}
	return s.accept(ln, OriginLocal)
}

func (s *Server) ListenTCP(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("rpc: listen tcp %s: %w", addr, err)
	}
	return s.accept(ln, OriginRemote)
}

func (s *Server) accept(ln net.Listener, origin Origin) error {
	s.mu.Lock()
	s.listeners = append(s.listeners, ln)
	s.mu.Unlock()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil // listener closed
		}
		go s.serve(conn, origin)
	}
}

func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ln := range s.listeners {
		_ = ln.Close()
	}
	return nil
}

func (s *Server) serve(conn net.Conn, origin Origin) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(conn)
	done := make(chan struct{})
	defer close(done)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(Response{JSONRPC: "2.0", Error: &Error{Code: CodeParseError, Message: err.Error()}})
			continue
		}

		// Enforce transport-origin restriction before doing any work.
		if ok, reason := s.remoteAuthorized(req.Method, origin); !ok {
			_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeMethodNotFound, Message: reason}})
			continue
		}

		// Stream methods keep the connection open and push notifications.
		s.mu.RLock()
		streamHandler, isStream := s.streams[req.Method]
		_, classified := s.descriptors[req.Method]
		strict := s.strictMethods
		s.mu.RUnlock()
		if isStream {
			if strict && !classified {
				_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeMethodNotFound, Message: "method not classified: " + req.Method}})
				continue
			}
			sub := &Subscription{enc: enc, done: done}
			if err := streamHandler(req.Params, sub); err != nil {
				_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeInternalError, Message: err.Error()}})
				continue
			}
			_ = enc.Encode(Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"subscribed": true}})
			continue
		}

		_ = enc.Encode(s.dispatch(req))
	}
}

func (s *Server) dispatch(req Request) Response {
	resp := Response{JSONRPC: "2.0", ID: req.ID}
	s.mu.RLock()
	h, ok := s.handlers[req.Method]
	_, classified := s.descriptors[req.Method]
	strict := s.strictMethods
	s.mu.RUnlock()
	if !ok {
		resp.Error = &Error{Code: CodeMethodNotFound, Message: "method not found: " + req.Method}
		return resp
	}
	if strict && !classified {
		resp.Error = &Error{Code: CodeMethodNotFound, Message: "method not classified: " + req.Method}
		return resp
	}
	result, err := h(req.Params)
	if err != nil {
		resp.Error = &Error{Code: CodeInternalError, Message: err.Error()}
		return resp
	}
	resp.Result = result
	return resp
}
