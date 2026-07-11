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
}

const toolArtifactTTL = 30 * 24 * time.Hour

func toolCompleted(display string) toolExecutionOutcome {
	return toolExecutionOutcome{display: display, status: "completed"}
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

type toolCallLifecycle struct {
	id      string
	tool    string
	kind    string
	created time.Time
}

func (d *Daemon) beginToolCall(sess *sessionstore.Session, task *scheduler.Task, act *action) (toolCallLifecycle, error) {
	call := toolCallLifecycle{id: newToolCallID(), tool: act.Tool, kind: toolKind(act.Tool), created: time.Now().UTC()}
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
	if err := d.recordRuntimeStageStrict(sess, task, call, 1, "tool.requested", "running"); err != nil {
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
	return d.recordRuntimeStageStrict(sess, task, call, 2, "tool.executing", "running")
}

func (d *Daemon) finishToolCall(sess *sessionstore.Session, task *scheduler.Task, call toolCallLifecycle, outcome toolExecutionOutcome) {
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
		meta, err := d.artifacts.Put([]byte(outcome.display), artifact.PutOptions{Scope: artifact.Scope{SessionID: sess.SessionID, TaskID: task.TaskID, CallID: call.id}, MediaType: "text/plain; charset=utf-8", TTL: toolArtifactTTL})
		if err == nil {
			env.ArtifactIDs = []string{meta.ID}
			payload["artifact_ids"] = env.ArtifactIDs
			outputMetadata["artifact_id"] = meta.ID
			outputMetadata["scope"] = meta.Scope
			outputMetadata["artifact_status"] = "available"
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
	d.record(sess.SessionID, eventType, task.TaskID, "go", payload, "")
	d.recordRuntimeStage(sess, task, call, 3, "tool."+outcome.status, outcome.status)
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
	case "read", "list", "search", "code.search", "code.symbols", "code.map", "code.def", "code.refs", "code.impact":
		return "read"
	case "patch", "memory":
		return "write"
	case "run":
		return "command"
	case "spawn", "workflow":
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
	case "mcp":
		args["mcp_server"], args["mcp_tool"] = act.MCPServer, act.MCPTool
		args["argument_keys"] = sortedMapKeys(act.Args)
	case "memory":
		args["target"] = act.Target
	case "ask_user":
		args["option_count"] = len(act.Options)
	case "code.search":
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
