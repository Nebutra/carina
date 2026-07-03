package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// Client is a blocking JSON-RPC 2.0 client for the daemon socket.
// It is used by pi-cli, pi-tui, and the Go SDK.
type Client struct {
	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
	nextID int64
}

func Dial(socketPath string) (*Client, error) {
	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("rpc: dial %s: %w (is the daemon running? try `pi-daemon`)", socketPath, err)
	}
	return &Client{conn: conn, reader: bufio.NewReader(conn)}, nil
}

// Call performs a single request/response round trip. result may be nil.
func (c *Client) Call(method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	id, _ := json.Marshal(c.nextID)
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("rpc: marshal params: %w", err)
	}
	req := Request{JSONRPC: "2.0", ID: id, Method: method, Params: rawParams}
	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("rpc: marshal request: %w", err)
	}
	if _, err := c.conn.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("rpc: write: %w", err)
	}

	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("rpc: read: %w", err)
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *Error          `json:"error"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("rpc: decode response: %w", err)
	}
	if resp.Error != nil {
		return resp.Error
	}
	if result != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, result)
	}
	return nil
}

func (c *Client) Close() error { return c.conn.Close() }
