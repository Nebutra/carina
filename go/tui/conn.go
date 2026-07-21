package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/rpc"
)

// ConnectionController owns the mutable target of one TUI connection loop.
// Switching closes the current stream to interrupt ReadNotification; the loop
// then reconnects at cursor zero with fresh reconciliation trackers.
type ConnectionController struct {
	mu         sync.Mutex
	target     string
	generation uint64
	stream     *rpc.Client
}

func NewConnectionController() *ConnectionController { return &ConnectionController{} }

func (c *ConnectionController) Switch(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	c.mu.Lock()
	c.target = sessionID
	c.generation++
	stream := c.stream
	c.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
	return nil
}

func (c *ConnectionController) state(initial string) (string, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.target == "" {
		c.target = initial
		if c.generation == 0 {
			c.generation = 1
		}
	}
	return c.target, c.generation
}

func (c *ConnectionController) bind(stream *rpc.Client) {
	c.mu.Lock()
	c.stream = stream
	c.mu.Unlock()
}

func (c *ConnectionController) adoptCreated(sessionID string) {
	c.mu.Lock()
	if c.target == "" {
		c.target = sessionID
	}
	c.mu.Unlock()
}

func (c *ConnectionController) unbind(stream *rpc.Client) {
	c.mu.Lock()
	if c.stream == stream {
		c.stream = nil
	}
	c.mu.Unlock()
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
	go func() {
		if controller == nil {
			controller = NewConnectionController()
		}
		sid, generation := controller.state(sessionID)
		cursor := 0
		connectedOnce := false
		completions := newCompletionTracker()
		permissions := newPermissionTracker()
		questions := newQuestionTracker()
		for attempt := 0; ; attempt++ {
			desired, desiredGeneration := controller.state(sessionID)
			if desired != sid || desiredGeneration != generation {
				sid, generation, cursor, connectedOnce = desired, desiredGeneration, 0, false
				completions = newCompletionTracker()
				permissions = newPermissionTracker()
				questions = newQuestionTracker()
				attempt = 0
			}
			if attempt > 0 {
				p.Send(ReconnectingMsg{SessionID: sid, Generation: generation, Attempt: attempt})
				time.Sleep(backoff(attempt))
			}

			call, err := rpc.Dial(socket)
			if err != nil {
				if attempt == 0 {
					p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: err})
				}
				continue
			}
			if sid == "" {
				ws := workspaceRoot
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
				controller.adoptCreated(sid)
			}

			// Pull before subscribing so a resumed session renders its history.
			// Keep the batch buffered until the full call+stream link is ready, so
			// SessionReadyMsg remains the first message of a healthy connection.
			initial, err := pullSessionEvents(call, sid, cursor)
			if err != nil {
				call.Close()
				p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: fmt.Errorf("session attach: %w", err)})
				continue
			}
			cursor = initial.Cursor

			stream, err := rpc.Dial(socket)
			if err != nil {
				call.Close()
				p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: err})
				continue
			}
			controller.bind(stream)
			if err := stream.Call("session.events.stream", map[string]any{"session_id": sid}, nil); err != nil {
				call.Close()
				stream.Close()
				controller.unbind(stream)
				p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: err})
				continue
			}

			// An event may be appended after the initial attach and before the
			// subscription is installed. A second attach after subscribe closes
			// that gap. Events appended after this checkpoint wake the stream and
			// are pulled through the same cursor path below.
			gap, err := pullSessionEvents(call, sid, cursor)
			if err != nil {
				call.Close()
				stream.Close()
				p.Send(ConnLostMsg{SessionID: sid, Generation: generation, Err: fmt.Errorf("session attach after subscribe: %w", err)})
				continue
			}
			cursor = gap.Cursor

			if attempt > 0 {
				p.Send(ConnRestoredMsg{SessionID: sid, Generation: generation})
			}
			p.Send(SessionReadyMsg{SessionID: sid, Generation: generation, Call: call})
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
					desired, desiredGeneration := controller.state(sessionID)
					if desired != sid || desiredGeneration != generation {
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
