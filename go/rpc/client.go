package rpc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// defaultCallTimeout bounds every Client.Call when the caller never sets an
// explicit timeout via SetCallTimeout: a diagnostic tool like `carina
// doctor` must fail fast and observably rather than hang forever if the
// daemon accepts the connection but its handler (or the kernel child
// process behind it) never responds.
const defaultCallTimeout = 15 * time.Second

// deadlineSetter is satisfied by net.Conn (and any other transport that
// supports a read deadline). Call uses it, when present, to bound how long
// it waits for a response; transports that don't support deadlines (e.g. a
// plain io.Pipe in a unit test) simply never get one applied.
type deadlineSetter interface {
	SetReadDeadline(t time.Time) error
}

// Client is a blocking JSON-RPC 2.0 client over any line-delimited JSON
// transport: the daemon unix socket (CLI/SDK) or a child process's stdio
// (the Rust kernel service).
type Client struct {
	mu          sync.Mutex
	w           io.Writer
	r           *bufio.Reader
	closer      io.Closer
	nextID      int64
	deadlineSet deadlineSetter // nil if the transport doesn't support read deadlines
	callTimeout time.Duration  // 0 means defaultCallTimeout

	// notifications receives server-initiated messages (method calls
	// without a request id), e.g. streamed events. May be nil.
	notifyMu sync.Mutex
	onNotify func(method string, params json.RawMessage)
}

func Dial(socketPath string) (*Client, error) {
	conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("rpc: dial %s: %w (is the daemon running? try `carina-daemon`): %w", socketPath, err, ErrDaemonUnreachable)
	}
	return NewClient(conn, conn, conn), nil
}

// DialTCP connects to a daemon exposed over TCP (remote workers, Phase 3).
func DialTCP(addr string) (*Client, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("rpc: dial %s: %w", addr, err)
	}
	return NewClient(conn, conn, conn), nil
}

// NewClient wraps an arbitrary transport. closer may be nil. If r also
// implements deadlineSetter (as net.Conn does), Call bounds its wait for a
// response with the client's call timeout (SetCallTimeout, or
// defaultCallTimeout).
func NewClient(w io.Writer, r io.Reader, closer io.Closer) *Client {
	reader := bufio.NewReaderSize(r, 1<<20)
	c := &Client{w: w, r: reader, closer: closer}
	if ds, ok := r.(deadlineSetter); ok {
		c.deadlineSet = ds
	}
	return c
}

// callTimeoutDisabled is SetCallTimeout's sentinel for "block indefinitely,
// no per-call bound" — distinct from the zero value of callTimeout (never
// called SetCallTimeout at all), which means "use defaultCallTimeout".
const callTimeoutDisabled = -1 * time.Nanosecond

// SetCallTimeout overrides the default bound (defaultCallTimeout) on how
// long Call waits for a response before returning ErrCallTimeout. Pass a
// negative duration to disable the timeout entirely (block indefinitely) —
// used by long-lived streaming-adjacent callers that intentionally want no
// per-call bound.
func (c *Client) SetCallTimeout(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d < 0 {
		c.callTimeout = callTimeoutDisabled
		return
	}
	c.callTimeout = d
}

// OnNotify installs a handler for server notifications (requests without
// an id). It must be set before concurrent Call traffic starts.
func (c *Client) OnNotify(fn func(method string, params json.RawMessage)) {
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	c.onNotify = fn
}

// Call performs a single request/response round trip. result may be nil.
// Server notifications interleaved with the response are dispatched to the
// OnNotify handler.
func (c *Client) Call(method string, params any, result any) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.nextID++
	id := c.nextID
	rawID, _ := json.Marshal(id)
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("rpc: marshal params: %w", err)
	}
	payload, err := json.Marshal(Request{JSONRPC: "2.0", ID: rawID, Method: method, Params: rawParams})
	if err != nil {
		return fmt.Errorf("rpc: marshal request: %w", err)
	}
	if _, err := c.w.Write(append(payload, '\n')); err != nil {
		return fmt.Errorf("rpc: write: %w", err)
	}

	if c.deadlineSet != nil {
		timeout := c.callTimeout
		if timeout == 0 {
			timeout = defaultCallTimeout
		}
		if timeout > 0 {
			_ = c.deadlineSet.SetReadDeadline(time.Now().Add(timeout))
			defer func() { _ = c.deadlineSet.SetReadDeadline(time.Time{}) }()
		}
	}

	for {
		line, err := c.r.ReadBytes('\n')
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return fmt.Errorf("rpc: call %s: %w: %w", method, err, ErrCallTimeout)
			}
			return fmt.Errorf("rpc: read: %w", err)
		}
		var resp struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *Error          `json:"error"`
		}
		if err := json.Unmarshal(line, &resp); err != nil {
			return fmt.Errorf("rpc: decode response: %w", err)
		}
		// Notification: no id, has method.
		if len(resp.ID) == 0 || string(resp.ID) == "null" {
			c.notifyMu.Lock()
			fn := c.onNotify
			c.notifyMu.Unlock()
			if fn != nil && resp.Method != "" {
				fn(resp.Method, resp.Params)
			}
			continue
		}
		var gotID int64
		_ = json.Unmarshal(resp.ID, &gotID)
		if gotID != id {
			continue // stale response from a previous timeout; skip
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			return json.Unmarshal(resp.Result, result)
		}
		return nil
	}
}

// ReadNotification blocks until the next server notification arrives.
// Used by clients that subscribe to event streams.
func (c *Client) ReadNotification() (string, json.RawMessage, error) {
	for {
		line, err := c.r.ReadBytes('\n')
		if err != nil {
			return "", nil, fmt.Errorf("rpc: read: %w", err)
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if (len(msg.ID) == 0 || string(msg.ID) == "null") && msg.Method != "" {
			return msg.Method, msg.Params, nil
		}
	}
}

func (c *Client) Close() error {
	if c.closer != nil {
		return c.closer.Close()
	}
	return nil
}
