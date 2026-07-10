// Package sdk provides typed JSON-RPC wrappers for Carina Runtime 0.6.1.
package sdk

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

const CompatibleRuntimeVersion = "0.6.1"

type Client struct {
	*rpc.Client
}

type Session struct {
	SessionID         string `json:"session_id"`
	WorkspaceID       string `json:"workspace_id"`
	WorkspaceRoot     string `json:"workspace_root"`
	Status            string `json:"status"`
	PermissionProfile string `json:"permission_profile"`
	CreatedAt         string `json:"created_at"`
}

type Task struct {
	TaskID      string `json:"task_id"`
	SessionID   string `json:"session_id"`
	WorkspaceID string `json:"workspace_id"`
	Status      string `json:"status"`
	UserPrompt  string `json:"user_prompt"`
}

type Event struct {
	EventID   string         `json:"event_id,omitempty"`
	SessionID string         `json:"session_id"`
	TaskID    string         `json:"task_id,omitempty"`
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Payload   map[string]any `json:"payload,omitempty"`
}

type SessionAttachment struct {
	Events []json.RawMessage `json:"events"`
	From   int               `json:"from"`
	Cursor int               `json:"cursor"`
}

type UsageCostRow struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Requests         int     `json:"requests"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	PricingKnown     bool    `json:"pricing_known"`
	Estimated        bool    `json:"estimated"`
}

type UsageCostReport struct {
	Providers []UsageCostRow `json:"providers"`
	Totals    UsageCostRow   `json:"totals"`
	Estimated bool           `json:"estimated"`
}

func DefaultSocketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".carina", "daemon.sock"), nil
}

func NewClient(inner *rpc.Client) *Client { return &Client{Client: inner} }

func Dial() (*Client, error) {
	socket, err := DefaultSocketPath()
	if err != nil {
		return nil, err
	}
	return DialPath(socket)
}

func DialPath(socketPath string) (*Client, error) {
	client, err := rpc.Dial(socketPath)
	if err != nil {
		return nil, err
	}
	return NewClient(client), nil
}

func (c *Client) SetTimeout(timeout time.Duration) { c.SetCallTimeout(timeout) }

func (c *Client) CreateSession(workspaceRoot, profile string) (Session, error) {
	if profile == "" {
		profile = "safe-edit"
	}
	var out Session
	err := c.Call("session.create", map[string]any{"workspace_root": workspaceRoot, "profile": profile}, &out)
	return out, err
}

func (c *Client) SubmitTask(sessionID, prompt string) (Task, error) {
	var out Task
	err := c.Call("task.submit", map[string]any{"session_id": sessionID, "prompt": prompt}, &out)
	return out, err
}

func (c *Client) AttachSession(sessionID string, since int) (SessionAttachment, error) {
	var out SessionAttachment
	err := c.Call("session.attach", map[string]any{"session_id": sessionID, "since": since}, &out)
	return out, err
}

func (c *Client) ForkSession(sessionID string) (Session, error) {
	var out Session
	err := c.Call("session.fork", map[string]any{"session_id": sessionID}, &out)
	return out, err
}

func (c *Client) Cost(sessionID, taskID string) (UsageCostReport, error) {
	params := map[string]any{}
	if sessionID != "" {
		params["session_id"] = sessionID
	}
	if taskID != "" {
		params["task_id"] = taskID
	}
	var out UsageCostReport
	err := c.Call("usage.cost", params, &out)
	return out, err
}

func (c *Client) SteerTask(taskID, message string) error {
	return c.Call("task.steer", map[string]any{"task_id": taskID, "message": message}, nil)
}

func (c *Client) AnswerQuestion(questionID, value string) error {
	return c.Call("task.user.answer", map[string]any{"question_id": questionID, "value": value}, nil)
}

func (c *Client) SubscribeSessionEvents(sessionID string) error {
	return c.Call("session.events.stream", map[string]any{"session_id": sessionID}, nil)
}

func (c *Client) ReadEvent() (Event, error) {
	for {
		method, params, err := c.ReadNotification()
		if err != nil {
			return Event{}, err
		}
		if method != "event" {
			continue
		}
		var event Event
		if err := json.Unmarshal(params, &event); err != nil {
			return Event{}, fmt.Errorf("sdk: decode event: %w", err)
		}
		return event, nil
	}
}
