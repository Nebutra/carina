// Package mcp is a production client for the Model Context Protocol: it connects
// to external MCP servers over stdio JSON-RPC, performs the initialize / list /
// call lifecycle, and proxies tool calls. Carina layers the capability kernel +
// audit on top (every proxied call is gated), so MCP tools are subject to the
// same policy as native tools. Transport: newline-delimited JSON-RPC 2.0 over
// the server subprocess's stdin/stdout (the MCP stdio transport).
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	protocolVersion = "2024-11-05"
	callTimeout     = 30 * time.Second
)

// Server is one configured MCP server (mcpServers entry in mcp.json).
type Server struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// Tool mirrors an MCP tool definition from tools/list.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// NamespacedTool is a tool exposed to the agent as mcp__<server>__<name>.
type NamespacedTool struct {
	Server      string `json:"server"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Prompt mirrors an MCP prompt definition from prompts/list.
type Prompt struct {
	Server      string           `json:"server,omitempty"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Arguments   []PromptArgument `json:"arguments,omitempty"`
}

// PromptArgument mirrors an MCP prompt argument definition.
type PromptArgument struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type toolsListResult struct {
	Tools []Tool `json:"tools"`
}

type promptsListResult struct {
	Prompts []Prompt `json:"prompts"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

type promptMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type promptGetResult struct {
	Messages []promptMessage `json:"messages"`
}

// Client manages one MCP server subprocess. A background reader dispatches
// responses to per-call channels by id, so calls have clean timeouts and the
// stream can fail all in-flight calls on disconnect.
type Client struct {
	name   string
	server Server

	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcResponse
	closed  bool

	tools   []Tool
	prompts []Prompt
}

func newClient(name string, s Server) *Client {
	return &Client{name: name, server: s, pending: make(map[int64]chan rpcResponse)}
}

func (c *Client) connect() error {
	cmd := exec.Command(c.server.Command, c.server.Args...)
	cmd.Env = os.Environ()
	for k, v := range c.server.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp %q: start %s: %w", c.name, c.server.Command, err)
	}
	c.mu.Lock()
	c.cmd, c.stdin, c.closed = cmd, stdin, false
	c.pending = make(map[int64]chan rpcResponse)
	c.mu.Unlock()
	go c.readLoop(stdout)

	if err := c.call("initialize", map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "carina", "version": "0.5.0"},
	}, nil); err != nil {
		c.close()
		return err
	}
	_ = c.notify("notifications/initialized", map[string]any{})
	var tl toolsListResult
	_ = c.call("tools/list", map[string]any{}, &tl)
	var pl promptsListResult
	_ = c.call("prompts/list", map[string]any{}, &pl)
	c.mu.Lock()
	c.tools = tl.Tools
	c.prompts = pl.Prompts
	c.mu.Unlock()
	return nil
}

func (c *Client) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var resp rpcResponse
		if json.Unmarshal(sc.Bytes(), &resp) != nil || resp.ID == nil {
			continue // parse error, or a server notification/request (no id)
		}
		c.mu.Lock()
		ch := c.pending[*resp.ID]
		delete(c.pending, *resp.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp
		}
	}
	// Stream closed: fail every in-flight call.
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
}

func (c *Client) call(method string, params any, result any) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("mcp %q: disconnected", c.name)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	req := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	line, _ := json.Marshal(req)
	c.writeMu.Lock()
	_, werr := c.stdin.Write(append(line, '\n'))
	c.writeMu.Unlock()
	if werr != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("mcp %q write: %w", c.name, werr)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("mcp %q: connection closed during %s", c.name, method)
		}
		if resp.Error != nil {
			return fmt.Errorf("mcp %s: %s", method, resp.Error.Message)
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	case <-time.After(callTimeout):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return fmt.Errorf("mcp %s: timeout after %s", method, callTimeout)
	}
}

func (c *Client) notify(method string, params any) error {
	req := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	line, _ := json.Marshal(req)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.stdin.Write(append(line, '\n'))
	return err
}

// callTool invokes an MCP tool and flattens its text content.
func (c *Client) callTool(name string, args map[string]any) (string, error) {
	if args == nil {
		args = map[string]any{}
	}
	var res toolCallResult
	if err := c.call("tools/call", map[string]any{"name": name, "arguments": args}, &res); err != nil {
		return "", err
	}
	var out string
	for _, b := range res.Content {
		if b.Type == "text" {
			out += b.Text
		}
	}
	if res.IsError {
		return out, fmt.Errorf("mcp tool %q returned an error", name)
	}
	return out, nil
}

// getPrompt renders an MCP prompt and flattens text message content.
func (c *Client) getPrompt(name string, args map[string]string) (string, error) {
	if args == nil {
		args = map[string]string{}
	}
	var res promptGetResult
	if err := c.call("prompts/get", map[string]any{"name": name, "arguments": args}, &res); err != nil {
		return "", err
	}
	out := flattenPromptMessages(res.Messages)
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("mcp prompt %q returned no text content", name)
	}
	return out, nil
}

func flattenPromptMessages(messages []promptMessage) string {
	var parts []string
	for _, msg := range messages {
		text := strings.TrimSpace(flattenPromptContent(msg.Content))
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func flattenPromptContent(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var block contentBlock
	if json.Unmarshal(raw, &block) == nil && block.Type == "text" {
		return block.Text
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var out strings.Builder
		for _, b := range blocks {
			if b.Type == "text" {
				out.WriteString(b.Text)
			}
		}
		return out.String()
	}
	return ""
}

func (c *Client) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *Client) close() {
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
}

// ---- Manager --------------------------------------------------------------

type config struct {
	MCPServers map[string]Server `json:"mcpServers"`
}

// Manager owns the set of connected MCP servers.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
}

func NewManager() *Manager { return &Manager{clients: make(map[string]*Client)} }

// LoadAndConnect reads mcp.json config files and connects each server (best
// effort — a server that fails to start is skipped, not fatal).
func (m *Manager) LoadAndConnect(paths ...string) {
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var cfg config
		if json.Unmarshal(raw, &cfg) != nil {
			continue
		}
		for name, srv := range cfg.MCPServers {
			c := newClient(name, srv)
			if c.connect() == nil {
				m.mu.Lock()
				if old := m.clients[name]; old != nil {
					old.close()
				}
				m.clients[name] = c
				m.mu.Unlock()
			}
		}
	}
}

// Tools returns every connected server's tools, namespaced for the agent.
func (m *Manager) Tools() []NamespacedTool {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []NamespacedTool
	for name, c := range m.clients {
		c.mu.Lock()
		for _, t := range c.tools {
			out = append(out, NamespacedTool{Server: name, Name: t.Name, Description: t.Description})
		}
		c.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server == out[j].Server {
			return out[i].Name < out[j].Name
		}
		return out[i].Server < out[j].Server
	})
	return out
}

// Prompts returns every connected server's prompts, namespaced by server.
func (m *Manager) Prompts() []Prompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Prompt
	for name, c := range m.clients {
		c.mu.Lock()
		for _, p := range c.prompts {
			cp := p
			cp.Server = name
			cp.Arguments = append([]PromptArgument(nil), p.Arguments...)
			out = append(out, cp)
		}
		c.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Server == out[j].Server {
			return out[i].Name < out[j].Name
		}
		return out[i].Server < out[j].Server
	})
	return out
}

// Call invokes a tool on a server, reconnecting once if the server has died.
func (m *Manager) Call(server, tool string, args map[string]any) (string, error) {
	m.mu.Lock()
	c := m.clients[server]
	m.mu.Unlock()
	if c == nil {
		return "", fmt.Errorf("unknown mcp server %q", server)
	}
	if c.isClosed() {
		if err := c.connect(); err != nil {
			return "", fmt.Errorf("mcp server %q is down: %w", server, err)
		}
	}
	return c.callTool(tool, args)
}

// GetPrompt renders a prompt on a server, reconnecting once if the server has died.
func (m *Manager) GetPrompt(server, prompt string, args map[string]string) (string, error) {
	m.mu.Lock()
	c := m.clients[server]
	m.mu.Unlock()
	if c == nil {
		return "", fmt.Errorf("unknown mcp server %q", server)
	}
	if c.isClosed() {
		if err := c.connect(); err != nil {
			return "", fmt.Errorf("mcp server %q is down: %w", server, err)
		}
	}
	return c.getPrompt(prompt, args)
}

// Servers returns the names of connected servers.
func (m *Manager) Servers() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.clients))
	for n := range m.clients {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.close()
	}
	m.clients = map[string]*Client{}
}
