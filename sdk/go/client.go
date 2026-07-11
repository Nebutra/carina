// Package sdk provides typed JSON-RPC wrappers for Carina Runtime 0.6.1.
package sdk

import (
	"context"
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
	Summary     string `json:"summary,omitempty"`
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

type RuntimeInfo struct {
	RuntimeVersion         string         `json:"runtime_version"`
	ProtocolVersion        string         `json:"protocol_version"`
	MinimumProtocolVersion string         `json:"minimum_protocol_version,omitempty"`
	Capabilities           map[string]any `json:"capabilities"`
}
type RunOptions struct {
	OutputSchema json.RawMessage
	PollInterval time.Duration
}
type TurnResult struct {
	Task             Task   `json:"task"`
	FinalResponse    string `json:"final_response"`
	StructuredOutput any    `json:"structured_output,omitempty"`
}
type StreamEvent struct {
	Type   string      `json:"type"`
	Event  *Event      `json:"event,omitempty"`
	Result *TurnResult `json:"result,omitempty"`
	Err    error       `json:"-"`
}
type Thread struct {
	client  *Client
	Session Session
}

type AgentViewEntry struct {
	SessionID     string `json:"session_id"`
	TaskID        string `json:"task_id,omitempty"`
	State         string `json:"state"`
	Title         string `json:"title,omitempty"`
	Summary       string `json:"summary,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
}

type AgentView struct {
	NeedsInput []AgentViewEntry `json:"needs_input"`
	Working    []AgentViewEntry `json:"working"`
	Completed  []AgentViewEntry `json:"completed"`
}

type SuccessCheck struct {
	Kind    string   `json:"kind"`
	Path    string   `json:"path,omitempty"`
	Pattern string   `json:"pattern,omitempty"`
	Command []string `json:"command,omitempty"`
}

type Checkpoint struct {
	CheckpointID   string   `json:"checkpoint_id"`
	TaskID         string   `json:"task_id"`
	SessionID      string   `json:"session_id"`
	Turn           int      `json:"turn"`
	Summary        string   `json:"summary,omitempty"`
	AppliedPatches []string `json:"applied_patches"`
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
func (c *Client) StartThread(workspaceRoot, profile string) (*Thread, error) {
	if _, err := c.Initialize("carina-sdk-go", CompatibleRuntimeVersion); err != nil {
		return nil, err
	}
	s, err := c.CreateSession(workspaceRoot, profile)
	if err != nil {
		return nil, err
	}
	return &Thread{client: c, Session: s}, nil
}
func (c *Client) ResumeThread(sessionID string) (*Thread, error) {
	if _, err := c.Initialize("carina-sdk-go", CompatibleRuntimeVersion); err != nil {
		return nil, err
	}
	var s Session
	err := c.Call("session.get", map[string]any{"session_id": sessionID}, &s)
	if err != nil {
		return nil, err
	}
	return &Thread{client: c, Session: s}, nil
}
func (c *Client) ForkThread(sessionID, lastTaskID string, throughTurn int) (*Thread, error) {
	params := map[string]any{"session_id": sessionID}
	if lastTaskID != "" {
		params["last_task_id"] = lastTaskID
	}
	if throughTurn > 0 {
		params["through_turn"] = throughTurn
	}
	var s Session
	if err := c.Call("session.fork", params, &s); err != nil {
		return nil, err
	}
	return &Thread{client: c, Session: s}, nil
}
func (t *Thread) Fork(lastTaskID string, throughTurn int) (*Thread, error) {
	return t.client.ForkThread(t.Session.SessionID, lastTaskID, throughTurn)
}
func (t *Thread) Run(ctx context.Context, prompt string, opts RunOptions) (TurnResult, error) {
	params := map[string]any{"session_id": t.Session.SessionID, "prompt": prompt}
	if len(opts.OutputSchema) > 0 {
		params["output_schema"] = json.RawMessage(opts.OutputSchema)
	}
	var task Task
	if err := t.client.Call("task.submit", params, &task); err != nil {
		return TurnResult{}, err
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}
	for {
		select {
		case <-ctx.Done():
			_ = t.client.Call("task.cancel", map[string]any{"task_id": task.TaskID}, nil)
			return TurnResult{}, ctx.Err()
		case <-time.After(interval):
		}
		var current Task
		if err := t.client.Call("task.result", map[string]any{"task_id": task.TaskID}, &current); err != nil {
			return TurnResult{}, err
		}
		switch current.Status {
		case "completed", "degraded", "failed", "cancelled", "needs_input":
			result := TurnResult{Task: current, FinalResponse: current.Summary}
			if len(opts.OutputSchema) > 0 {
				_ = json.Unmarshal([]byte(current.Summary), &result.StructuredOutput)
			}
			return result, nil
		}
	}
}
func (t *Thread) RunStreamed(ctx context.Context, prompt string, opts RunOptions) <-chan StreamEvent {
	out := make(chan StreamEvent, 1)
	go func() {
		defer close(out)
		result, err := t.Run(ctx, prompt, opts)
		if err != nil {
			out <- StreamEvent{Type: "turn.failed", Err: err}
			return
		}
		out <- StreamEvent{Type: "turn.completed", Result: &result}
	}()
	return out
}

func (c *Client) SubmitGoal(sessionID, prompt string, criteria []SuccessCheck) (Task, error) {
	var out Task
	err := c.Call("task.submit", map[string]any{"session_id": sessionID, "prompt": prompt, "success_criteria": criteria}, &out)
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
func (c *Client) Initialize(clientName, clientVersion string) (RuntimeInfo, error) {
	var out RuntimeInfo
	err := c.Call("runtime.initialize", map[string]any{"protocol_version": "1.1.0", "schema_version": "1.1.0", "client_name": clientName, "client_version": clientVersion}, &out)
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
func (c *Client) AgentView() (AgentView, error) {
	var out AgentView
	err := c.Call("agent.view", map[string]any{}, &out)
	return out, err
}
func (c *Client) ListCheckpoints(sessionID string) ([]Checkpoint, error) {
	var out []Checkpoint
	err := c.Call("session.checkpoint.list", map[string]any{"session_id": sessionID}, &out)
	return out, err
}
func (c *Client) PreviewCheckpoint(sessionID, checkpointID string) (map[string]any, error) {
	var out map[string]any
	err := c.Call("session.checkpoint.preview", map[string]any{"session_id": sessionID, "checkpoint_id": checkpointID}, &out)
	return out, err
}
func (c *Client) SummarizeCheckpoint(sessionID, checkpointID string) (map[string]any, error) {
	var out map[string]any
	err := c.Call("session.checkpoint.summarize", map[string]any{"session_id": sessionID, "checkpoint_id": checkpointID}, &out)
	return out, err
}
func (c *Client) RestoreCheckpoint(sessionID, checkpointID string, confirmed bool) (map[string]any, error) {
	var out map[string]any
	err := c.Call("session.checkpoint.restore", map[string]any{"session_id": sessionID, "checkpoint_id": checkpointID, "confirmed": confirmed}, &out)
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
