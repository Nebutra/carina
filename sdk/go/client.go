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

type WorkflowRun struct {
	ID        string  `json:"id"`
	Workflow  string  `json:"workflow"`
	SessionID string  `json:"session_id"`
	Status    string  `json:"status"`
	Attempt   int     `json:"attempt"`
	Progress  float64 `json:"progress,omitempty"`
}

type Worker struct {
	WorkerID string `json:"worker_id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
}

type ChannelEvent struct {
	ID                   string         `json:"id"`
	SenderID             string         `json:"sender_id"`
	SessionID            string         `json:"session_id"`
	Kind                 string         `json:"kind"`
	Timestamp            time.Time      `json:"timestamp"`
	Payload              map[string]any `json:"payload,omitempty"`
	PermissionDecisionID string         `json:"permission_decision_id,omitempty"`
	PermissionAllow      *bool          `json:"permission_allow,omitempty"`
}

type Extension struct {
	Manifest struct {
		Name                  string `json:"name"`
		Version               string `json:"version"`
		EstimatedPromptTokens int    `json:"estimated_prompt_tokens"`
	} `json:"manifest"`
	Source  string `json:"source"`
	Enabled bool   `json:"enabled"`
	Trusted bool   `json:"trusted"`
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

func (c *Client) ListWorkflows() ([]WorkflowRun, error) {
	var out []WorkflowRun
	err := c.Call("workflow.list", map[string]any{}, &out)
	return out, err
}
func (c *Client) WorkflowDetail(runID string) (map[string]any, error) {
	var out map[string]any
	err := c.Call("workflow.detail", map[string]any{"run_id": runID}, &out)
	return out, err
}
func (c *Client) RunWorkflow(sessionID, workflow, input string) (WorkflowRun, error) {
	var out WorkflowRun
	err := c.Call("workflow.run", map[string]any{"session_id": sessionID, "workflow": workflow, "input": input}, &out)
	return out, err
}
func (c *Client) PauseWorkflow(runID string) (WorkflowRun, error) {
	return c.workflowTransition("workflow.pause", runID)
}
func (c *Client) ResumeWorkflow(runID string) (WorkflowRun, error) {
	return c.workflowTransition("workflow.resume", runID)
}
func (c *Client) StopWorkflow(runID string) (WorkflowRun, error) {
	return c.workflowTransition("workflow.stop", runID)
}
func (c *Client) RestartWorkflow(runID string) (WorkflowRun, error) {
	var out WorkflowRun
	err := c.Call("workflow.restart", map[string]any{"run_id": runID}, &out)
	return out, err
}
func (c *Client) workflowTransition(method, runID string) (WorkflowRun, error) {
	var out WorkflowRun
	err := c.Call(method, map[string]any{"run_id": runID}, &out)
	return out, err
}
func (c *Client) ListWorkers() ([]Worker, error) {
	var out []Worker
	err := c.Call("worker.list", map[string]any{}, &out)
	return out, err
}
func (c *Client) ResolveApproval(decisionID string, allow bool, approver, scope string) error {
	return c.Call("task.approval.resolve", map[string]any{"decision_id": decisionID, "allow": allow, "approver": approver, "scope": scope}, nil)
}
func (c *Client) Doctor() (map[string]any, error) {
	var out map[string]any
	err := c.Call("daemon.doctor", map[string]any{}, &out)
	return out, err
}
func (c *Client) ListAgents(workspaceRoot string) (map[string]any, error) {
	var out map[string]any
	err := c.Call("agent.list", map[string]any{"workspace_root": workspaceRoot}, &out)
	return out, err
}
func (c *Client) InjectChannelEvent(event ChannelEvent, signature string) (map[string]any, error) {
	var out map[string]any
	err := c.Call("channel.event.inject", map[string]any{"event": event, "signature": signature}, &out)
	return out, err
}
func (c *Client) ListExtensions() (map[string]any, error) {
	var out map[string]any
	err := c.Call("extension.list", map[string]any{}, &out)
	return out, err
}
func (c *Client) SetExtensionEnabled(name string, on bool) (Extension, error) {
	var out Extension
	method := "extension.disable"
	if on {
		method = "extension.enable"
	}
	err := c.Call(method, map[string]any{"name": name}, &out)
	return out, err
}
