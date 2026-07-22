package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui/markdown"
	"github.com/Nebutra/carina/go/tui/mathimage"
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
	Key       string
	Kind      presentationKind
	KindLabel string
	Status    presentationStatus
	Timestamp string
	Title     string
	Summary   string
	Body      []string
	// BodyProse marks free-form body text — task summaries, prompts, model
	// action summaries — which soft-wraps to the transcript width via
	// wrapText. Structured bodies (diff hunks, command output chunks,
	// tool-call status rows) stay on the fitLine clipping path so their
	// line-oriented alignment survives.
	BodyProse bool
	// BodyMarkdown carries the sanitized markdown source of final assistant
	// prose (the "done" action's response/plan text). It is rendered through
	// go/tui/markdown at render() time, so resize and profile changes re-render
	// from source; Body keeps the structured action rows.
	BodyMarkdown string
	// ImageData is verified artifact content rendered through terminal-native
	// graphics. It never passes through the text sanitizer or audit payload.
	ImageKey  string
	ImageData []byte
	// Headerless marks a body-only continuation entry: committed stable chunks
	// and the mutable tail of a streaming assistant message (stream.go) render
	// under the header the stream's head entry already emitted, so they carry
	// no header line of their own.
	Headerless bool
	// LeadingBlank prepends the block-separator blank line a whole-source
	// markdown render would have put before this chunk. The stream manager
	// sets it on every chunk after the first so a streamed message reproduces
	// the exact line layout of a one-shot render.
	LeadingBlank bool
	Depth        int
	Collapsible  bool
	Collapsed    bool
	OpenLabel    string
	FoldLabel    string
}

// entry caches its rendered form. Typed entries retain their presentation so
// a resize or explicit fold toggle only re-renders the affected projection.
type entry struct {
	key          string
	rendered     string
	lines        []string // rendered split once; rebuildLines concatenates
	presentation *eventPresentation
}

// setRendered caches the rendered block and its line split together.
// rebuildLines runs on every transcript mutation — streaming makes that per
// delta — so the per-entry strings.Split must happen once per render, not
// once per rebuild.
func (e *entry) setRendered(rendered string) {
	e.rendered = rendered
	e.lines = strings.Split(rendered, "\n")
}

type transcript struct {
	entries []entry
	lines   []string
	// renderedTheme/renderedWidth are what every presentation was last
	// rendered against. layout() calls resizePresentations on every keystroke
	// and event; unless one of them actually changed, re-rendering the whole
	// transcript (chroma tokenization included) would burn per-keystroke time
	// linear in session history.
	renderedTheme theme.Theme
	renderedWidth int
}

func (t *transcript) push(rendered string) {
	e := entry{}
	e.setRendered(rendered)
	t.entries = append(t.entries, e)
	t.lines = append(t.lines, e.lines...)
}

// plainExport returns a clipboard/file-friendly transcript without ANSI styling.
func (t *transcript) plainExport() string {
	if len(t.lines) == 0 {
		return ""
	}
	out := make([]string, 0, len(t.lines))
	for _, line := range t.lines {
		out = append(out, ansi.Strip(line))
	}
	return strings.Join(out, "\n") + "\n"
}

func (t *transcript) pushPresentation(p eventPresentation, th theme.Theme, width int) {
	t.upsertPresentationAfter("", p, th, width)
}

// upsertPresentationAfter replaces the entry carrying p.Key in place, or —
// when that key is absent — inserts the new entry directly after afterKey
// instead of appending, so a recreated streaming tail lands inside its own
// message even when unrelated events arrived meanwhile. Missing anchors
// degrade to a plain append.
func (t *transcript) upsertPresentationAfter(afterKey string, p eventPresentation, th theme.Theme, width int) {
	t.noteRenderParams(th, width)
	pCopy := p
	if i := t.indexOf(pCopy.Key); i >= 0 {
		// Preserve the operator's fold choice while lifecycle updates replace
		// the semantic state of the same authoritative call.
		if old := t.entries[i].presentation; old != nil && old.Collapsible && pCopy.Collapsible {
			pCopy.Collapsed = old.Collapsed
		}
		t.entries[i].presentation = &pCopy
		t.entries[i].setRendered(pCopy.render(th, width))
		t.rebuildLines()
		return
	}
	e := entry{key: pCopy.Key, presentation: &pCopy}
	e.setRendered(pCopy.render(th, width))
	at := len(t.entries)
	if i := t.indexOf(afterKey); i >= 0 {
		at = i + 1
	}
	t.insertAt(at, e)
}

// insertPresentationBefore places a new entry directly before the entry with
// beforeKey — the streaming manager commits stable chunks into the exact slot
// its mutable tail occupies. When beforeKey is absent (the tail was empty and
// removed), the entry lands directly after afterKey — the message's own last
// entry — so an interleaved event's header can never adopt the chunk. Only
// when both anchors are missing does it append.
func (t *transcript) insertPresentationBefore(beforeKey, afterKey string, p eventPresentation, th theme.Theme, width int) {
	t.noteRenderParams(th, width)
	pCopy := p
	e := entry{key: pCopy.Key, presentation: &pCopy}
	e.setRendered(pCopy.render(th, width))
	at := len(t.entries)
	if i := t.indexOf(beforeKey); i >= 0 {
		at = i
	} else if i := t.indexOf(afterKey); i >= 0 {
		at = i + 1
	}
	t.insertAt(at, e)
}

func (t *transcript) insertAt(at int, e entry) {
	t.entries = append(t.entries[:at], append([]entry{e}, t.entries[at:]...)...)
	t.rebuildLines()
}

func (t *transcript) indexOf(key string) int {
	if key == "" {
		return -1
	}
	for i := range t.entries {
		if t.entries[i].key == key {
			return i
		}
	}
	return -1
}

// removePresentation drops the entry with the given key (the streaming tail
// once its content has fully committed). It reports whether a removal happened.
func (t *transcript) removePresentation(key string) bool {
	if key == "" {
		return false
	}
	for i := range t.entries {
		if t.entries[i].key == key {
			t.entries = append(t.entries[:i], t.entries[i+1:]...)
			t.rebuildLines()
			return true
		}
	}
	return false
}

// noteRenderParams records the (theme, width) presentations render against so
// resizePresentations can tell a real change from a routine layout() pass.
func (t *transcript) noteRenderParams(th theme.Theme, width int) {
	t.renderedTheme, t.renderedWidth = th, width
}

func (t *transcript) resizePresentations(th theme.Theme, width int) {
	// Rendering is a pure function of (source, theme, width): with both
	// unchanged since the last render, every cached entry is already exact.
	// This runs inside layout() — per keystroke — so the skip is what keeps
	// markdown/chroma re-rendering off the typing path.
	if width == t.renderedWidth && th == t.renderedTheme && width != 0 {
		return
	}
	t.noteRenderParams(th, width)
	changed := false
	for i := range t.entries {
		if p := t.entries[i].presentation; p != nil {
			t.entries[i].setRendered(p.render(th, width))
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
		t.entries[i].setRendered(p.render(th, width))
		t.rebuildLines()
		return true
	}
	return false
}

func (t *transcript) rebuildLines() {
	t.lines = t.lines[:0]
	for i := range t.entries {
		t.lines = append(t.lines, t.entries[i].lines...)
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
	bodyCount := len(p.Body)
	if p.BodyMarkdown != "" {
		bodyCount += strings.Count(p.BodyMarkdown, "\n") + 1
	}
	if len(p.ImageData) > 0 {
		bodyCount++
	}
	if p.Collapsible && bodyCount > 0 {
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
			fold = "[" + strings.ReplaceAll(label, "{count}", fmt.Sprintf("%d", bodyCount)) + "]"
		}
		header += " " + muted.Render(fold)
	}
	var lines []string
	if p.Headerless {
		if p.LeadingBlank {
			lines = append(lines, indent+"  ")
		}
	} else {
		lines = append(lines, fitLine(indent+title.Render(header), width))
	}
	if !p.Collapsed {
		bodyIndent := indent + "  "
		for _, raw := range p.Body {
			for _, line := range strings.Split(sanitize(raw), "\n") {
				if p.BodyProse {
					lines = append(lines, wrapText(line, width, bodyIndent, bodyIndent)...)
				} else {
					lines = append(lines, fitLine(bodyIndent+line, width))
				}
			}
		}
		if p.BodyMarkdown != "" {
			// Markdown output is renderer-emitted styling over already-sanitized
			// source; it must not pass through sanitize again.
			lines = append(lines, markdown.Render(p.BodyMarkdown, th, width, bodyIndent, wrapText)...)
		}
		if len(p.ImageData) > 0 {
			if rendered, ok := mathimage.RenderImage(p.ImageKey, p.ImageData, maxInt(1, width-len(bodyIndent)), bodyIndent); ok {
				lines = append(lines, rendered...)
			} else {
				lines = append(lines, fitLine(bodyIndent+"image preview unavailable in this terminal", width))
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
		if id := firstValue(ev, "decision_id", "permission_decision_id"); id != "" {
			p.Key = "governance:" + id + ":request"
		}
		p.Summary = microcopy.Governed(microcopy.GovernedApprovalRequired, microcopy.Args{
			"action":      str(ev["capability"]),
			"path":        str(ev["resource"]),
			"decision_id": str(ev["decision_id"]),
		}, microcopy.WithLocale(locale))
	case "user.question":
		p.Kind, p.Status, p.Title = presentationGovernance, statusNeedsAuth, "question"
		if id := str(ev["question_id"]); id != "" {
			p.Key = "governance:" + id + ":request"
		}
		p.Summary = truncate(str(ev["prompt"]), 160)
	case "task.completed":
		p.Kind, p.Title = presentationAgent, ""
		if taskID := str(ev["task_id"]); taskID != "" {
			p.Key = "result:" + taskID
		}
		status := str(ev["status"])
		outcome := normalizeConversationOutcome(status)
		p.Status = terminalPresentationStatus(outcome.taskStatus())
		p.Summary = outcome.taskStatus()
		if p.Summary == "" {
			p.Summary = strings.ToLower(strings.TrimSpace(status))
		}
		if summary := str(ev["summary"]); summary != "" {
			if outcome == outcomeCompleted {
				// The durable completion replaces ModelResponded under the same
				// result key, so it must preserve the final assistant message's
				// markdown semantics instead of degrading it to plain prose.
				p.BodyMarkdown = sanitize(summary)
			} else {
				p.Body = []string{summary}
				p.BodyProse = true
			}
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
		if tool, _, _ := safeModelAction(str(payload["text"])); tool == "done" {
			if taskID := str(ev["task_id"]); taskID != "" {
				p.Key = "result:" + taskID
			}
		}
	case "ToolRequested", "ToolApproved", "ToolDenied":
		if status := str(payload["status"]); typ == "ToolRequested" && (status == "permission_requested" || status == "user_question_requested") {
			p = presentGovernanceRequest(p, payload)
		} else {
			p = presentToolEvent(p, typ, payload)
		}
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
	case "activity":
		p.Title = l.Text(MsgTranscriptActivity, nil)
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
	p.Body = selectedBody(payload, "call_id", "reason", "duration_ms")
	if detail := toolCallErrorSummary(payload["error"]); detail != "" {
		p.Body = append(p.Body, "error: "+detail)
	}
	if ids := valueString(payload["artifact_ids"]); ids != "" {
		p.Body = append(p.Body, "artifact: "+ids)
	}
	p.Collapsible = len(p.Body) > 0
	p.Collapsed = len(p.Body) > 0
	return p
}

func presentTaskEvent(p eventPresentation, ev, payload map[string]any) eventPresentation {
	status := str(payload["status"])
	if terminalTranscriptTaskStatus(status) {
		if taskID := eventTaskID(ev, payload); taskID != "" {
			p.Key = "result:" + taskID
		}
	}
	switch {
	case str(payload["workflow"]) != "" || strings.HasPrefix(status, "workflow_"):
		p.Kind, p.Depth, p.Title = presentationWorkflow, 1, "workflow"
		p.Summary = strings.TrimSpace(str(payload["workflow"]) + " " + strings.TrimPrefix(status, "workflow_"))
		p.Status = lifecycleStatus(status)
	case status == "context_compressed" || status == "context_retrieved" || status == "context_engine_failed":
		p.Kind, p.Title = presentationContext, strings.ReplaceAll(status, "_", " ")
		p.Status = lifecycleStatus(status)
		p.Summary = joinValues(payload, "engine", "tool", "savings_percent", "error")
	case status == "risk_review":
		// Autonomous always-approve path: make guardian-style review visible
		// (outcome/risk/rationale), not only a bare capability string.
		p.Kind, p.Title = presentationGovernance, "risk review"
		p.Status = lifecycleStatus(status)
		p.Summary = joinValues(payload, "outcome", "risk", "capability", "mode")
		if r := str(payload["rationale"]); r != "" {
			p.Body = []string{r}
			p.BodyProse = true
			p.Collapsible = true
			p.Collapsed = false
		}
	case status == "permission_requested" || status == "user_question_requested" || status == "approval_resolved" || status == "user_question_resolved":
		p.Kind, p.Title = presentationGovernance, strings.ReplaceAll(status, "_", " ")
		p.Status = lifecycleStatus(status)
		if status == "permission_requested" || status == "user_question_requested" {
			p.Status = statusNeedsAuth
		}
		if granted, ok := payload["granted"].(bool); ok && !granted {
			p.Status = statusFailure
		}
		if cancelled, _ := payload["cancelled"].(bool); cancelled {
			p.Status = statusFailure
		}
		p.Summary = joinValues(payload, "capability", "resource", "decision_id", "question_id", "granted", "value", "cancelled")
		if cancelled, _ := payload["cancelled"].(bool); cancelled {
			p.Summary = strings.TrimSpace(joinValues(payload, "capability", "resource", "decision_id", "question_id") + " cancelled")
		}
		if id := firstValue(payload, "decision_id", "question_id"); id != "" {
			suffix := ":resolved"
			if status == "permission_requested" || status == "user_question_requested" {
				suffix = ":request"
			}
			p.Key = "governance:" + id + suffix
		}
	case status != "":
		p.Kind, p.Title = presentationAgent, "task"
		p.Status = lifecycleStatus(status)
		p.Summary = strings.ReplaceAll(status, "_", " ")
		p.Body = selectedBody(payload, "message", "summary", "reason", "error", "diagnostics")
		p.BodyProse = true
		p.Collapsible = len(p.Body) > 0
		p.Collapsed = len(p.Body) > 0
	default:
		p.Kind, p.Status, p.Title = presentationAgent, statusRunning, "task"
		p.Summary = str(ev["task_id"])
		if prompt := str(payload["user_prompt"]); prompt != "" {
			p.Body = []string{prompt}
			p.BodyProse = true
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
			p.BodyProse = true
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
		// The "done" summary is the final assistant response/plan text — the
		// one free-form surface the transcript shows. It renders through the
		// markdown pipeline; every other action keeps the plain summary row.
		p.BodyMarkdown = sanitize(summary)
		summary = ""
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
	p.BodyProse = true
	p.Collapsible = len(p.Body) > 0 || p.BodyMarkdown != ""
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
		p.BodyProse = true
		p.Collapsible, p.Collapsed = len(p.Body) > 0, true
	} else if workflow := str(payload["workflow"]); workflow != "" {
		p.Kind, p.Depth, p.Title = presentationWorkflow, 2, "step"
		p.Summary = strings.TrimSpace(str(payload["step"]) + " " + str(payload["agent"]))
	}
	return p
}

func presentGovernanceRequest(p eventPresentation, payload map[string]any) eventPresentation {
	status := str(payload["status"])
	request, _ := payload["request"].(map[string]any)
	id := firstValue(payload, "decision_id", "question_id")
	if id == "" {
		id = firstValue(request, "decision_id", "question_id")
	}
	p.Kind, p.Status, p.Title = presentationGovernance, statusNeedsAuth, strings.ReplaceAll(status, "_", " ")
	if id != "" {
		p.Key = "governance:" + id + ":request"
	}
	p.Summary = joinValues(request, "capability", "resource", "prompt", "decision_id", "question_id")
	if p.Summary == "" {
		p.Summary = joinValues(payload, "capability", "resource", "decision_id", "question_id")
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
	case strings.Contains(status, "failed"), strings.Contains(status, "denied"), strings.Contains(status, "degraded"), strings.Contains(status, "rejected"), strings.Contains(status, "cancelled"), strings.Contains(status, "canceled"), strings.Contains(status, "aborted"), strings.Contains(status, "interrupted"):
		return statusFailure
	case strings.Contains(status, "completed"), strings.Contains(status, "resolved"), strings.Contains(status, "applied"), strings.Contains(status, "retrieved"), strings.Contains(status, "compressed"):
		return statusSuccess
	case strings.Contains(status, "approval"), strings.Contains(status, "question"), strings.Contains(status, "review"):
		return statusNeedsAuth
	default:
		return statusRunning
	}
}

func toolCallErrorSummary(value any) string {
	errorMap, _ := value.(map[string]any)
	return joinValues(errorMap, "code", "category", "message")
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
		if r == '\n' || r == '\t' {
			return r
		}
		// DEL and the C1 range (0x80–0x9f) are control characters too: a
		// decoded U+009B is a one-rune CSI introducer on terminals that honor
		// C1, so they stop at this boundary exactly like their C0 siblings.
		if r < ' ' || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return -1
		}
		return r
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
