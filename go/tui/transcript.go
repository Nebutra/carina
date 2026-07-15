package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui/theme"
)

type presentationKind string

const (
	presentationAgent      presentationKind = "agent"
	presentationTool       presentationKind = "tool"
	presentationCommand    presentationKind = "command"
	presentationFile       presentationKind = "file"
	presentationContext    presentationKind = "context"
	presentationGovernance presentationKind = "governance"
	presentationSubagent   presentationKind = "subagent"
	presentationWorkflow   presentationKind = "workflow"
	presentationSystem     presentationKind = "system"
)

type presentationStatus int

const (
	statusNeutral presentationStatus = iota
	statusRunning
	statusSuccess
	statusFailure
	statusNeedsAuth
)

// eventPresentation is the typed, terminal-safe projection of one audit or
// transient event. It intentionally contains no generic JSON rendering path:
// unknown payloads get a compact system label instead of becoming the UI.
type eventPresentation struct {
	Key         string
	Kind        presentationKind
	KindLabel   string
	Status      presentationStatus
	Timestamp   string
	Title       string
	Summary     string
	Body        []string
	Depth       int
	Collapsible bool
	Collapsed   bool
	OpenLabel   string
	FoldLabel   string
}

// entry caches its rendered form. Typed entries retain their presentation so
// a resize or explicit fold toggle only re-renders the affected projection.
type entry struct {
	key          string
	rendered     string
	presentation *eventPresentation
}

type transcript struct {
	entries []entry
	lines   []string
}

func (t *transcript) push(rendered string) {
	t.entries = append(t.entries, entry{rendered: rendered})
	t.lines = append(t.lines, strings.Split(rendered, "\n")...)
}

func (t *transcript) pushPresentation(p eventPresentation, th theme.Theme, width int) {
	pCopy := p
	if pCopy.Key != "" {
		for i := range t.entries {
			if t.entries[i].key != pCopy.Key {
				continue
			}
			// Preserve the operator's fold choice while lifecycle updates replace
			// the semantic state of the same authoritative call.
			if old := t.entries[i].presentation; old != nil && old.Collapsible && pCopy.Collapsible {
				pCopy.Collapsed = old.Collapsed
			}
			t.entries[i].presentation = &pCopy
			t.entries[i].rendered = pCopy.render(th, width)
			t.rebuildLines()
			return
		}
	}
	t.entries = append(t.entries, entry{
		key:          pCopy.Key,
		presentation: &pCopy,
		rendered:     pCopy.render(th, width),
	})
	t.rebuildLines()
}

func (t *transcript) resizePresentations(th theme.Theme, width int) {
	changed := false
	for i := range t.entries {
		if p := t.entries[i].presentation; p != nil {
			t.entries[i].rendered = p.render(th, width)
			changed = true
		}
	}
	if changed {
		t.rebuildLines()
	}
}

func (t *transcript) toggleLastCollapsible(th theme.Theme, width int) bool {
	for i := len(t.entries) - 1; i >= 0; i-- {
		p := t.entries[i].presentation
		if p == nil || !p.Collapsible {
			continue
		}
		p.Collapsed = !p.Collapsed
		t.entries[i].rendered = p.render(th, width)
		t.rebuildLines()
		return true
	}
	return false
}

func (t *transcript) rebuildLines() {
	t.lines = t.lines[:0]
	for _, e := range t.entries {
		t.lines = append(t.lines, strings.Split(e.rendered, "\n")...)
	}
}

// Status glyphs use the documented ASCII fallback under Mono/NO_COLOR.
func glyphOK(th theme.Theme) string {
	if th.Profile() == theme.Mono {
		return "+"
	}
	return th.Style(theme.RoleSuccess).Render("✓")
}

func glyphNeedsAuth(th theme.Theme) string {
	if th.Profile() == theme.Mono {
		return "!"
	}
	return th.Style(theme.RoleWarning).Render("⚿")
}

func glyphFailed(th theme.Theme) string {
	if th.Profile() == theme.Mono {
		return "x"
	}
	return th.Style(theme.RoleError).Render("✗")
}

func glyphNeutral(th theme.Theme) string {
	if th.Profile() == theme.Mono {
		return "-"
	}
	return th.Style(theme.RoleMuted).Render("·")
}

func glyphRunning(th theme.Theme) string {
	if th.Profile() == theme.Mono {
		return ">"
	}
	return th.Style(theme.RoleInfo).Render("›")
}

func (p eventPresentation) render(th theme.Theme, width int) string {
	if width < 1 {
		width = 1
	}
	muted := th.Style(theme.RoleMuted)
	title := th.Style(theme.RoleTitle)
	glyph := glyphNeutral(th)
	switch p.Status {
	case statusRunning:
		glyph = glyphRunning(th)
	case statusSuccess:
		glyph = glyphOK(th)
	case statusFailure:
		glyph = glyphFailed(th)
	case statusNeedsAuth:
		glyph = glyphNeedsAuth(th)
	}
	indent := strings.Repeat("  ", maxInt(p.Depth, 0))
	kind := p.KindLabel
	if kind == "" {
		kind = string(p.Kind)
	}
	header := strings.TrimSpace(strings.Join(nonEmpty(p.Timestamp, glyph, kind, p.Title), " "))
	if p.Summary != "" {
		header += " " + p.Summary
	}
	if p.Collapsible && len(p.Body) > 0 {
		open := p.OpenLabel
		if open == "" {
			open = "open"
		}
		fold := "[" + open + "]"
		if p.Collapsed {
			label := p.FoldLabel
			if label == "" {
				label = "+{count}"
			}
			fold = "[" + strings.ReplaceAll(label, "{count}", fmt.Sprintf("%d", len(p.Body))) + "]"
		}
		header += " " + muted.Render(fold)
	}
	lines := []string{fitLine(indent+title.Render(header), width)}
	if !p.Collapsed {
		for _, raw := range p.Body {
			for _, line := range strings.Split(sanitize(raw), "\n") {
				lines = append(lines, fitLine(indent+"  "+line, width))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func nonEmpty(parts ...string) []string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			out = append(out, part)
		}
	}
	return out
}

func presentationTimestamp(ev map[string]any) string {
	ts := str(ev["timestamp"])
	if len(ts) >= 19 {
		return ts[11:19]
	}
	return ts
}

func presentEvent(ev map[string]any, th theme.Theme, locale string) eventPresentation {
	typ := str(ev["type"])
	payload, _ := ev["payload"].(map[string]any)
	p := eventPresentation{
		Kind:      presentationSystem,
		Status:    statusNeutral,
		Timestamp: presentationTimestamp(ev),
		Title:     typ,
	}

	switch typ {
	case "ToolCallRequested", "ToolCallApprovalRequired", "ToolCallStarted", "ToolCallCompleted", "ToolCallFailed", "ToolCallDenied", "ToolCallCancelled":
		p = presentAuthoritativeToolCall(p, typ, payload)
	case "RuntimeStageChanged":
		p.Kind, p.Title = presentationSystem, "runtime"
		p.Status = lifecycleStatus(firstValue(payload, "status", "stage"))
		p.Summary = joinValues(payload, "stage", "status", "tool", "call_id")
		if callID := str(payload["call_id"]); callID != "" {
			p.Key = "stage:" + callID
		}
	case "permission.request":
		p.Kind, p.Status, p.Title = presentationGovernance, statusNeedsAuth, "approval "+str(ev["decision_id"])
		p.Summary = microcopy.Governed(microcopy.GovernedApprovalRequired, microcopy.Args{
			"action":      str(ev["capability"]),
			"path":        str(ev["resource"]),
			"decision_id": str(ev["decision_id"]),
		}, microcopy.WithLocale(locale))
	case "user.question":
		p.Kind, p.Status, p.Title = presentationGovernance, statusNeedsAuth, "question"
		p.Summary = truncate(str(ev["prompt"]), 160)
	case "task.completed":
		p.Kind, p.Title = presentationAgent, "task"
		status := str(ev["status"])
		p.Status = terminalPresentationStatus(status)
		p.Summary = strings.TrimSpace(str(ev["task_id"]) + " " + status)
		if summary := str(ev["summary"]); summary != "" {
			p.Body = []string{summary}
			p.Collapsible = true
			p.Collapsed = false
		}
	case "MemoryProjectionChanged":
		status := strings.ToLower(str(payload["status"]))
		if status == "failed" || status == "reconcile" {
			p.Kind, p.Status, p.Title = presentationGovernance, statusNeedsAuth, "memory sync"
			p.Summary = "Memory sync needs action. Run `carina memory status <session_id>` for the exact recovery command."
			p.Key = "memory-projection:" + str(payload["document_id"]) + ":" + status
		} else {
			p.Summary = joinValues(payload, "target", "status")
		}
	case "ModelRequested", "RoutingDecision", "RoutingOutcome":
		p.Kind, p.Status, p.Title = presentationAgent, statusRunning, "model"
		p.Summary = firstValue(payload, "status", "requested_model", "model", "reasoner")
	case "ModelResponded":
		p = presentModelEvent(p, payload)
	case "ToolRequested", "ToolApproved", "ToolDenied":
		p = presentToolEvent(p, typ, payload)
	case "CommandStarted", "CommandOutput", "CommandExited", "CommandExecuted":
		p = presentCommandEvent(p, typ, payload)
	case "FileRead", "FileWriteProposed", "PatchProposed", "PatchApplied", "PatchFailed", "RollbackStarted", "RollbackCompleted":
		p = presentFileEvent(p, typ, payload)
	case "ContextCompacted":
		p.Kind, p.Status, p.Title = presentationContext, statusSuccess, "context compacted"
		p.Summary = firstValue(payload, "summary", "receipt")
	case "PolicyViolation", "NetworkRequested", "SecretRequested":
		p.Kind, p.Title = presentationGovernance, strings.ToLower(typ)
		p.Status = statusFailure
		p.Summary = joinValues(payload, "capability", "resource", "host", "method", "policy_id")
	case "TaskCreated":
		p = presentTaskEvent(p, ev, payload)
	default:
		// Keep unknown event types legible without dumping their payload.
		p.Summary = firstValue(payload, "status", "command", "path", "reason", "error")
	}

	p.Summary = sanitize(p.Summary)
	localizePresentation(&p, newLocalizer(locale))
	return p
}

func localizePresentation(p *eventPresentation, l localizer) {
	p.OpenLabel = l.Text(MsgTranscriptOpen, nil)
	p.FoldLabel = l.Text(MsgTranscriptCollapsed, MessageArgs{"count": "{count}"})
	switch p.Kind {
	case presentationAgent:
		p.KindLabel = l.Text(MsgTranscriptAgent, nil)
	case presentationTool:
		p.KindLabel = l.Text(MsgTranscriptTool, nil)
	case presentationCommand:
		p.KindLabel = l.Text(MsgTranscriptCommand, nil)
	case presentationFile:
		p.KindLabel = l.Text(MsgTranscriptKindFile, nil)
	case presentationContext:
		p.KindLabel = l.Text(MsgTranscriptKindContext, nil)
	case presentationGovernance:
		p.KindLabel = l.Text(MsgTranscriptKindGovernance, nil)
	case presentationSubagent:
		p.KindLabel = l.Text(MsgTranscriptSubagent, nil)
	case presentationWorkflow:
		p.KindLabel = l.Text(MsgTranscriptWorkflow, nil)
	default:
		p.KindLabel = l.Text(MsgTranscriptKindSystem, nil)
	}
	switch p.Title {
	case "runtime":
		p.Title = l.Text(MsgTranscriptRuntime, nil)
	case "question":
		p.Title = l.Text(MsgTranscriptQuestion, nil)
	case "task":
		p.Title = l.Text(MsgTranscriptTask, nil)
	case "model":
		p.Title = l.Text(MsgTranscriptModel, nil)
	case "context compacted":
		p.Title = l.Text(MsgTranscriptContextCompacted, nil)
	case "tool":
		p.Title = l.Text(MsgTranscriptTool, nil)
	case "workflow":
		p.Title = l.Text(MsgTranscriptWorkflow, nil)
	case "subagent":
		p.Title = l.Text(MsgTranscriptSubagent, nil)
	case "agent":
		p.Title = l.Text(MsgTranscriptAgent, nil)
	case "step":
		p.Title = l.Text(MsgTranscriptStep, nil)
	case "command":
		p.Title = l.Text(MsgTranscriptCommand, nil)
	case "output":
		p.Title = l.Text(MsgTranscriptOutput, nil)
	}
	if strings.HasPrefix(p.Title, "approval ") {
		p.Title = l.Text(MsgTranscriptApproval, MessageArgs{"id": strings.TrimPrefix(p.Title, "approval ")})
	}
	switch {
	case p.Summary == "completed":
		p.Summary = l.Text(MsgTranscriptCompleted, nil)
	case p.Summary == "response received":
		p.Summary = l.Text(MsgTranscriptResponseReceived, nil)
	case strings.HasPrefix(p.Summary, "selected "):
		p.Summary = l.Text(MsgTranscriptSelected, MessageArgs{"tool": strings.TrimPrefix(p.Summary, "selected ")})
	case strings.HasSuffix(p.Summary, " started"):
		p.Summary = l.Text(MsgTranscriptStarted, MessageArgs{"agent": strings.TrimSuffix(p.Summary, " started")})
	case strings.HasPrefix(p.Summary, "exit "):
		p.Summary = l.Text(MsgTranscriptExit, MessageArgs{"code": strings.TrimPrefix(p.Summary, "exit ")})
	}
	for i, line := range p.Body {
		switch {
		case strings.HasPrefix(line, "artifact: "):
			p.Body[i] = l.Text(MsgTranscriptArtifact, MessageArgs{"ids": strings.TrimPrefix(line, "artifact: ")})
		case line == "open: carina artifact read <session_id> <artifact_id>":
			p.Body[i] = l.Text(MsgTranscriptOpenArtifact, nil)
		}
	}
}

func presentAuthoritativeToolCall(p eventPresentation, typ string, payload map[string]any) eventPresentation {
	p.Kind, p.Title = presentationTool, "tool"
	callID := str(payload["call_id"])
	if callID != "" {
		p.Key = "tool:" + callID
	}
	p.Summary = joinValues(payload, "tool", "kind", "status")
	p.Status = statusRunning
	switch typ {
	case "ToolCallApprovalRequired":
		p.Status = statusNeedsAuth
	case "ToolCallCompleted":
		p.Status = statusSuccess
	case "ToolCallFailed", "ToolCallDenied", "ToolCallCancelled":
		p.Status = statusFailure
	}
	p.Body = selectedBody(payload, "call_id", "reason", "error", "duration_ms")
	if ids := valueString(payload["artifact_ids"]); ids != "" {
		p.Body = append(p.Body, "artifact: "+ids, "open: carina artifact read <session_id> <artifact_id>")
	}
	p.Collapsible = len(p.Body) > 0
	p.Collapsed = len(p.Body) > 0
	return p
}

func presentTaskEvent(p eventPresentation, ev, payload map[string]any) eventPresentation {
	status := str(payload["status"])
	switch {
	case str(payload["workflow"]) != "" || strings.HasPrefix(status, "workflow_"):
		p.Kind, p.Depth, p.Title = presentationWorkflow, 1, "workflow"
		p.Summary = strings.TrimSpace(str(payload["workflow"]) + " " + strings.TrimPrefix(status, "workflow_"))
		p.Status = lifecycleStatus(status)
	case status == "context_compressed" || status == "context_retrieved" || status == "context_engine_failed":
		p.Kind, p.Title = presentationContext, strings.ReplaceAll(status, "_", " ")
		p.Status = lifecycleStatus(status)
		p.Summary = joinValues(payload, "engine", "tool", "savings_percent", "error")
	case status == "permission_requested" || status == "approval_resolved" || status == "risk_review":
		p.Kind, p.Title = presentationGovernance, strings.ReplaceAll(status, "_", " ")
		p.Status = lifecycleStatus(status)
		p.Summary = joinValues(payload, "capability", "resource", "decision_id")
	case status != "":
		p.Kind, p.Title = presentationAgent, "task"
		p.Status = lifecycleStatus(status)
		p.Summary = strings.ReplaceAll(status, "_", " ")
		p.Body = selectedBody(payload, "message", "summary", "reason", "error", "diagnostics")
		p.Collapsible = len(p.Body) > 0
		p.Collapsed = len(p.Body) > 0
	default:
		p.Kind, p.Status, p.Title = presentationAgent, statusRunning, "task"
		p.Summary = str(ev["task_id"])
		if prompt := str(payload["user_prompt"]); prompt != "" {
			p.Body = []string{prompt}
			p.Collapsible, p.Collapsed = true, true
		}
	}
	return p
}

func presentModelEvent(p eventPresentation, payload map[string]any) eventPresentation {
	status := str(payload["status"])
	if str(payload["spawn_agent"]) != "" {
		p.Kind, p.Depth, p.Title = presentationSubagent, 1, "subagent"
		p.Status = statusSuccess
		p.Summary = str(payload["spawn_agent"]) + " completed"
		if s := str(payload["result_summary"]); s != "" {
			p.Body, p.Collapsible, p.Collapsed = []string{s}, true, true
		}
		return p
	}
	if strings.HasPrefix(status, "workflow_") {
		p.Kind, p.Depth, p.Title = presentationWorkflow, 1, "workflow"
		p.Status = lifecycleStatus(status)
		p.Summary = strings.TrimSpace(str(payload["workflow"]) + " " + strings.TrimPrefix(status, "workflow_"))
		return p
	}
	p.Kind, p.Status, p.Title = presentationAgent, statusSuccess, "agent"
	text := str(payload["text"])
	tool, summary, body := safeModelAction(text)
	if tool == "done" {
		p.Summary = "completed"
	} else if tool != "" {
		p.Status = statusRunning
		p.Summary = "selected " + tool
	} else {
		p.Summary = "response received"
	}
	if summary != "" {
		p.Body = append(p.Body, summary)
	}
	p.Body = append(p.Body, body...)
	p.Collapsible = len(p.Body) > 0
	p.Collapsed = tool != "done" && len(p.Body) > 0
	return p
}

// safeModelAction deliberately omits "thought" and unknown free-form model
// text. The TUI displays actions, summaries, and observable execution state,
// never hidden chain-of-thought.
func safeModelAction(raw string) (tool, summary string, body []string) {
	raw = strings.TrimSpace(ansi.Strip(raw))
	start, end := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return "", "", nil
	}
	var action map[string]any
	if json.Unmarshal([]byte(raw[start:end+1]), &action) != nil {
		return "", "", nil
	}
	tool = str(action["tool"])
	summary = str(action["summary"])
	for _, key := range []string{"path", "pattern", "query", "name", "mcp_server", "mcp_tool", "workflow", "agent", "task"} {
		if value := str(action[key]); value != "" {
			body = append(body, key+": "+value)
		}
	}
	if command, ok := action["command"].([]any); ok {
		parts := make([]string, 0, len(command))
		for _, value := range command {
			if s := str(value); s != "" {
				parts = append(parts, s)
			}
		}
		if len(parts) > 0 {
			body = append(body, "$ "+strings.Join(parts, " "))
		}
	}
	return tool, summary, body
}

func presentToolEvent(p eventPresentation, typ string, payload map[string]any) eventPresentation {
	p.Kind, p.Title = presentationTool, "tool"
	p.Summary = firstValue(payload, "tool", "spawn_agent", "step", "command")
	p.Status = statusRunning
	switch typ {
	case "ToolApproved":
		p.Status = statusSuccess
	case "ToolDenied":
		p.Status = statusFailure
	}
	if agent := str(payload["spawn_agent"]); agent != "" {
		p.Kind, p.Depth, p.Title = presentationSubagent, 1, "subagent"
		p.Summary = agent + " started"
		p.Body = selectedBody(payload, "task", "child_session", "child_profile")
		p.Collapsible, p.Collapsed = len(p.Body) > 0, true
	} else if workflow := str(payload["workflow"]); workflow != "" {
		p.Kind, p.Depth, p.Title = presentationWorkflow, 2, "step"
		p.Summary = strings.TrimSpace(str(payload["step"]) + " " + str(payload["agent"]))
	}
	return p
}

func presentCommandEvent(p eventPresentation, typ string, payload map[string]any) eventPresentation {
	p.Kind, p.Title = presentationCommand, "command"
	p.Status = statusRunning
	switch typ {
	case "CommandStarted", "CommandExecuted":
		p.Summary = firstValue(payload, "command")
		if typ == "CommandExecuted" {
			p.Title = typ
		}
	case "CommandOutput":
		p.Title = firstValue(payload, "stream")
		if p.Title == "" {
			p.Title = "output"
		}
		chunk := str(payload["chunk"])
		p.Summary = truncate(firstLine(chunk), 100)
		p.Body = strings.Split(sanitize(chunk), "\n")
		p.Collapsible, p.Collapsed = len(p.Body) > 0, true
	case "CommandExited":
		code := valueString(payload["exit_code"])
		p.Summary = "exit " + code
		p.Status = statusSuccess
		if code != "" && code != "0" {
			p.Status = statusFailure
		}
	}
	return p
}

func presentFileEvent(p eventPresentation, typ string, payload map[string]any) eventPresentation {
	p.Kind, p.Title = presentationFile, strings.ToLower(strings.ReplaceAll(typ, "Patch", "patch "))
	p.Status = statusRunning
	switch typ {
	case "FileRead", "PatchApplied", "RollbackCompleted":
		p.Status = statusSuccess
	case "PatchFailed":
		p.Status = statusFailure
	}
	p.Summary = joinValues(payload, "path", "patch_id", "affected_files", "error")
	if diff := str(payload["diff"]); diff != "" {
		p.Body = strings.Split(diff, "\n")
		p.Collapsible, p.Collapsed = true, true
	}
	return p
}

func lifecycleStatus(status string) presentationStatus {
	switch {
	case strings.Contains(status, "failed"), strings.Contains(status, "denied"), strings.Contains(status, "degraded"), strings.Contains(status, "rejected"):
		return statusFailure
	case strings.Contains(status, "completed"), strings.Contains(status, "resolved"), strings.Contains(status, "applied"), strings.Contains(status, "retrieved"), strings.Contains(status, "compressed"):
		return statusSuccess
	case strings.Contains(status, "approval"), strings.Contains(status, "question"), strings.Contains(status, "review"):
		return statusNeedsAuth
	default:
		return statusRunning
	}
}

func terminalPresentationStatus(status string) presentationStatus {
	if status == "completed" {
		return statusSuccess
	}
	return statusFailure
}

func selectedBody(payload map[string]any, keys ...string) []string {
	var out []string
	for _, key := range keys {
		if value := valueString(payload[key]); value != "" {
			out = append(out, key+": "+value)
		}
	}
	return out
}

func firstValue(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := valueString(payload[key]); value != "" {
			return value
		}
	}
	return ""
}

func joinValues(payload map[string]any, keys ...string) string {
	var out []string
	for _, key := range keys {
		if value := valueString(payload[key]); value != "" {
			out = append(out, value)
		}
	}
	return strings.Join(out, " · ")
}

func valueString(v any) string {
	switch value := v.(type) {
	case string:
		return sanitize(value)
	case float64:
		return fmt.Sprintf("%g", value)
	case int:
		return fmt.Sprintf("%d", value)
	case bool:
		return fmt.Sprintf("%t", value)
	case []string:
		return strings.Join(value, ", ")
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			if s := valueString(item); s != "" {
				parts = append(parts, s)
			}
		}
		sort.Strings(parts)
		return strings.Join(parts, ", ")
	default:
		return ""
	}
}

// renderEvent remains the shared CLI/test projection. The interactive model
// stores the typed presentation so it can fold and resize it.
func renderEvent(ev map[string]any, th theme.Theme, locale string) string {
	return presentEvent(ev, th, locale).render(th, 240)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func sanitize(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' {
			return r
		}
		return -1
	}, s)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func fitLine(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = strings.ReplaceAll(sanitize(s), "\n", " ")
	if ansi.StringWidth(s) <= width {
		return s
	}
	if width == 1 {
		return ansi.Truncate(s, 1, "")
	}
	return ansi.Truncate(s, width, "…")
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(sanitize(s), "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
