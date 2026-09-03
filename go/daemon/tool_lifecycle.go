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
	"unicode"

	"github.com/Nebutra/carina/go/artifact"
	"github.com/Nebutra/carina/go/continuity"
	"github.com/Nebutra/carina/go/runtimecontract"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

type toolExecutionOutcome struct {
	display          string
	status           string
	errorCategory    string
	observationError *ToolObservationError
	// mediaRefs carries content-addressed references to non-text payloads
	// (images) the tool produced; display holds only their textual
	// placeholders. The loop copies these onto the turn's Observation so a
	// vision-capable model can receive the bytes on later turns.
	mediaRefs []MediaRef
	// artifactIDs carries non-media outputs such as quarantined downloads.
	// Raw bytes never enter display, transcript, or audit payloads.
	artifactIDs []string
}

func toolCompleted(display string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "completed"}
}

func toolCompletedMedia(display string, refs ...MediaRef) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "completed", mediaRefs: refs}
}

func toolCompletedArtifacts(display string, ids ...string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "completed", artifactIDs: append([]string(nil), ids...)}
}

func toolFailed(display, category string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "failed", errorCategory: category, observationError: newToolObservationError("failed", category, display)}
}

func toolDenied(display, category string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "denied", errorCategory: category, observationError: newToolObservationError("denied", category, display)}
}

func toolTimedOut(display string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "timed_out", errorCategory: "timeout", observationError: newToolObservationError("timed_out", "timeout", display)}
}

func toolCancelled(display, category string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "cancelled", errorCategory: category, observationError: newToolObservationError("cancelled", category, display)}
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
	task           *scheduler.ExecutionRun
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

func (d *Daemon) hasActiveToolCall(taskID string) bool {
	d.activeToolCallMu.Lock()
	defer d.activeToolCallMu.Unlock()
	return len(d.activeToolCallsByTask[taskID]) > 0
}

func (d *Daemon) installActiveToolCall(sess *sessionstore.Session, task *scheduler.ExecutionRun, call toolCallLifecycle) {
	d.activeToolCallMu.Lock()
	d.activeToolCalls[call.id] = &activeToolCall{call: call, sess: sess, task: task}
	if d.activeToolCallsByTask[task.RunID] == nil {
		d.activeToolCallsByTask[task.RunID] = map[string]struct{}{}
	}
	d.activeToolCallsByTask[task.RunID][call.id] = struct{}{}
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

func (d *Daemon) startInstalledToolCall(sess *sessionstore.Session, task *scheduler.ExecutionRun, call toolCallLifecycle) error {
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
	if err := d.recordStrict(active.sess.SessionID, "ToolCallApprovalRequired", active.task.RunID, "go", map[string]any{
		"call_id": active.call.id, "tool": active.call.tool, "kind": active.call.kind,
		"status": "awaiting_approval", "decision_id": decisionID,
	}, decisionID); err != nil {
		return err
	}
	return d.recordRuntimeStageStrict(active.sess, active.task, active.call, active.call.nextSequence(), "tool.awaiting_approval", "awaiting_approval")
}

func (d *Daemon) beginToolCall(sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) (toolCallLifecycle, error) {
	call := toolCallLifecycle{id: newToolCallID(), tool: act.Tool, kind: toolKind(act.Tool), created: time.Now().UTC(), sequence: &atomic.Int64{}}
	redacted := redactedToolArguments(act)
	effect := continuity.ClassifyTool(act.Tool, effectArguments(act))
	args, _ := json.Marshal(redacted)
	env := runtimecontract.ToolCallEnvelope{CallID: call.id, SessionID: sess.SessionID, TaskID: task.RunID, Tool: call.tool, Status: runtimecontract.ToolCallPending, Arguments: args, CreatedAt: call.created, UpdatedAt: call.created}
	if err := env.Validate(); err != nil {
		return call, err
	}
	payload := map[string]any{
		"call_id": call.id, "tool": call.tool, "kind": call.kind, "status": string(env.Status),
		"arguments": redacted, "effect": effect,
	}
	if intent := normalizeToolIntent(act.Intent); intent != "" {
		payload["intent"] = intent
	}
	if err := d.recordStrict(sess.SessionID, "ToolCallRequested", task.RunID, "go", payload, ""); err != nil {
		return call, err
	}
	if err := d.recordRuntimeStageStrict(sess, task, call, call.nextSequence(), "tool.requested", "running"); err != nil {
		return call, err
	}
	return call, nil
}

func normalizeToolIntent(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	const maxRunes = 160
	runes := []rune(value)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes-1]) + "…"
	}
	return value
}

func effectArguments(act *action) map[string]any {
	args := map[string]any{}
	if act == nil {
		return args
	}
	if value, ok := act.Args["idempotency_key"]; ok {
		args["idempotency_key"] = value
	}
	return args
}

func (d *Daemon) startToolCall(sess *sessionstore.Session, task *scheduler.ExecutionRun, call toolCallLifecycle) error {
	now := time.Now().UTC()
	env := runtimecontract.ToolCallEnvelope{CallID: call.id, SessionID: sess.SessionID, TaskID: task.RunID, Tool: call.tool, Status: runtimecontract.ToolCallRunning, CreatedAt: call.created, UpdatedAt: now}
	if err := env.Validate(); err != nil {
		return err
	}
	if err := d.recordStrict(sess.SessionID, "ToolCallStarted", task.RunID, "go", map[string]any{
		"call_id": call.id, "tool": call.tool, "kind": call.kind, "status": "running",
	}, ""); err != nil {
		return err
	}
	return d.recordRuntimeStageStrict(sess, task, call, call.nextSequence(), "tool.executing", "running")
}

func (d *Daemon) finishToolCall(sess *sessionstore.Session, task *scheduler.ExecutionRun, call toolCallLifecycle, outcome toolExecutionOutcome) error {
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
		"artifact_ids": []string{}, "media_refs": []MediaRef{},
	}
	outputMetadata := safeOutputMetadata(outcome.display)
	outputMetadata["artifact_status"] = "not_created"
	contractStatus := runtimecontract.ToolCallStatus(outcome.status)
	env := runtimecontract.ToolCallEnvelope{CallID: call.id, SessionID: sess.SessionID, TaskID: task.RunID, Tool: call.tool, Status: contractStatus, CreatedAt: call.created, UpdatedAt: time.Now().UTC()}
	if outcome.display != "" && d.artifacts != nil {
		meta, err := d.artifacts.Put([]byte(outcome.display), artifact.PutOptions{
			Scope:     artifact.Scope{SessionID: sess.SessionID, TaskID: task.RunID, CallID: call.id},
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
	if len(outcome.mediaRefs) > 0 {
		payload["media_refs"] = outcome.mediaRefs
		ids := append([]string(nil), env.ArtifactIDs...)
		for _, ref := range outcome.mediaRefs {
			ids = append(ids, ref.ArtifactID)
		}
		env.ArtifactIDs = ids
		payload["artifact_ids"] = ids
	}
	if len(outcome.artifactIDs) > 0 {
		ids := append([]string(nil), env.ArtifactIDs...)
		seen := make(map[string]struct{}, len(ids)+len(outcome.artifactIDs))
		for _, id := range ids {
			seen[id] = struct{}{}
		}
		for _, id := range outcome.artifactIDs {
			if id == "" {
				continue
			}
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		env.ArtifactIDs = ids
		payload["artifact_ids"] = ids
	}
	if outcome.status == "completed" {
		payload["output"] = outputMetadata
	} else {
		category := runtimecontract.ErrorInternal
		if outcome.status == "denied" {
			category = runtimecontract.ErrorPermission
		}
		env.Error = &runtimecontract.ErrorEnvelope{Code: "tool_" + outcome.status, Category: category, Message: operatorFacingToolError(outcome.display), Retry: runtimecontract.NoRetry(), Metadata: safeErrorMetadata(outcome.display, outcome.errorCategory)}
		payload["error"] = env.Error
	}
	if err := env.Validate(); err != nil {
		payload["contract_error"] = err.Error()
	}
	if err := d.recordStrict(sess.SessionID, eventType, task.RunID, "go", payload, ""); err != nil {
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

func (d *Daemon) recordRuntimeStageStrict(sess *sessionstore.Session, task *scheduler.ExecutionRun, call toolCallLifecycle, sequence int, stage, status string) error {
	return d.recordStrict(sess.SessionID, "RuntimeStageChanged", task.RunID, "go", map[string]any{
		"stage_id": call.id + ":" + fmt.Sprint(sequence), "stage": stage, "status": status,
		"sequence": sequence, "call_id": call.id, "tool": call.tool, "kind": call.kind,
	}, "")
}

func (d *Daemon) recordRuntimeStage(sess *sessionstore.Session, task *scheduler.ExecutionRun, call toolCallLifecycle, sequence int, stage, status string) {
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

func operatorFacingToolError(display string) string {
	msg := strings.TrimSpace(display)
	for _, prefix := range []string{"error:", "ERROR:", "DENIED:"} {
		if strings.HasPrefix(msg, prefix) {
			msg = strings.TrimSpace(msg[len(prefix):])
			break
		}
	}
	if msg == "" {
		return "tool did not complete successfully"
	}
	return truncateUTF8Bytes(msg, 240)
}

func toolKind(tool string) string {
	descriptor, ok := defaultBuiltinTools.lookup(tool)
	if !ok {
		return "unknown"
	}
	return string(descriptor.Effect)
}

func redactedToolArguments(act *action) map[string]any {
	args := map[string]any{}
	switch act.Tool {
	case "read", "patch", "edit":
		args["path"] = act.Path
		if act.Tool == "read" && act.StartLine != nil && act.LineCount != nil {
			args["start_line"] = *act.StartLine
			args["line_count"] = *act.LineCount
		}
	case "search":
		args["pattern"] = act.Pattern
	case "git.status":
		args["limit"] = act.Limit
	case "git.diff":
		args["view"], args["path_count"] = act.GitView, len(act.Paths)
	case "git.log":
		args["revision"], args["max_commits"], args["path_count"] = act.GitRevision, act.MaxCommits, len(act.Paths)
	case "web.fetch":
		args["host"] = webFetchHost(act.URL)
	case "web.search":
		args["host"] = webSearchHost
		args["query"] = brief(act.Query, 80)
	case "browser.open":
		args["browser_id"], args["tab_id"], args["mode"] = act.BrowserID, act.TabID, act.WaitMode
		args["approved_origin_count"] = len(act.ApprovedOrigins)
		if host := webFetchHost(act.URL); host != "" {
			args["host"] = host
		}
	case "browser.snapshot", "browser.capture", "browser.close":
		args["browser_id"], args["tab_id"] = act.BrowserID, act.TabID
		if act.Tool == "browser.capture" {
			args["full_page"] = act.FullPage
		}
	case "browser.action":
		args["browser_id"], args["tab_id"] = act.BrowserID, act.TabID
		args["action_kind"] = browserActionKind(act.Action)
	case "browser.tabs":
		args["browser_id"], args["tab_id"], args["operation"] = act.BrowserID, act.TabID, act.TabOperation
		if host := webFetchHost(act.URL); host != "" {
			args["host"] = host
		}
	case "run":
		args["argc"] = len(act.Command)
		if len(act.Command) > 0 {
			args["executable"] = act.Command[0]
		}
	case "spawn":
		args["agent"], args["task_count"] = act.Agent, max(1, len(act.Tasks))
		args["background"] = act.Background
	case "job.list":
		args["status_count"], args["limit"], args["has_cursor"] = len(act.Statuses), act.Limit, act.Cursor != ""
	case "job.wait":
		args["job_count"], args["mode"], args["timeout_ms"] = len(act.JobIDs), act.WaitMode, act.TimeoutMS
	case "job.cancel":
		args["job_id"] = act.JobID
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
	case "todo", "update_plan":
		if items, ok := act.incomingChecklist(); ok {
			if normalized, err := coerceTodoItems(items); err == nil {
				args["todos"] = normalized
			}
		}
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
