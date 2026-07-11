// Package rpc implements the Carina JSON-RPC 2.0 transport. Framing is
// newline-delimited JSON over a unix socket or TCP (docs/rpc-api.md).
// Beyond request/response it supports server-initiated notifications, used
// for streaming session events to subscribers.
package rpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/unix"
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
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// Handler processes a single method call.
type Handler func(params json.RawMessage) (any, error)

// ScopeResolver can narrow or raise a method's required scope from request
// params. It is used for mixed-risk methods such as patch proposals.
type ScopeResolver func(params json.RawMessage) (Scope, error)

// StreamHandler attaches a long-lived subscription to a connection.
type StreamHandler func(params json.RawMessage, sub *Subscription) error

// Subscription pushes server notifications to one connection.
type Subscription struct {
	id     string
	w      *connWriter
	done   chan struct{}
	close  func() error
	result any
}

var subscriptionSequence atomic.Uint64

func nextSubscriptionID() string { return fmt.Sprintf("sub_%d", subscriptionSequence.Add(1)) }

func (s *Subscription) ID() string { return s.id }

// SetResult lets a stream handler return catch-up metadata in the subscribe
// response without changing the handler signature.
func (s *Subscription) SetResult(result any) { s.result = result }

// Notify sends a server notification (no id) to the subscriber. The frame is
// enqueued on the connection's single-writer drain loop (connWriter), so it
// preserves wire order relative to any other frame — request response or
// notification — enqueued for the same connection, regardless of which
// goroutine calls Notify or when its own encode happens to complete. See
// P1.8 (docs/plans/agent-cli-productization.md): an approval-resolution
// event enqueued before a later request's response must never be observed
// on the wire after it.
func (s *Subscription) Notify(method string, params any) error {
	select {
	case <-s.done:
		return fmt.Errorf("subscription closed")
	default:
	}
	return s.w.enqueue(Response{JSONRPC: "2.0", Result: nil, Error: nil, ID: nil}.withNotify(method, params))
}

var ErrSlowConsumer = fmt.Errorf("rpc: slow consumer queue full")

// TryNotify never blocks a publisher. A full connection queue is surfaced as
// ErrSlowConsumer so the owner can disconnect that subscriber and require a
// cursor-based catch-up on reconnect.
func (s *Subscription) TryNotify(method string, params any) error {
	select {
	case <-s.done:
		return fmt.Errorf("subscription closed")
	default:
	}
	return s.w.tryEnqueue(Response{JSONRPC: "2.0"}.withNotify(method, params))
}

func (s *Subscription) Disconnect() error {
	if s.close == nil {
		return nil
	}
	return s.close()
}

// Done reports when the subscriber disconnected.
func (s *Subscription) Done() <-chan struct{} { return s.done }

// connWriter is the single writer goroutine for one connection: every frame
// destined for the wire — request responses from serveWithScopes and
// notifications from any Subscription tied to this connection — is enqueued
// here and encoded strictly in enqueue order by one goroutine. This is the
// P1.8 single-writer-drain hardener: without it, serveWithScopes's own
// enc.Encode(response) call and a concurrent Subscription.Notify call race
// on the same underlying net.Conn with no coordination between them, so
// wire order can diverge from enqueue order — misrepresenting event
// ordering to a watching client (approval events overtaking the tool call
// they govern).
type connWriter struct {
	enc     *json.Encoder
	queue   chan any
	done    chan struct{}
	stopped chan struct{}
}

func newConnWriter(enc *json.Encoder, done chan struct{}) *connWriter {
	w := &connWriter{enc: enc, queue: make(chan any, 256), done: done, stopped: make(chan struct{})}
	go w.drain()
	return w
}

// drain is the single writer goroutine: it encodes queued frames one at a
// time, in the order they were enqueued, until the connection is done.
func (w *connWriter) drain() {
	defer close(w.stopped)
	for {
		select {
		case frame := <-w.queue:
			_ = w.enc.Encode(frame)
		case <-w.done:
			return
		}
	}
}

// enqueue hands a frame to the single writer goroutine and blocks until it
// has been accepted onto the queue (not until it has been written), so
// callers observe a bounded, ordered handoff rather than an unbounded
// buffer racing writer fairness.
func (w *connWriter) enqueue(frame any) error {
	select {
	case w.queue <- frame:
		return nil
	case <-w.done:
		return fmt.Errorf("connection closed")
	}
}

func (w *connWriter) tryEnqueue(frame any) error {
	select {
	case <-w.done:
		return fmt.Errorf("connection closed")
	default:
	}
	select {
	case w.queue <- frame:
		return nil
	case <-w.done:
		return fmt.Errorf("connection closed")
	default:
		return ErrSlowConsumer
	}
}

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
	scopeResolvers map[string]ScopeResolver
	listeners      []net.Listener
	remoteSafe     map[string]bool // methods a Remote origin may call
	remoteDisabled bool            // kill-switch: refuse all Remote calls
	strictMethods  bool            // refuse registered handlers without descriptors
	lockFile       *os.File        // ListenUnix's cross-process advisory lock (P1.8), nil until acquired
}

func NewServer() *Server {
	return &Server{
		handlers:       make(map[string]Handler),
		streams:        make(map[string]StreamHandler),
		descriptors:    make(map[string]MethodDescriptor),
		scopeResolvers: make(map[string]ScopeResolver),
		remoteSafe:     make(map[string]bool),
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
	return s.RegisterMethodDynamic(desc, h, nil)
}

// RegisterMethodDynamic registers a method with an optional param-sensitive
// scope resolver. The descriptor's static scope remains the fallback and
// advertised baseline.
func (s *Server) RegisterMethodDynamic(desc MethodDescriptor, h Handler, resolver ScopeResolver) error {
	normalized, err := desc.normalized(false)
	if err != nil {
		return err
	}
	normalized.DynamicScope = resolver != nil
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
	if resolver != nil {
		s.scopeResolvers[normalized.Method] = resolver
	}
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

// ResolveScope returns the effective scope for a method and params. The bool is
// true when the scope came from a dynamic resolver rather than the descriptor's
// static baseline.
func (s *Server) ResolveScope(method string, params json.RawMessage) (Scope, bool, error) {
	s.mu.RLock()
	desc, ok := s.descriptors[method]
	resolver := s.scopeResolvers[method]
	s.mu.RUnlock()
	if !ok {
		return "", false, fmt.Errorf("rpc method not classified: %s", method)
	}
	if resolver == nil {
		return desc.Scope, false, nil
	}
	scope, err := resolver(params)
	if err != nil {
		return "", true, err
	}
	if !ValidScope(scope) {
		return "", true, fmt.Errorf("rpc method %s resolved invalid scope %q", method, scope)
	}
	return scope, true, nil
}

// ListenUnix binds a unix socket for RPC, first acquiring an exclusive
// advisory lock (flock) on a sibling ".lock" file next to socketPath (P1.8
// startup discipline). This is the cross-process mutual-exclusion guard: a
// second daemon instance racing to bind the same socketPath — e.g. two bare
// `carina` invocations both auto-starting carina-daemon on a fresh machine
// — fails fast with ErrSocketInUse instead of os.Remove-ing the socket path
// a live first instance is already listening on and silently stealing it.
// The lock is held for the lifetime of the listener (released on Close, or
// automatically by the OS if the process dies), so a stale socket left by a
// daemon that exited without cleanup (killed, crashed) has no live lock
// holder and a fresh ListenUnix can still reclaim it.
func (s *Server) ListenUnix(socketPath string) error {
	lockPath := socketPath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("rpc: open lock %s: %w", lockPath, err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lockFile.Close()
		return fmt.Errorf("rpc: acquire lock %s: %w: %w", lockPath, err, ErrSocketInUse)
	}
	s.mu.Lock()
	s.lockFile = lockFile
	s.mu.Unlock()

	_ = os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		s.releaseLock()
		return fmt.Errorf("rpc: listen %s: %w", socketPath, err)
	}
	return s.accept(ln, OriginLocal)
}

// releaseLock unlocks and closes the ListenUnix advisory lock file, if one
// was acquired. Safe to call multiple times.
func (s *Server) releaseLock() {
	s.mu.Lock()
	lockFile := s.lockFile
	s.lockFile = nil
	s.mu.Unlock()
	if lockFile == nil {
		return
	}
	_ = unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
	_ = lockFile.Close()
}

func (s *Server) ListenTCP(addr string) error {
	if err := ValidateLoopbackTCPAddress(addr); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("rpc: listen tcp %s: %w", addr, err)
	}
	return s.accept(ln, OriginRemote)
}

// ValidateLoopbackTCPAddress permits the legacy bare TCP transport only as an
// explicit local diagnostic path. Network-facing clients must use an
// authenticated Gateway transport instead.
func ValidateLoopbackTCPAddress(addr string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("rpc: invalid tcp listen address %q: %w", addr, err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("rpc: unauthenticated tcp is restricted to explicit loopback addresses")
	}
	return nil
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
	for _, ln := range s.listeners {
		_ = ln.Close()
	}
	s.mu.Unlock()
	s.releaseLock()
	return nil
}

func (s *Server) serve(conn net.Conn, origin Origin) {
	s.serveWithScopes(conn, origin, nil)
}

func (s *Server) serveWithScopes(conn net.Conn, origin Origin, scopes []Scope) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	enc := json.NewEncoder(conn)
	done := make(chan struct{})
	// Single-writer drain: every frame for this connection — request
	// responses below and any Subscription.Notify from a stream handler
	// registered on it — funnels through one channel and one writer
	// goroutine, so wire order always matches enqueue order (P1.8).
	w := newConnWriter(enc, done)
	// Wait for drain() to actually exit before conn.Close() runs. Defers
	// are LIFO, so registration order here matters: close(done) must run
	// BEFORE <-w.stopped (drain()'s select is gated on done to return), and
	// both must run before conn.Close(). Without waiting on w.stopped, a
	// frame that enqueue() already reported as accepted can be mid-Encode
	// inside drain()'s single writer goroutine at the exact moment
	// close(done) fires; conn.Close() racing that in-flight Encode from a
	// different goroutine can silently drop the frame (drain() discards
	// the Encode error), even though its caller already observed a nil
	// error from enqueue()/Notify. Blocking here bounds shutdown to
	// whatever is already queued — it does not wait for new frames, since
	// done is already closed and drain() will not pick up anything
	// enqueued after that (enqueue() itself returns an error once done
	// fires).
	defer func() { <-w.stopped }()
	defer close(done)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = w.enqueue(Response{JSONRPC: "2.0", Error: &Error{Code: CodeParseError, Message: err.Error()}})
			continue
		}

		// Enforce transport-origin restriction before doing any work.
		if ok, reason := s.remoteAuthorized(req.Method, origin); !ok {
			_ = w.enqueue(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeMethodNotFound, Message: reason}})
			continue
		}
		if scopes != nil {
			scope, _, err := s.ResolveScope(req.Method, req.Params)
			if err != nil {
				_ = w.enqueue(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeMethodNotFound, Message: err.Error()}})
				continue
			}
			if !scopeAllowed(scope, scopes) {
				_ = w.enqueue(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeMethodNotFound, Message: "method scope not negotiated: " + req.Method + " requires " + string(scope)}})
				continue
			}
		}

		// Stream methods keep the connection open and push notifications.
		s.mu.RLock()
		streamHandler, isStream := s.streams[req.Method]
		_, classified := s.descriptors[req.Method]
		strict := s.strictMethods
		s.mu.RUnlock()
		if isStream {
			if strict && !classified {
				_ = w.enqueue(Response{JSONRPC: "2.0", ID: req.ID, Error: &Error{Code: CodeMethodNotFound, Message: "method not classified: " + req.Method}})
				continue
			}
			sub := &Subscription{id: nextSubscriptionID(), w: w, done: done, close: conn.Close}
			if err := streamHandler(req.Params, sub); err != nil {
				_ = w.enqueue(Response{JSONRPC: "2.0", ID: req.ID, Error: responseError(err)})
				continue
			}
			result := sub.result
			if result == nil {
				result = map[string]any{"subscribed": true, "subscription_id": sub.ID()}
			}
			_ = w.enqueue(Response{JSONRPC: "2.0", ID: req.ID, Result: result})
			continue
		}

		_ = w.enqueue(s.dispatch(req))
	}
}

func scopeAllowed(required Scope, allowed []Scope) bool {
	for _, scope := range allowed {
		if scope == required {
			return true
		}
	}
	return false
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
		resp.Error = responseError(err)
		return resp
	}
	resp.Result = result
	return resp
}

func responseError(err error) *Error {
	var rpcErr *Error
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	return &Error{Code: CodeInternalError, Message: err.Error()}
}
