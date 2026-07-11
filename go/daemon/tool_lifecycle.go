package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Nebutra/carina/go/artifact"
	"github.com/Nebutra/carina/go/runtimecontract"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

type toolExecutionOutcome struct {
	display       string
	status        string
	errorCategory string
	// mediaRefs carries content-addressed references to non-text payloads
	// (images) the tool produced; display holds only their textual
	// placeholders. The loop copies these onto the turn's Observation so a
	// vision-capable model can receive the bytes on later turns.
	mediaRefs []MediaRef
}

func toolCompleted(display string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "completed"}
}

func toolCompletedMedia(display string, refs ...MediaRef) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "completed", mediaRefs: refs}
}

func toolFailed(display, category string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "failed", errorCategory: category}
}

func toolDenied(display, category string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "denied", errorCategory: category}
}

func toolTimedOut(display string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "timed_out", errorCategory: "timeout"}
}

func classifyLegacyToolResult(display string) toolExecutionOutcome {
	trimmed := strings.TrimSpace(display)
	lower := strings.ToLower(trimmed)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "DENIED:") || strings.HasPrefix(upper, "BLOCKED") || strings.HasPrefix(lower, "requires approval") {
		return toolDenied(display, "tool_denied")
	}
	if strings.HasPrefix(lower, "error:") || strings.Contains(lower, " failed:") || strings.HasPrefix(lower, "unknown tool:") || strings.HasPrefix(lower, "memory error:") {
		return toolFailed(display, "tool_error")
	}
	return toolCompleted(display)
}

// MistakeTracker is a consecutive-failure circuit breaker over
// toolExecutionOutcome.status: it counts back-to-back non-"completed"
// outcomes ("failed", "denied", "timed_out", "cancelled") and reports a trip
// once the streak crosses MaxConsecutive, so a model that keeps hitting the
// same (or different) broken tool call doesn't burn the rest of its turn
// budget one failure at a time. Any "completed" outcome resets the streak —
// this tracks *consecutive* failures, not a lifetime total (that's
// LoopGuard's MaxHardRepeat's job, which fires on repeated identical
// actions regardless of outcome). Shape mirrors LoopGuard
// (go/daemon/transcript.go): a small struct with an observe-style method and
// a threshold field, fully unit-testable standalone.
type MistakeTracker struct {
	consecutive    int
	MaxConsecutive int
	lastCategory   string
}

func newMistakeTracker() *MistakeTracker {
	return &MistakeTracker{MaxConsecutive: 3}
}

// observe records one tool outcome and reports whether the consecutive
// non-completed streak has crossed MaxConsecutive (caller should treat this
// as a trip — e.g. degrade the task — rather than continue looping).
func (m *MistakeTracker) observe(outcome toolExecutionOutcome) bool {
	if outcome.status == "completed" {
		m.consecutive = 0
		m.lastCategory = ""
		return false
	}
	m.consecutive++
	m.lastCategory = outcome.errorCategory
	return m.tripped()
}

// tripped reports whether the current consecutive-failure streak has
// crossed MaxConsecutive without recording a new observation.
func (m *MistakeTracker) tripped() bool {
	return m.MaxConsecutive > 0 && m.consecutive >= m.MaxConsecutive
}

// reset clears the consecutive-failure streak (e.g. after a nudge the
// caller wants to give one more clean chance to before tripping again).
func (m *MistakeTracker) reset() {
	m.consecutive = 0
	m.lastCategory = ""
}

type toolCallLifecycle struct {
	id       string
	tool     string
	kind     string
	created  time.Time
	sequence *atomic.Int64
}

func (c toolCallLifecycle) nextSequence() int { return int(c.sequence.Add(1)) }

type activeToolCall struct {
	call           toolCallLifecycle
	sess           *sessionstore.Session
	task           *scheduler.Task
	started        bool
	terminalStatus string
}

func (d *Daemon) markActiveToolTerminal(taskID, status string) {
	d.activeToolCallMu.Lock()
	for callID := range d.activeToolCallsByTask[taskID] {
		if active := d.activeToolCalls[callID]; active != nil {
			active.terminalStatus = status
		}
	}
	d.activeToolCallMu.Unlock()
}

func (d *Daemon) activeToolTerminal(taskID string) string {
	d.activeToolCallMu.Lock()
	defer d.activeToolCallMu.Unlock()
	for callID := range d.activeToolCallsByTask[taskID] {
		if active := d.activeToolCalls[callID]; active != nil && active.terminalStatus != "" {
			return active.terminalStatus
		}
	}
	return ""
}

func (d *Daemon) installActiveToolCall(sess *sessionstore.Session, task *scheduler.Task, call toolCallLifecycle) {
	d.activeToolCallMu.Lock()
	d.activeToolCalls[call.id] = &activeToolCall{call: call, sess: sess, task: task}
	if d.activeToolCallsByTask[task.TaskID] == nil {
		d.activeToolCallsByTask[task.TaskID] = map[string]struct{}{}
	}
	d.activeToolCallsByTask[task.TaskID][call.id] = struct{}{}
	d.activeToolCallMu.Unlock()
}

func (d *Daemon) clearActiveToolCall(taskID, callID string) {
	d.activeToolCallMu.Lock()
	delete(d.activeToolCalls, callID)
	delete(d.activeToolCallsByTask[taskID], callID)
	if len(d.activeToolCallsByTask[taskID]) == 0 {
		delete(d.activeToolCallsByTask, taskID)
	}
	d.activeToolCallMu.Unlock()
}

func (d *Daemon) startInstalledToolCall(sess *sessionstore.Session, task *scheduler.Task, call toolCallLifecycle) error {
	d.activeToolCallMu.Lock()
	d.activeToolCallMu.Unlock()
	if err := d.startToolCall(sess, task, call); err != nil {
		return err
	}
	d.activeToolCallMu.Lock()
	if active := d.activeToolCalls[call.id]; active != nil {
		active.started = true
	}
	d.activeToolCallMu.Unlock()
	return nil
}

func (d *Daemon) ensureActiveToolStarted(taskID string) error {
	d.activeToolCallMu.Lock()
	var active *activeToolCall
	for callID := range d.activeToolCallsByTask[taskID] {
		if candidate := d.activeToolCalls[callID]; candidate != nil && !candidate.started {
			active = candidate
			break
		}
	}
	if active == nil || active.started {
		d.activeToolCallMu.Unlock()
		return nil
	}
	d.activeToolCallMu.Unlock()
	if err := d.startToolCall(active.sess, active.task, active.call); err != nil {
		return err
	}
	d.activeToolCallMu.Lock()
	if current := d.activeToolCalls[active.call.id]; current != nil {
		current.started = true
	}
	d.activeToolCallMu.Unlock()
	return nil
}

func (d *Daemon) ensureToolCallStarted(callID string) error {
	d.activeToolCallMu.Lock()
	active := d.activeToolCalls[callID]
	if active == nil || active.started {
		d.activeToolCallMu.Unlock()
		return nil
	}
	d.activeToolCallMu.Unlock()
	if err := d.startToolCall(active.sess, active.task, active.call); err != nil {
		return err
	}
	d.activeToolCallMu.Lock()
	if current := d.activeToolCalls[callID]; current != nil {
		current.started = true
	}
	d.activeToolCallMu.Unlock()
	return nil
}

func (d *Daemon) markActiveToolApprovalRequired(taskID, decisionID string) error {
	d.activeToolCallMu.Lock()
	var active *activeToolCall
	for callID := range d.activeToolCallsByTask[taskID] {
		if candidate := d.activeToolCalls[callID]; candidate != nil && !candidate.started {
			active = candidate
			break
		}
	}
	d.activeToolCallMu.Unlock()
	if active == nil {
		return nil
	}
	if err := d.recordStrict(active.sess.SessionID, "ToolCallApprovalRequired", active.task.TaskID, "go", map[string]any{
		"call_id": active.call.id, "tool": active.call.tool, "kind": active.call.kind,
		"status": "awaiting_approval", "decision_id": decisionID,
	}, decisionID); err != nil {
		return err
	}
	return d.recordRuntimeStageStrict(active.sess, active.task, active.call, active.call.nextSequence(), "tool.awaiting_approval", "awaiting_approval")
}

func (d *Daemon) beginToolCall(sess *sessionstore.Session, task *scheduler.Task, act *action) (toolCallLifecycle, error) {
	call := toolCallLifecycle{id: newToolCallID(), tool: act.Tool, kind: toolKind(act.Tool), created: time.Now().UTC(), sequence: &atomic.Int64{}}
	args, _ := json.Marshal(redactedToolArguments(act))
	env := runtimecontract.ToolCallEnvelope{CallID: call.id, SessionID: sess.SessionID, TaskID: task.TaskID, Tool: call.tool, Status: runtimecontract.ToolCallPending, Arguments: args, CreatedAt: call.created, UpdatedAt: call.created}
	if err := env.Validate(); err != nil {
		return call, err
	}
	if err := d.recordStrict(sess.SessionID, "ToolCallRequested", task.TaskID, "go", map[string]any{
		"call_id": call.id, "tool": call.tool, "kind": call.kind, "status": string(env.Status),
		"arguments": redactedToolArguments(act),
	}, ""); err != nil {
		return call, err
	}
	if err := d.recordRuntimeStageStrict(sess, task, call, call.nextSequence(), "tool.requested", "running"); err != nil {
		return call, err
	}
	return call, nil
}

func (d *Daemon) startToolCall(sess *sessionstore.Session, task *scheduler.Task, call toolCallLifecycle) error {
	now := time.Now().UTC()
	env := runtimecontract.ToolCallEnvelope{CallID: call.id, SessionID: sess.SessionID, TaskID: task.TaskID, Tool: call.tool, Status: runtimecontract.ToolCallRunning, CreatedAt: call.created, UpdatedAt: now}
	if err := env.Validate(); err != nil {
		return err
	}
	if err := d.recordStrict(sess.SessionID, "ToolCallStarted", task.TaskID, "go", map[string]any{
		"call_id": call.id, "tool": call.tool, "kind": call.kind, "status": "running",
	}, ""); err != nil {
		return err
	}
	return d.recordRuntimeStageStrict(sess, task, call, call.nextSequence(), "tool.executing", "running")
}

func (d *Daemon) finishToolCall(sess *sessionstore.Session, task *scheduler.Task, call toolCallLifecycle, outcome toolExecutionOutcome) error {
	eventType := map[string]string{
		"completed": "ToolCallCompleted", "failed": "ToolCallFailed", "denied": "ToolCallDenied",
		"cancelled": "ToolCallCancelled", "timed_out": "ToolCallFailed",
	}[outcome.status]
	if eventType == "" {
		eventType = "ToolCallFailed"
		outcome.status, outcome.errorCategory = "failed", "invalid_outcome"
	}
	payload := map[string]any{
		"call_id": call.id, "tool": call.tool, "kind": call.kind, "status": outcome.status,
		"artifact_ids": []string{},
	}
	outputMetadata := safeOutputMetadata(outcome.display)
	outputMetadata["artifact_status"] = "not_created"
	contractStatus := runtimecontract.ToolCallStatus(outcome.status)
	env := runtimecontract.ToolCallEnvelope{CallID: call.id, SessionID: sess.SessionID, TaskID: task.TaskID, Tool: call.tool, Status: contractStatus, CreatedAt: call.created, UpdatedAt: time.Now().UTC()}
	if outcome.display != "" && d.artifacts != nil {
		meta, err := d.artifacts.Put([]byte(outcome.display), artifact.PutOptions{
			Scope: artifact.Scope{SessionID: sess.SessionID, TaskID: task.TaskID, CallID: call.id},
			MediaType: "text/plain; charset=utf-8", Retention: artifact.RetentionNormal,
			// Bound the stored preview so a large command/tool output still
			// gets a head+tail-aware Metadata.Preview (see makePreview):
			// mirrors transcript.go's ToolOutputMax so the artifact-level
			// preview and the model-facing observation truncate at the same
			// scale. The preview itself never enters the audit payload below
			// (safeOutputMetadata stays hash-only) — it is only reachable by
			// a caller with scope access via Store.Read/artifact RPC.
			PreviewBytes: defaultCompactionPolicy().ToolOutputMax,
		})
		if err == nil {
			env.ArtifactIDs = []string{meta.ID}
			payload["artifact_ids"] = env.ArtifactIDs
			outputMetadata["artifact_id"] = meta.ID
			outputMetadata["scope"] = meta.Scope
			outputMetadata["artifact_status"] = "available"
			outputMetadata["artifact_truncated"] = meta.Truncated
		} else {
			outputMetadata["artifact_status"] = "unavailable"
			outputMetadata["artifact_error"] = artifactErrorCode(err)
		}
	}
	if outcome.status == "completed" {
		payload["output"] = outputMetadata
	} else {
		category := runtimecontract.ErrorInternal
		if outcome.status == "denied" {
			category = runtimecontract.ErrorPermission
		}
		env.Error = &runtimecontract.ErrorEnvelope{Code: "tool_" + outcome.status, Category: category, Message: "tool did not complete successfully", Retry: runtimecontract.NoRetry(), Metadata: safeErrorMetadata(outcome.display, outcome.errorCategory)}
		payload["error"] = env.Error
	}
	if err := env.Validate(); err != nil {
		payload["contract_error"] = err.Error()
	}
	if err := d.recordStrict(sess.SessionID, eventType, task.TaskID, "go", payload, ""); err != nil {
		return err
	}
	if err := d.recordRuntimeStageStrict(sess, task, call, call.nextSequence(), "tool."+outcome.status, outcome.status); err != nil {
		return fmt.Errorf("persist terminal runtime stage for %s: %w", call.id, err)
	}
	return nil
}

func artifactErrorCode(err error) string {
	switch {
	case errors.Is(err, artifact.ErrObjectTooLarge):
		return "object_too_large"
	case errors.Is(err, artifact.ErrQuotaExceeded):
		return "quota_exceeded"
	default:
		return "storage_error"
	}
}

func (d *Daemon) recordStrict(sessionID, eventType, taskID, actor string, payload map[string]any, decisionID string) error {
	if err := d.recordChecked(sessionID, eventType, taskID, actor, payload, decisionID); err != nil {
		return fmt.Errorf("persist %s: %w", eventType, err)
	}
	return nil
}

func (d *Daemon) recordRuntimeStageStrict(sess *sessionstore.Session, task *scheduler.Task, call toolCallLifecycle, sequence int, stage, status string) error {
	return d.recordStrict(sess.SessionID, "RuntimeStageChanged", task.TaskID, "go", map[string]any{
		"stage_id": call.id + ":" + fmt.Sprint(sequence), "stage": stage, "status": status,
		"sequence": sequence, "call_id": call.id, "tool": call.tool, "kind": call.kind,
	}, "")
}

func (d *Daemon) recordRuntimeStage(sess *sessionstore.Session, task *scheduler.Task, call toolCallLifecycle, sequence int, stage, status string) {
	_ = d.recordRuntimeStageStrict(sess, task, call, sequence, stage, status)
}

func safeOutputMetadata(output string) map[string]any {
	sum := sha256.Sum256([]byte(output))
	return map[string]any{"sha256": hex.EncodeToString(sum[:]), "bytes": len([]byte(output)), "redacted": true}
}

func safeErrorMetadata(display, category string) map[string]any {
	if category == "" {
		category = "execution_error"
	}
	sum := sha256.Sum256([]byte(display))
	return map[string]any{"category": category, "sha256": hex.EncodeToString(sum[:]), "redacted": true}
}

func toolKind(tool string) string {
	switch tool {
	case "read", "list", "search", "code.search", "code.symbols", "code.map", "code.def", "code.refs", "code.impact", "mcp_find":
		return "read"
	case "patch", "memory":
		return "write"
	case "run":
		return "command"
	case "spawn", "workflow", "best_of_n":
		return "delegation"
	case "mcp":
		return "mcp"
	case "ask_user":
		return "interaction"
	default:
		return "unknown"
	}
}

func redactedToolArguments(act *action) map[string]any {
	args := map[string]any{}
	switch act.Tool {
	case "read", "patch":
		args["path"] = act.Path
	case "search":
		args["pattern"] = act.Pattern
	case "run":
		args["argc"] = len(act.Command)
		if len(act.Command) > 0 {
			args["executable"] = act.Command[0]
		}
	case "spawn":
		args["agent"], args["task_count"] = act.Agent, max(1, len(act.Tasks))
	case "workflow":
		args["workflow"] = act.Workflow
	case "best_of_n":
		args["n"] = act.N
	case "mcp":
		args["mcp_server"], args["mcp_tool"] = act.MCPServer, act.MCPTool
		args["argument_keys"] = sortedMapKeys(act.Args)
	case "memory":
		args["target"] = act.Target
	case "ask_user":
		args["option_count"] = len(act.Options)
	case "code.search", "mcp_find":
		args["query"] = act.Query
	case "code.symbols", "code.def", "code.refs", "code.impact":
		args["name"] = act.Name
	}
	return args
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func newToolCallID() string {
	var random [12]byte
	if _, err := rand.Read(random[:]); err == nil {
		return "call_" + hex.EncodeToString(random[:])
	}
	return fmt.Sprintf("call_%d", time.Now().UTC().UnixNano())
}
