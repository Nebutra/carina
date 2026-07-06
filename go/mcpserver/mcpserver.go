// Package mcpserver implements the server half of the Model Context Protocol
// (JSON-RPC 2.0 over newline-delimited stdio): it exposes a Handler's tools to
// any MCP client (Claude Desktop, other agents). It is the inverse of go/mcp,
// which is the client. Security is the Handler's responsibility — the daemon
// adapter routes every call through the capability kernel.
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"sync"
)

const protocolVersion = "2024-11-05"

// Tool is an exposed MCP tool definition (mirrors tools/list entries).
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Handler supplies the tool catalog and executes calls. Implementations MUST
// enforce their own security policy — a Call reaching real side effects without
// a capability check is a vulnerability.
type Handler interface {
	Tools() []Tool
	Call(name string, args map[string]any) (string, error)
}

// Server speaks MCP as a server, bridging a Handler to an MCP client over a
// byte stream.
type Server struct {
	name    string
	version string
	handler Handler
}

// New builds a server that advertises the given name/version and serves h's tools.
func New(name, version string, h Handler) *Server {
	if version == "" {
		version = "0.0.0"
	}
	return &Server{name: name, version: version, handler: h}
}

type request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"` // absent/null => notification (no reply)
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id"`
	Result  any              `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

// Serve runs the read/dispatch loop until in is exhausted, a read error occurs,
// or ctx is cancelled. Each line is one JSON-RPC message; each reply is written
// as one newline-terminated JSON object.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var writeMu sync.Mutex
	writeResp := func(resp response) error {
		b, err := json.Marshal(resp)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if _, err := out.Write(append(b, '\n')); err != nil {
			return err
		}
		return nil
	}
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue // ignore unparseable lines rather than crash the transport
		}
		resp, isNotification := s.handle(&req)
		if isNotification {
			continue
		}
		if err := writeResp(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) handle(req *request) (response, bool) {
	if req.ID == nil {
		return response{}, true // notification (initialized, cancelled, …): no reply
	}
	switch req.Method {
	case "initialize":
		return s.ok(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.name, "version": s.version},
		}), false
	case "ping":
		return s.ok(req.ID, map[string]any{}), false
	case "tools/list":
		tools := s.handler.Tools()
		if tools == nil {
			tools = []Tool{}
		}
		return s.ok(req.ID, map[string]any{"tools": tools}), false
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return s.fail(req.ID, -32602, "invalid params"), false
		}
		text, err := s.handler.Call(p.Name, p.Arguments)
		if err != nil {
			// Per the MCP spec, tool failures are reported as isError content
			// (visible to the client's model), not JSON-RPC protocol errors.
			return s.ok(req.ID, toolResult(err.Error(), true)), false
		}
		return s.ok(req.ID, toolResult(text, false)), false
	default:
		return s.fail(req.ID, -32601, "method not found: "+req.Method), false
	}
}

func toolResult(text string, isErr bool) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	}
}

func (s *Server) ok(id *json.RawMessage, result any) response {
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *Server) fail(id *json.RawMessage, code int, msg string) response {
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}}
}
