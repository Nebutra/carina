package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
)

// ConnectionController owns the mutable target of one TUI connection loop.
// Switching closes the current stream to interrupt ReadNotification; the loop
// then reconnects at cursor zero with fresh reconciliation trackers.
type ConnectionController struct {
	mu         sync.Mutex
	target     ConnectionTarget
	generation uint64
	stream     *rpc.Client
	prepared   map[uint64]*preparedConnection
	nextToken  uint64
}

// ConnectionTarget is the complete identity needed to attach one TUI session.
// RuntimeSpec is nil only for the deprecated legacy/external socket path.
type ConnectionTarget struct {
	Socket        string
	SessionID     string
	WorkspaceRoot string
	StateDir      string
	RuntimeSpec   *localruntime.Spec
	AutoStart     bool
}

type preparedConnection struct {
	token            uint64
	sourceGeneration uint64
	target           ConnectionTarget
	call             *rpc.Client
	stream           *rpc.Client
	initial          attachBatch
	gap              attachBatch
}

func NewConnectionController(initial ...ConnectionTarget) *ConnectionController {
	c := &ConnectionController{prepared: make(map[uint64]*preparedConnection)}
	if len(initial) > 0 {
		c.target = cloneConnectionTarget(initial[0])
		c.generation = 1
	}
	return c
}

func (c *ConnectionController) Switch(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	c.mu.Lock()
	target := cloneConnectionTarget(c.target)
	target.SessionID = sessionID
	c.target = target
	c.generation++
	stream := c.stream
	c.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	return nil
}

// PrepareTarget establishes and validates the destination call and stream
// connections without disturbing the currently published target.
func (c *ConnectionController) PrepareTarget(target ConnectionTarget) (uint64, error) {
	if err := validateConnectionTarget(target); err != nil {
		return 0, err
	}
	c.mu.Lock()
	sourceGeneration := c.generation
	c.nextToken++
	token := c.nextToken
	c.mu.Unlock()

	prepared, err := prepareConnection(target)
	if err != nil {
		return 0, err
	}
	prepared.token = token
	prepared.sourceGeneration = sourceGeneration

	c.mu.Lock()
	if c.generation != sourceGeneration {
		c.mu.Unlock()
		prepared.close()
		return 0, fmt.Errorf("connection target changed during preparation")
	}
	c.prepared[token] = prepared
	c.mu.Unlock()
	return token, nil
}

// CommitPrepared publishes a fully attached destination in one infallible
// state transition, then interrupts the source stream so the connection loop
// can adopt the prepared clients.
func (c *ConnectionController) CommitPrepared(token uint64) error {
	c.mu.Lock()
	prepared := c.prepared[token]
	if prepared == nil {
		c.mu.Unlock()
		return fmt.Errorf("prepared connection %d is unavailable", token)
	}
	if prepared.sourceGeneration != c.generation {
		delete(c.prepared, token)
		c.mu.Unlock()
		prepared.close()
		return fmt.Errorf("connection target changed before commit")
	}
	delete(c.prepared, token)
	c.target = cloneConnectionTarget(prepared.target)
	c.generation++
	prepared.sourceGeneration = c.generation
	c.prepared[token] = prepared
	stream := c.stream
	c.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	return nil
}

func (c *ConnectionController) AbortPrepared(token uint64) {
	c.mu.Lock()
	prepared := c.prepared[token]
	delete(c.prepared, token)
	c.mu.Unlock()
	if prepared != nil {
		prepared.close()
	}
}

func (c *ConnectionController) targetState(initial ConnectionTarget) (ConnectionTarget, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.target.SessionID == "" && c.target.Socket == "" {
		c.target = cloneConnectionTarget(initial)
		if c.generation == 0 {
			c.generation = 1
		}
	}
	return cloneConnectionTarget(c.target), c.generation
}

// state keeps the session-only controller contract used by same-runtime
// callers and older focused tests.
func (c *ConnectionController) state(initial string) (string, uint64) {
	target, generation := c.targetState(ConnectionTarget{SessionID: initial})
	return target.SessionID, generation
}

func (c *ConnectionController) bind(stream *rpc.Client) {
	c.mu.Lock()
	c.stream = stream
	c.mu.Unlock()
}

func (c *ConnectionController) adoptCreated(sessionID string) {
	c.mu.Lock()
	if c.target.SessionID == "" {
		c.target.SessionID = sessionID
	}
	c.mu.Unlock()
}

func (c *ConnectionController) takePrepared(generation uint64) *preparedConnection {
	c.mu.Lock()
	defer c.mu.Unlock()
	for token, prepared := range c.prepared {
		if prepared.sourceGeneration == generation {
			delete(c.prepared, token)
			return prepared
		}
	}
	return nil
}

func (c *ConnectionController) unbind(stream *rpc.Client) {
	c.mu.Lock()
	if c.stream == stream {
		c.stream = nil
	}
	c.mu.Unlock()
}

func cloneConnectionTarget(target ConnectionTarget) ConnectionTarget {
	out := target
	if target.RuntimeSpec != nil {
		spec := *target.RuntimeSpec
		out.RuntimeSpec = &spec
	}
	return out
}

func validateConnectionTarget(target ConnectionTarget) error {
	if strings.TrimSpace(target.SessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	if target.RuntimeSpec != nil {
		return target.RuntimeSpec.Validate()
	}
	if strings.TrimSpace(target.Socket) == "" {
		return fmt.Errorf("socket is required")
	}
	return nil
}

func sameConnectionTarget(left, right ConnectionTarget) bool {
	leftRuntime, rightRuntime := "", ""
	if left.RuntimeSpec != nil {
		leftRuntime = left.RuntimeSpec.RuntimeID
	}
	if right.RuntimeSpec != nil {
		rightRuntime = right.RuntimeSpec.RuntimeID
	}
	return left.Socket == right.Socket && left.SessionID == right.SessionID &&
		left.WorkspaceRoot == right.WorkspaceRoot && left.StateDir == right.StateDir &&
		leftRuntime == rightRuntime && left.AutoStart == right.AutoStart
}

func (p *preparedConnection) close() {
	if p == nil {
		return
	}
	if p.call != nil {
		_ = p.call.Close()
	}
	if p.stream != nil {
		_ = p.stream.Close()
	}
}

// Sender delivers messages into the running program from goroutines.
// *tea.Program satisfies it.
type Sender interface {
	Send(tea.Msg)
}

// attachBatch is the cursor checkpoint returned by session.attach. Cursor is
// the audit log length, not an event ID: clients must accept it moving
// backwards when the daemon clamps a cursor that is ahead of a compacted log.
type attachBatch struct {
	Events []map[string]any `json:"events"`
	From   int              `json:"from"`
	Cursor int              `json:"cursor"`
}

type taskSnapshot struct {
	TaskID         string   `json:"task_id"`
	Status         string   `json:"status"`
	CreatedAt      string   `json:"created_at"`
	Summary        string   `json:"summary"`
	AppliedPatches []string `json:"applied_patches"`
	TokensUsed     int      `json:"tokens_used"`
	Attempts       int      `json:"attempts"`
	Mode           string   `json:"mode"`
}

type completionTracker struct {
	active    map[string]bool
	pending   map[string]map[string]any
	pendingID []string
	delivered map[string]bool
}

type permissionTracker struct {
	pending   map[string]map[string]any
	pendingID []string
	opened    map[string]bool
	resolved  map[string]bool
}

type questionTracker struct {
	pending   map[string]map[string]any
	pendingID []string
	opened    map[string]bool
	resolved  map[string]bool
}

func newQuestionTracker() *questionTracker {
	return &questionTracker{
		pending:  make(map[string]map[string]any),
		opened:   make(map[string]bool),
		resolved: make(map[string]bool),
	}
}

func (t *questionTracker) observeAudit(ev map[string]any) {
	payload, _ := ev["payload"].(map[string]any)
	status, _ := payload["status"].(string)
	switch status {
	case "user_question_requested":
		request, _ := payload["request"].(map[string]any)
		questionID, _ := request["question_id"].(string)
		if questionID == "" || t.resolved[questionID] {
			return
		}
		if _, exists := t.pending[questionID]; !exists {
			t.pendingID = append(t.pendingID, questionID)
		}
		t.pending[questionID] = request
	case "user_question_resolved":
		questionID, _ := payload["question_id"].(string)
		if questionID != "" {
			t.resolved[questionID] = true
			delete(t.pending, questionID)
		}
	}
}

func (t *questionTracker) forwardTransient(ev map[string]any) bool {
	if typ, _ := ev["type"].(string); typ != "user.question" {
		return true
	}
	questionID, _ := ev["question_id"].(string)
	if questionID == "" {
		return true
	}
	if t.opened[questionID] || t.resolved[questionID] {
		return false
	}
	t.opened[questionID] = true
	return true
}

func (t *questionTracker) flush(p Sender, sessionID string, generation uint64) {
	for _, questionID := range t.pendingID {
		request, ok := t.pending[questionID]
		if !ok || t.opened[questionID] || t.resolved[questionID] {
			continue
		}
		p.Send(EventMsg{SessionID: sessionID, Generation: generation, Raw: request})
		t.opened[questionID] = true
	}
	t.pendingID = t.pendingID[:0]
}

// reconcile drops audit-log questions that are no longer blocked in the live
// daemon. This prevents a client from reopening a stale question after the
// daemon crashed between persisting the request and persisting its resolution.
func (t *questionTracker) reconcile(call Caller) {
	var result struct {
		QuestionIDs []string `json:"question_ids"`
	}
	if call == nil || call.Call("task.user.pending", map[string]any{}, &result) != nil {
		return
	}
	active := make(map[string]bool, len(result.QuestionIDs))
	for _, questionID := range result.QuestionIDs {
		active[questionID] = true
	}
	for questionID := range t.pending {
		if !active[questionID] {
			delete(t.pending, questionID)
			t.resolved[questionID] = true
		}
	}
}

func newPermissionTracker() *permissionTracker {
	return &permissionTracker{
		pending:  make(map[string]map[string]any),
		opened:   make(map[string]bool),
		resolved: make(map[string]bool),
	}
}

// observeAudit folds durable permission requests and their later resolution
// records by decision_id. Only requests still pending after the complete attach
// batch are reopened; an old resolved request must never recreate an overlay.
func (t *permissionTracker) observeAudit(ev map[string]any) {
	payload, _ := ev["payload"].(map[string]any)
	status, _ := payload["status"].(string)
	switch status {
	case "permission_requested":
		request, _ := payload["request"].(map[string]any)
		decisionID, _ := request["decision_id"].(string)
		if decisionID == "" || t.resolved[decisionID] {
			return
		}
		if _, exists := t.pending[decisionID]; !exists {
			t.pendingID = append(t.pendingID, decisionID)
		}
		t.pending[decisionID] = request
	case "approval_resolved":
		decisionID, _ := payload["decision_id"].(string)
		if decisionID == "" {
			decisionID, _ = ev["permission_decision_id"].(string)
		}
		if decisionID != "" {
			t.resolved[decisionID] = true
			delete(t.pending, decisionID)
		}
	}
}

func (t *permissionTracker) forwardTransient(ev map[string]any) bool {
	if typ, _ := ev["type"].(string); typ != "permission.request" {
		return true
	}
	decisionID, _ := ev["decision_id"].(string)
	if decisionID == "" {
		return true
	}
	if t.opened[decisionID] || t.resolved[decisionID] {
		return false
	}
	t.opened[decisionID] = true
	return true
}

func (t *permissionTracker) flush(p Sender, sessionID string, generation uint64) {
	for _, decisionID := range t.pendingID {
		request, ok := t.pending[decisionID]
		if !ok || t.opened[decisionID] || t.resolved[decisionID] {
			continue
		}
		p.Send(EventMsg{SessionID: sessionID, Generation: generation, Raw: request})
		t.opened[decisionID] = true
	}
	t.pendingID = t.pendingID[:0]
}

func newCompletionTracker() *completionTracker {
	return &completionTracker{
		active:    make(map[string]bool),
		pending:   make(map[string]map[string]any),
		delivered: make(map[string]bool),
	}
}

func terminalTaskStatus(status string) bool {
	switch status {
	case "completed", "degraded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func steerableTaskStatus(status string) bool {
	switch status {
	case "queued", "running", "waiting_approval":
		return true
	default:
		return false
	}
}

func completionFromTask(task taskSnapshot) map[string]any {
	return map[string]any{
		"type":            "task.completed",
		"task_id":         task.TaskID,
		"status":          task.Status,
		"summary":         task.Summary,
		"applied_patches": task.AppliedPatches,
		"tokens_used":     task.TokensUsed,
		"attempts":        task.Attempts,
		"mode":            task.Mode,
	}
}

// observeAudit tracks task liveness from durable events. Terminal audit
// events are retained until either the matching transient task.completed
// arrives or a reconnect proves it was lost and flushes the pending envelope.
func (t *completionTracker) observeAudit(ev map[string]any, trackTerminal bool) {
	taskID, _ := ev["task_id"].(string)
	if taskID == "" {
		return
	}
	// Audit payloads share generic status words. In particular,
	// ToolCallCompleted carries status=completed, but that only closes one tool
	// call; treating it as a task terminal event publishes an empty result early
	// and causes the real final answer to be discarded as a duplicate.
	typ, _ := ev["type"].(string)
	switch typ {
	case "TaskCreated", "TaskCompleted", "TaskFailed", "TaskCancelled", "TaskCanceled", "TaskInterrupted":
		// TaskCreated is the daemon's durable task-state journal; the explicit
		// terminal variants remain accepted for compatible event producers.
	default:
		return
	}
	payload, _ := ev["payload"].(map[string]any)
	status, _ := payload["status"].(string)
	if terminalTaskStatus(status) {
		delete(t.active, taskID)
		if trackTerminal && !t.delivered[taskID] {
			summary, _ := payload["summary"].(string)
			if summary == "" {
				summary, _ = payload["reason"].(string)
			}
			t.queue(taskID, map[string]any{
				"type":      "task.completed",
				"task_id":   taskID,
				"status":    status,
				"summary":   summary,
				"timestamp": ev["timestamp"],
			})
		}
		return
	}
	t.active[taskID] = true
}

func (t *completionTracker) reconcile(call Caller, sessionID string, reconnecting bool) string {
	var tasks []taskSnapshot
	if err := call.Call("task.list", map[string]any{"session_id": sessionID}, &tasks); err != nil {
		return "" // completion reconciliation is best-effort; event transport remains healthy
	}
	var latestActive taskSnapshot
	for _, task := range tasks {
		if steerableTaskStatus(task.Status) {
			t.active[task.TaskID] = true
			if latestActive.TaskID == "" || task.CreatedAt > latestActive.CreatedAt {
				latestActive = task
			}
			continue
		}
		if !terminalTaskStatus(task.Status) {
			delete(t.active, task.TaskID)
			continue
		}
		if reconnecting && t.active[task.TaskID] && !t.delivered[task.TaskID] {
			t.queue(task.TaskID, completionFromTask(task))
		}
		delete(t.active, task.TaskID)
		if !reconnecting {
			t.delivered[task.TaskID] = true // historical terminal task: baseline only
		}
	}
	return latestActive.TaskID
}

func (t *completionTracker) queue(taskID string, ev map[string]any) {
	if _, exists := t.pending[taskID]; !exists {
		t.pendingID = append(t.pendingID, taskID)
	}
	t.pending[taskID] = ev
}

func (t *completionTracker) forwardTransient(ev map[string]any) bool {
	if typ, _ := ev["type"].(string); typ != "task.completed" {
		return true
	}
	taskID, _ := ev["task_id"].(string)
	if taskID == "" {
		return true
	}
	if t.delivered[taskID] {
		return false
	}
	t.delivered[taskID] = true
	delete(t.active, taskID)
	delete(t.pending, taskID)
	return true
}

func (t *completionTracker) flush(p Sender, sessionID string, generation uint64) {
	for _, taskID := range t.pendingID {
		ev, ok := t.pending[taskID]
		if !ok {
			continue
		}
		if !t.delivered[taskID] {
			p.Send(EventMsg{SessionID: sessionID, Generation: generation, Raw: ev})
			t.delivered[taskID] = true
		}
		delete(t.pending, taskID)
	}
	t.pendingID = t.pendingID[:0]
}

func pullSessionEvents(call Caller, sessionID string, since int) (attachBatch, error) {
	var batch attachBatch
	if err := call.Call("session.attach", map[string]any{
		"session_id": sessionID,
		"since":      since,
	}, &batch); err != nil {
		return attachBatch{}, err
	}
	if batch.From < 0 || batch.Cursor < 0 || batch.From > batch.Cursor {
		return attachBatch{}, fmt.Errorf("invalid session.attach cursor: from=%d cursor=%d", batch.From, batch.Cursor)
	}
	return batch, nil
}

// durableStreamEvent reports whether an event is the live mirror of an audit
// event. The daemon's durable envelopes always carry actor+payload; transient
// UI control envelopes (permission.request and task.completed) do not.
//
// Durable stream notifications are wakeups only. Re-reading them through
// session.attach gives the client an authoritative cursor and avoids rendering
// the same event once from replay and once from the live stream.
func durableStreamEvent(ev map[string]any) bool {
	actor, _ := ev["actor"].(string)
	_, hasPayload := ev["payload"]
	return actor != "" && hasPayload
}

// backoff returns the delay before reconnect attempt n: exponential from 1s,
// capped at 30s.
func backoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	d := time.Second << (attempt - 1)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// Connect drives the daemon link on the spike's two-connection go/rpc
// pattern: one request/response connection for calls, one dedicated
// connection subscribed to session.events.stream, with program.Send from the
// stream goroutine. session.attach is the authoritative durable event source:
// it loads initial history, closes the attach/subscribe race, and catches up
// from the last cursor after a reconnect. Durable live notifications are
// treated as low-latency wakeups for another attach, while transient control
// events are forwarded directly. Every failure surfaces as a message (degrade
// banner); reconnects are attempted forever with visible attempt counts.
func Connect(p Sender, socket, sessionID, workspaceRoot string) {
	ConnectControlled(p, socket, sessionID, workspaceRoot, NewConnectionController())
}

func ConnectControlled(p Sender, socket, sessionID, workspaceRoot string, controller *ConnectionController) {
	connectControlled(p, ConnectionTarget{Socket: socket, SessionID: sessionID, WorkspaceRoot: workspaceRoot}, controller)
}

// ConnectControlledRuntime validates every call and stream connection against
// the stable workspace/runtime identity, including reconnects after restart.
func ConnectControlledRuntime(p Sender, socket, sessionID, workspaceRoot string, controller *ConnectionController, spec localruntime.Spec) {
	connectControlled(p, ConnectionTarget{
		Socket: socket, SessionID: sessionID, WorkspaceRoot: workspaceRoot,
		StateDir: spec.Paths.StateDir, RuntimeSpec: &spec,
	}, controller)
}

func dialConnectionTarget(target ConnectionTarget) (*rpc.Client, error) {
	if target.RuntimeSpec == nil {
		if target.AutoStart {
			return localdaemon.EnsureReachable(target.Socket)
		}
		return rpc.Dial(target.Socket)
	}
	spec := *target.RuntimeSpec
	if latest, err := localruntime.LoadSpec(spec.Paths.SpecPath); err == nil {
		if latest.Workspace.ID != spec.Workspace.ID || latest.RuntimeID != spec.RuntimeID {
			return nil, fmt.Errorf("runtime identity changed on disk")
		}
		spec = latest
	}
	var client *rpc.Client
	var err error
	if target.AutoStart {
		client, _, err = localdaemon.ConnectOrStart(spec)
	} else {
		client, _, err = localdaemon.Connect(spec)
	}
	return client, err
}

func latestWorkspaceSession(call *rpc.Client, workspaceRoot string) string {
	if call == nil {
		return ""
	}
	var sessions []struct {
		SessionID     string `json:"session_id"`
		WorkspaceRoot string `json:"workspace_root"`
		UpdatedAt     string `json:"updated_at"`
		CreatedAt     string `json:"created_at"`
		Status        string `json:"status"`
	}
	if err := call.Call("session.list", map[string]any{}, &sessions); err != nil {
		return ""
	}
	var latest, latestAt string
	for _, session := range sessions {
		if session.Status == "closed" || cleanWorkspaceRoot(session.WorkspaceRoot) != cleanWorkspaceRoot(workspaceRoot) {
			continue
		}
		at := session.UpdatedAt
		if at == "" {
			at = session.CreatedAt
		}
		if latest == "" || at > latestAt {
			latest, latestAt = session.SessionID, at
		}
	}
	return latest
}

func prepareConnection(target ConnectionTarget) (*preparedConnection, error) {
	call, err := dialConnectionTarget(target)
	if err != nil {
		return nil, err
	}
	initial, err := pullSessionEvents(call, target.SessionID, 0)
	if err != nil {
		_ = call.Close()
		return nil, fmt.Errorf("session attach: %w", err)
	}
	stream, err := dialConnectionTarget(target)
	if err != nil {
		_ = call.Close()
		return nil, err
	}
	if err := stream.Call("session.events.stream", map[string]any{"session_id": target.SessionID}, nil); err != nil {
		_ = call.Close()
		_ = stream.Close()
		return nil, err
	}
	gap, err := pullSessionEvents(call, target.SessionID, initial.Cursor)
	if err != nil {
		_ = call.Close()
		_ = stream.Close()
		return nil, fmt.Errorf("session attach after subscribe: %w", err)
	}
	return &preparedConnection{target: cloneConnectionTarget(target), call: call, stream: stream, initial: initial, gap: gap}, nil
}

func connectControlled(p Sender, initialTarget ConnectionTarget, controller *ConnectionController) {
	go func() {
		if controller == nil {
			controller = NewConnectionController(initialTarget)
		}
		target, generation := controller.targetState(initialTarget)
		sid := target.SessionID
		cursor := 0
		connectedOnce := false
		completions := newCompletionTracker()
		permissions := newPermissionTracker()
		questions := newQuestionTracker()
		for attempt := 0; ; attempt++ {
			desired, desiredGeneration := controller.targetState(initialTarget)
			if !sameConnectionTarget(desired, target) || desiredGeneration != generation {
				target, sid, generation, cursor, connectedOnce = desired, desired.SessionID, desiredGeneration, 0, false
				completions = newCompletionTracker()
				permissions = newPermissionTracker()
				questions = newQuestionTracker()
				attempt = 0
			}
			if attempt > 0 {
				p.Send(ReconnectingMsg{SessionID: sid, Generation: generation, Attempt: attempt})
				time.Sleep(backoff(attempt))
			}

			prepared := controller.takePrepared(generation)
			var call, stream *rpc.Client
			var initial, gap attachBatch
			var err error
			if prepared != nil {
				call, stream, initial, gap = prepared.call, prepared.stream, prepared.initial, prepared.gap
				cursor = gap.Cursor
			} else {
				call, err = dialConnectionTarget(target)
				if err != nil {
					if attempt == 0 {
						p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: err})
					}
					continue
				}
			}
			if sid == "" {
				sid = latestWorkspaceSession(call, target.WorkspaceRoot)
				if sid != "" {
					target.SessionID = sid
					controller.adoptCreated(sid)
				}
			}
			if sid == "" {
				ws := target.WorkspaceRoot
				if ws == "" {
					ws, _ = os.Getwd()
				}
				var out struct {
					SessionID string `json:"session_id"`
				}
				if err := call.Call("session.create", map[string]any{
					"workspace_root": ws,
					"profile":        "safe-edit",
				}, &out); err != nil {
					call.Close()
					p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: err})
					continue
				}
				sid = out.SessionID
				target.SessionID = sid
				controller.adoptCreated(sid)
			}

			if prepared == nil {
				// Pull before subscribing so a resumed session renders its history.
				initial, err = pullSessionEvents(call, sid, cursor)
				if err != nil {
					call.Close()
					p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: fmt.Errorf("session attach: %w", err)})
					continue
				}
				cursor = initial.Cursor
				stream, err = dialConnectionTarget(target)
				if err != nil {
					call.Close()
					p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: err})
					continue
				}
				if err := stream.Call("session.events.stream", map[string]any{"session_id": sid}, nil); err != nil {
					call.Close()
					stream.Close()
					p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: err})
					continue
				}
				gap, err = pullSessionEvents(call, sid, cursor)
				if err != nil {
					call.Close()
					stream.Close()
					p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: fmt.Errorf("session attach after subscribe: %w", err)})
					continue
				}
				cursor = gap.Cursor
			}
			controller.bind(stream)

			if attempt > 0 {
				p.Send(ConnRestoredMsg{SessionID: sid, Generation: generation})
			}
			readyTarget := cloneConnectionTarget(target)
			readyTarget.SessionID = sid
			p.Send(SessionReadyMsg{SessionID: sid, Generation: generation, Call: call, Target: readyTarget})
			for _, ev := range initial.Events {
				p.Send(EventMsg{SessionID: sid, Generation: generation, Raw: ev})
				completions.observeAudit(ev, connectedOnce)
				permissions.observeAudit(ev)
				questions.observeAudit(ev)
			}
			for _, ev := range gap.Events {
				p.Send(EventMsg{SessionID: sid, Generation: generation, Raw: ev})
				completions.observeAudit(ev, connectedOnce)
				permissions.observeAudit(ev)
				questions.observeAudit(ev)
			}
			if activeTaskID := completions.reconcile(call, sid, connectedOnce); activeTaskID != "" {
				p.Send(TaskActiveMsg{SessionID: sid, Generation: generation, TaskID: activeTaskID})
			}
			if connectedOnce {
				completions.flush(p, sid, generation)
			}
			permissions.flush(p, sid, generation)
			questions.reconcile(call)
			questions.flush(p, sid, generation)
			connectedOnce = true
			attempt = 0 // healthy link resets the retry budget

			for {
				method, params, err := stream.ReadNotification()
				if err != nil {
					desired, desiredGeneration := controller.targetState(initialTarget)
					if !sameConnectionTarget(desired, target) || desiredGeneration != generation {
						attempt = -1
					} else {
						p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: fmt.Errorf("event stream closed: %w", err)})
					}
					break
				}
				if method != "event" {
					continue
				}
				var ev map[string]any
				if json.Unmarshal(params, &ev) == nil {
					if !durableStreamEvent(ev) {
						if permissions.forwardTransient(ev) && questions.forwardTransient(ev) && completions.forwardTransient(ev) {
							p.Send(EventMsg{SessionID: sid, Generation: generation, Raw: ev})
						}
						continue
					}
					batch, attachErr := pullSessionEvents(call, sid, cursor)
					if attachErr != nil {
						p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: fmt.Errorf("session attach after event: %w", attachErr)})
						break
					}
					cursor = batch.Cursor
					for _, replayed := range batch.Events {
						p.Send(EventMsg{SessionID: sid, Generation: generation, Raw: replayed})
						completions.observeAudit(replayed, true)
						permissions.observeAudit(replayed)
						questions.observeAudit(replayed)
					}
					// A durable terminal event is authoritative even if the best-effort
					// transient task.completed envelope is delayed or lost. Flush its
					// synthesized result immediately after the durable batch so the TUI
					// clears the live rail and appends the summary at the transcript tail.
					completions.flush(p, sid, generation)
				}
			}
			call.Close()
			controller.unbind(stream)
			stream.Close()
		}
	}()
}
