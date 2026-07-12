// Package sdk provides typed JSON-RPC wrappers for Carina Runtime 0.6.2.
package sdk

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

const CompatibleRuntimeVersion = "0.6.2"
const streamQueueLimit = 64

var ErrStreamOverflow = errors.New("sdk: event stream overflow")

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
	RawCursor int            `json:"raw_cursor,omitempty"`
}

type SessionAttachment struct {
	Events    []json.RawMessage `json:"events"`
	From      int               `json:"from"`
	Cursor    int               `json:"cursor"`
	EventMode string            `json:"event_mode"`
}

type EventSubscription struct {
	SubscriptionID string `json:"subscription_id"`
	Cursor         int    `json:"cursor"`
	Replayed       int    `json:"replayed"`
	EventMode      string `json:"event_mode"`
}

type ReviewItem struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Status  string         `json:"status"`
	TaskID  string         `json:"task_id,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

type SessionReview struct {
	SessionID         string         `json:"session_id"`
	ProjectionVersion string         `json:"projection_version"`
	SourceCursor      string         `json:"source_cursor"`
	State             string         `json:"state"`
	Summary           string         `json:"summary,omitempty"`
	WaitingReason     string         `json:"waiting_reason,omitempty"`
	Intent            string         `json:"intent,omitempty"`
	SuccessCriteria   []any          `json:"success_criteria"`
	Changes           []ReviewItem   `json:"changes"`
	Commands          []ReviewItem   `json:"commands"`
	Tools             []ReviewItem   `json:"tools"`
	Checks            []ReviewItem   `json:"checks"`
	Diagnostics       []ReviewItem   `json:"diagnostics"`
	PolicyDecisions   []ReviewItem   `json:"policy_decisions"`
	Questions         []ReviewItem   `json:"questions"`
	Conflicts         []ReviewItem   `json:"conflicts"`
	RiskAndPolicy     []ReviewItem   `json:"risk_and_policy"`
	ArtifactIDs       []string       `json:"artifact_ids"`
	Rollback          map[string]any `json:"rollback"`
	Stats             map[string]int `json:"stats"`
}

type SessionItemEvent struct {
	Type          string         `json:"type"`
	SessionID     string         `json:"session_id"`
	TurnID        string         `json:"turn_id,omitempty"`
	TaskID        string         `json:"task_id,omitempty"`
	ItemID        string         `json:"item_id,omitempty"`
	SourceEventID string         `json:"source_event_id,omitempty"`
	Timestamp     string         `json:"timestamp,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
	Item          *ReviewItem    `json:"item,omitempty"`
}
type SessionItemsPage struct {
	Data              []SessionItemEvent `json:"data"`
	NextCursor        string             `json:"next_cursor,omitempty"`
	ProjectionVersion string             `json:"projection_version"`
}
type CursorRecovery struct {
	Code              string `json:"code"`
	ProjectionVersion string `json:"projection_version"`
	Recovery          string `json:"recovery"`
	SnapshotMethod    string `json:"snapshot_method"`
	EarliestCursor    string `json:"earliest_cursor,omitempty"`
}

func CursorRecoveryFromError(err error) (CursorRecovery, bool) {
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32010 {
		return CursorRecovery{}, false
	}
	raw, marshalErr := json.Marshal(rpcErr.Data)
	if marshalErr != nil {
		return CursorRecovery{}, false
	}
	var recovery CursorRecovery
	if json.Unmarshal(raw, &recovery) != nil || recovery.Code == "" {
		return CursorRecovery{}, false
	}
	return recovery, true
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
	ProjectionVersion      string         `json:"projection_version,omitempty"`
	MinimumProtocolVersion string         `json:"minimum_protocol_version,omitempty"`
	Capabilities           map[string]any `json:"capabilities"`
}

func validateRuntimeInfo(info RuntimeInfo) error {
	version := strings.TrimPrefix(info.ProtocolVersion, "v")
	major := strings.SplitN(version, ".", 2)[0]
	if major != "1" {
		return fmt.Errorf("sdk: incompatible runtime protocol %q", info.ProtocolVersion)
	}
	if enabled, ok := info.Capabilities["tool_call_lifecycle"].(bool); !ok || !enabled {
		return errors.New("sdk: runtime lacks required tool_call_lifecycle capability")
	}
	if !compatibleEventSchema(info.Capabilities["event_schema_version"]) {
		return fmt.Errorf("sdk: incompatible event schema %q; require 0.3.x", info.Capabilities["event_schema_version"])
	}
	return nil
}

func compatibleEventSchema(raw any) bool {
	v, ok := raw.(string)
	if !ok {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	return len(parts) == 3 && parts[0] == "0" && parts[1] == "3"
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

type SearchResult struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type WorkspaceFile struct {
	Content string `json:"content"`
	Hash    string `json:"hash"`
}

type Checkpoint struct {
	CheckpointID   string   `json:"checkpoint_id"`
	TaskID         string   `json:"task_id"`
	SessionID      string   `json:"session_id"`
	Turn           int      `json:"turn"`
	Summary        string   `json:"summary,omitempty"`
	AppliedPatches []string `json:"applied_patches"`
}

type ArtifactScope struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
}
type ArtifactMetadata struct {
	ID          string        `json:"id"`
	Scope       ArtifactScope `json:"scope"`
	MediaType   string        `json:"media_type,omitempty"`
	Bytes       int64         `json:"bytes"`
	CreatedAt   string        `json:"created_at"`
	ExpiresAt   string        `json:"expires_at,omitempty"`
	Preview     string        `json:"preview,omitempty"`
	Truncated   bool          `json:"truncated"`
	PreviewUTF8 bool          `json:"preview_utf8"`
}
type ArtifactReadPage struct {
	Metadata      ArtifactMetadata `json:"metadata"`
	Offset        int64            `json:"offset"`
	NextOffset    int64            `json:"next_offset"`
	EOF           bool             `json:"eof"`
	ContentBase64 string           `json:"content_base64"`
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

func (c *Client) GetSession(sessionID string) (Session, error) {
	var out Session
	err := c.Call("session.get", map[string]any{"session_id": sessionID}, &out)
	return out, err
}

func (c *Client) ListSessions() ([]Session, error) {
	var out []Session
	err := c.Call("session.list", map[string]any{}, &out)
	return out, err
}

func (c *Client) ReplaySession(sessionID string) ([]Event, error) {
	var out []Event
	err := c.Call("session.replay", map[string]any{"session_id": sessionID}, &out)
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
	out := make(chan StreamEvent, 32)
	go func() {
		defer close(out)
		emitTerminal := func(event StreamEvent) {
			for {
				select {
				case out <- event:
					return
				default:
					// Preserve an explicit terminal outcome even when the consumer
					// fell behind; an older progress event is less important than
					// knowing the stream overflowed, failed, or completed.
					select {
					case <-out:
					default:
					}
				}
			}
		}
		inbox := make(chan Event, streamQueueLimit)
		overloaded := make(chan struct{})
		var overloadOnce sync.Once
		removeListener := t.client.AddNotificationListener(func(method string, params json.RawMessage) {
			if method != "event" {
				return
			}
			var event Event
			if json.Unmarshal(params, &event) == nil && event.SessionID == t.Session.SessionID {
				select {
				case inbox <- event:
				default:
					overloadOnce.Do(func() { close(overloaded) })
				}
			}
		})
		defer removeListener()
		drain := func() error {
			for {
				select {
				case <-overloaded:
					return ErrStreamOverflow
				default:
				}
				select {
				case event := <-inbox:
					select {
					case out <- StreamEvent{Type: "event", Event: &event}:
					default:
						return ErrStreamOverflow
					}
				default:
					return nil
				}
			}
		}

		subscriptionID, err := t.client.StartSessionEventStream(t.Session.SessionID)
		if err != nil {
			emitTerminal(StreamEvent{Type: "turn.failed", Err: err})
			return
		}
		defer func() {
			_ = drain()
			if subscriptionID != "" {
				_ = t.client.UnsubscribeSessionEvents(subscriptionID)
			}
		}()

		params := map[string]any{"session_id": t.Session.SessionID, "prompt": prompt}
		if len(opts.OutputSchema) > 0 {
			params["output_schema"] = json.RawMessage(opts.OutputSchema)
		}
		var task Task
		if err := t.client.Call("task.submit", params, &task); err != nil {
			emitTerminal(StreamEvent{Type: "turn.failed", Err: err})
			return
		}
		if err := drain(); err != nil {
			_ = t.client.Call("task.cancel", map[string]any{"task_id": task.TaskID}, nil)
			emitTerminal(StreamEvent{Type: "turn.failed", Err: err})
			return
		}
		interval := opts.PollInterval
		if interval <= 0 {
			interval = 50 * time.Millisecond
		}
		for {
			select {
			case <-ctx.Done():
				_ = t.client.Call("task.cancel", map[string]any{"task_id": task.TaskID}, nil)
				_ = drain()
				emitTerminal(StreamEvent{Type: "turn.failed", Err: ctx.Err()})
				return
			case <-time.After(interval):
			}
			var current Task
			if err := t.client.Call("task.result", map[string]any{"task_id": task.TaskID}, &current); err != nil {
				emitTerminal(StreamEvent{Type: "turn.failed", Err: err})
				return
			}
			if err := drain(); err != nil {
				_ = t.client.Call("task.cancel", map[string]any{"task_id": task.TaskID}, nil)
				emitTerminal(StreamEvent{Type: "turn.failed", Err: err})
				return
			}
			switch current.Status {
			case "completed", "degraded", "failed", "cancelled", "needs_input":
				result := TurnResult{Task: current, FinalResponse: current.Summary}
				if len(opts.OutputSchema) > 0 {
					_ = json.Unmarshal([]byte(current.Summary), &result.StructuredOutput)
				}
				emitTerminal(StreamEvent{Type: "turn.completed", Result: &result})
				return
			}
		}
	}()
	return out
}

func (c *Client) SubmitGoal(sessionID, prompt string, criteria []SuccessCheck) (Task, error) {
	var out Task
	err := c.Call("task.submit", map[string]any{"session_id": sessionID, "prompt": prompt, "success_criteria": criteria}, &out)
	return out, err
}

func (c *Client) AttachSession(sessionID string, since int) (SessionAttachment, error) {
	return c.AttachSessionMode(sessionID, since, "compat")
}

func (c *Client) AttachSessionMode(sessionID string, since int, eventMode string) (SessionAttachment, error) {
	var out SessionAttachment
	err := c.Call("session.attach", map[string]any{"session_id": sessionID, "since": since, "event_mode": eventMode}, &out)
	return out, err
}

func (c *Client) ReviewSession(sessionID string) (SessionReview, error) {
	var out SessionReview
	err := c.Call("session.review", map[string]any{"session_id": sessionID}, &out)
	return out, err
}

func (c *Client) ListSessionItems(sessionID, cursor string, limit int) (SessionItemsPage, error) {
	if limit <= 0 {
		limit = 50
	}
	params := map[string]any{"session_id": sessionID, "limit": limit}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var out SessionItemsPage
	err := c.Call("session.items", params, &out)
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
	_, err := c.StartSessionEventStream(sessionID)
	return err
}

func (c *Client) StartSessionEventStream(sessionID string) (string, error) {
	return c.StartSessionEventStreamMode(sessionID, "compat")
}

func (c *Client) StartSessionEventStreamMode(sessionID, eventMode string) (string, error) {
	out, err := c.StartSessionEventStreamFrom(sessionID, 0, eventMode)
	return out.SubscriptionID, err
}

func (c *Client) StartSessionEventStreamFrom(sessionID string, since int, eventMode string) (EventSubscription, error) {
	var out EventSubscription
	err := c.Call("session.events.stream", map[string]any{"session_id": sessionID, "since": since, "event_mode": eventMode}, &out)
	return out, err
}

func (c *Client) UnsubscribeSessionEvents(subscriptionID string) error {
	return c.Call("session.events.unsubscribe", map[string]any{"subscription_id": subscriptionID}, nil)
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
	err := c.Call("runtime.initialize", map[string]any{"protocol_version": "1.2.0", "schema_version": "1.2.0", "projection_version": "1.0.0", "client_name": clientName, "client_version": clientVersion}, &out)
	if err == nil {
		err = validateRuntimeInfo(out)
	}
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

func (c *Client) SearchWorkspace(sessionID, pattern string) ([]SearchResult, error) {
	var out []SearchResult
	err := c.Call("workspace.search", map[string]any{"session_id": sessionID, "pattern": pattern}, &out)
	return out, err
}

func (c *Client) GetWorkspaceFile(sessionID, path string) (WorkspaceFile, error) {
	var out WorkspaceFile
	err := c.Call("workspace.file.get", map[string]any{"session_id": sessionID, "path": path}, &out)
	return out, err
}

func (c *Client) ProposePatch(sessionID string, files []map[string]string, reason string) (map[string]any, error) {
	var out map[string]any
	err := c.Call("workspace.patch.propose", map[string]any{"session_id": sessionID, "files": files, "reason": reason}, &out)
	return out, err
}

func (c *Client) ApplyPatch(sessionID, patchID string) (map[string]any, error) {
	var out map[string]any
	err := c.Call("workspace.patch.apply", map[string]any{"session_id": sessionID, "patch_id": patchID}, &out)
	return out, err
}

func (c *Client) RollbackPatch(sessionID, patchID string) (map[string]any, error) {
	var out map[string]any
	err := c.Call("workspace.patch.rollback", map[string]any{"session_id": sessionID, "patch_id": patchID}, &out)
	return out, err
}

func (c *Client) Exec(sessionID string, argv []string, taskID string) (map[string]any, error) {
	params := map[string]any{"session_id": sessionID, "argv": argv}
	if taskID != "" {
		params["task_id"] = taskID
	}
	var out map[string]any
	err := c.Call("command.exec", params, &out)
	return out, err
}

func (c *Client) AuditReport(sessionID string) (map[string]any, error) {
	var out map[string]any
	err := c.Call("audit.report", map[string]any{"session_id": sessionID}, &out)
	return out, err
}
func (c *Client) ResolveApproval(decisionID string, allow bool, approver, scope string) error {
	return c.Call("task.approval.resolve", map[string]any{"decision_id": decisionID, "approve": allow, "approver": approver, "scope": scope}, nil)
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

func artifactParams(scope ArtifactScope, id string) map[string]any {
	return map[string]any{"session_id": scope.SessionID, "task_id": scope.TaskID, "call_id": scope.CallID, "artifact_id": id}
}
func (c *Client) StatArtifact(scope ArtifactScope, id string) (ArtifactMetadata, error) {
	var out ArtifactMetadata
	err := c.Call("artifact.stat", artifactParams(scope, id), &out)
	return out, err
}
func (c *Client) ReadArtifactPage(scope ArtifactScope, id string, offset, limit int64) (ArtifactReadPage, error) {
	p := artifactParams(scope, id)
	p["offset"] = offset
	p["limit"] = limit
	var out ArtifactReadPage
	err := c.Call("artifact.read", p, &out)
	return out, err
}
func (c *Client) DownloadArtifact(scope ArtifactScope, id string, maxBytes int64) ([]byte, ArtifactMetadata, error) {
	if maxBytes <= 0 {
		return nil, ArtifactMetadata{}, errors.New("sdk: maxBytes must be positive")
	}
	var all []byte
	var meta ArtifactMetadata
	var off int64
	for {
		page, err := c.ReadArtifactPage(scope, id, off, 1<<20)
		if err != nil {
			return nil, meta, err
		}
		meta = page.Metadata
		chunk, err := base64.StdEncoding.DecodeString(page.ContentBase64)
		if err != nil {
			return nil, meta, fmt.Errorf("sdk: decode artifact page: %w", err)
		}
		if int64(len(all)+len(chunk)) > maxBytes {
			return nil, meta, fmt.Errorf("sdk: artifact exceeds download limit %d", maxBytes)
		}
		all = append(all, chunk...)
		if page.EOF {
			break
		}
		if page.NextOffset <= off {
			return nil, meta, errors.New("sdk: artifact pagination did not advance")
		}
		off = page.NextOffset
	}
	sum := sha256.Sum256(all)
	if hex.EncodeToString(sum[:]) != id {
		return nil, meta, errors.New("sdk: artifact digest mismatch")
	}
	return all, meta, nil
}
