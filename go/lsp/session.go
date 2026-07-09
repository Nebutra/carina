// Persistent LSP sessions (docs/plans/code-intelligence.md, V2): beyond the
// one-shot Diagnose probe, a Session keeps a language server alive for
// definition/references queries. It reuses the same JSON-RPC framing
// (writeMsg/readMsg) and initialize handshake, and Close always shuts the
// child down (shutdown/exit, then Kill) so a session never leaks a process.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Location is one definition/reference site. Line and Char are 1-based
// (LSP wire positions are 0-based; the session converts).
type Location struct {
	Path string
	Line int
	Char int
}

// Session is a running language server with an initialized workspace.
type Session struct {
	w       io.Writer
	timeout time.Duration
	cmd     *exec.Cmd // nil when driven over mock streams

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcResult
	closed  bool
}

type rpcResult struct {
	result json.RawMessage
	err    error
}

// StartSession spawns a language server rooted at rootDir and completes the
// initialize handshake. The binary must be on PATH. env is the child's
// complete environment (exec.Cmd.Env semantics; nil inherits the parent's) —
// language servers are agent-triggerable children, so the daemon passes a
// credential-scrubbed environment carrying the governed egress proxy
// overrides instead of its own.
func StartSession(bin string, args []string, rootDir string, timeout time.Duration, env []string) (*Session, error) {
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("lsp: server %q not found", bin)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	sess, err := newSession(stdin, stdout, PathToURI(rootDir), timeout)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, err
	}
	sess.cmd = cmd
	return sess, nil
}

// newSession drives the initialize handshake over an arbitrary stream pair
// (so it is testable against a mock server, like collect).
func newSession(w io.Writer, r io.Reader, rootURI string, timeout time.Duration) (*Session, error) {
	s := &Session{
		w:       w,
		timeout: timeout,
		pending: make(map[int]chan rpcResult),
	}
	go s.readLoop(r)
	if _, err := s.call("initialize", map[string]any{
		"processId":    nil,
		"rootUri":      rootURI,
		"capabilities": map[string]any{},
	}); err != nil {
		return nil, fmt.Errorf("lsp: initialize failed: %w", err)
	}
	if err := writeMsg(w, rpcNotify("initialized", map[string]any{})); err != nil {
		return nil, err
	}
	return s, nil
}

// readLoop dispatches responses to callers waiting in call; notifications
// (e.g. publishDiagnostics) are ignored.
func (s *Session) readLoop(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		raw, err := readMsg(br)
		if err != nil {
			s.failAll(err)
			return
		}
		var msg struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
			Method string `json:"method"`
		}
		if json.Unmarshal(raw, &msg) != nil || msg.ID == nil || msg.Method != "" {
			continue // notification or server-initiated request: ignore
		}
		s.mu.Lock()
		ch, ok := s.pending[*msg.ID]
		if ok {
			delete(s.pending, *msg.ID)
		}
		s.mu.Unlock()
		if !ok {
			continue
		}
		if msg.Error != nil {
			ch <- rpcResult{err: fmt.Errorf("lsp: server error %d: %s", msg.Error.Code, msg.Error.Message)}
			continue
		}
		ch <- rpcResult{result: msg.Result}
	}
}

func (s *Session) failAll(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.pending {
		ch <- rpcResult{err: err}
		delete(s.pending, id)
	}
	s.closed = true
}

// call sends a request and waits for its response (bounded by the timeout).
func (s *Session) call(method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("lsp: session closed")
	}
	s.nextID++
	id := s.nextID
	ch := make(chan rpcResult, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	if err := writeMsg(s.w, rpcReq(id, method, params)); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, err
	}
	select {
	case res := <-ch:
		return res.result, res.err
	case <-time.After(s.timeout):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("lsp: timed out waiting for %s response", method)
	}
}

// DidOpen announces a file's content to the server.
func (s *Session) DidOpen(path, languageID, content string) error {
	return writeMsg(s.w, rpcNotify("textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri": PathToURI(path), "languageId": languageID, "version": 1, "text": content,
		},
	}))
}

// Definition resolves textDocument/definition at a 1-based position.
func (s *Session) Definition(path string, line, char int) ([]Location, error) {
	raw, err := s.call("textDocument/definition", positionParams(path, line, char))
	if err != nil {
		return nil, err
	}
	return parseLocations(raw)
}

// References resolves textDocument/references at a 1-based position.
func (s *Session) References(path string, line, char int, includeDecl bool) ([]Location, error) {
	params := positionParams(path, line, char)
	params["context"] = map[string]any{"includeDeclaration": includeDecl}
	raw, err := s.call("textDocument/references", params)
	if err != nil {
		return nil, err
	}
	return parseLocations(raw)
}

// Close shuts the server down (shutdown/exit, then Kill).
func (s *Session) Close() error {
	_, _ = s.call("shutdown", nil)
	_ = writeMsg(s.w, rpcNotify("exit", nil))
	if s.cmd != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func positionParams(path string, line, char int) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": PathToURI(path)},
		"position":     map[string]any{"line": line - 1, "character": char - 1},
	}
}

// wireLocation accepts both Location ("uri"/"range") and LocationLink
// ("targetUri"/"targetRange") shapes.
type wireLocation struct {
	URI         string    `json:"uri"`
	Range       wireRange `json:"range"`
	TargetURI   string    `json:"targetUri"`
	TargetRange wireRange `json:"targetRange"`
}

type wireRange struct {
	Start struct {
		Line      int `json:"line"`
		Character int `json:"character"`
	} `json:"start"`
}

// parseLocations converts a definition/references result (null, a single
// Location, or an array of Location/LocationLink) into 1-based Locations.
func parseLocations(raw json.RawMessage) ([]Location, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var wires []wireLocation
	if err := json.Unmarshal(raw, &wires); err != nil {
		var single wireLocation
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, fmt.Errorf("lsp: unexpected location result: %s", string(raw))
		}
		wires = []wireLocation{single}
	}
	out := make([]Location, 0, len(wires))
	for _, wl := range wires {
		uri, rng := wl.URI, wl.Range
		if uri == "" {
			uri, rng = wl.TargetURI, wl.TargetRange
		}
		if uri == "" {
			continue
		}
		path, ok := URIToPath(uri)
		if !ok {
			continue // non-file scheme (untitled:, notebook cells): drop
		}
		out = append(out, Location{
			Path: path,
			Line: rng.Start.Line + 1,
			Char: rng.Start.Character + 1,
		})
	}
	return out, nil
}
