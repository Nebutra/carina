package tui

import (
	"fmt"
	"strings"
)

type transcriptEventClass int

const (
	transcriptPermanentConversation transcriptEventClass = iota
	transcriptPermanentOperational
	transcriptGroupedActivity
	transcriptEphemeralActivity
	transcriptAuditOnly
)

type transcriptEventClassification struct {
	Class  transcriptEventClass
	TaskID string
	CallID string
	Family string
}

type activityChild struct {
	CallID string
	Tool   string
	Detail string
	Status string
}

type activityGroup struct {
	Key       string
	TaskID    string
	Family    string
	Timestamp string
	order     []string
	children  map[string]activityChild
}

func classifyTranscriptEvent(ev map[string]any) transcriptEventClassification {
	typ := str(ev["type"])
	payload, _ := ev["payload"].(map[string]any)
	taskID := eventTaskID(ev, payload)
	base := transcriptEventClassification{Class: transcriptPermanentOperational, TaskID: taskID}

	switch typ {
	case "ModelRequested", "RoutingDecision", "RoutingOutcome", "RuntimeStageChanged",
		"ToolApproved", "MemoryRecallRequested", "MemoryWriteRequested",
		"GoalChangeRequested", "ScheduleChanged":
		base.Class = transcriptAuditOnly
		return base
	case "ToolRequested":
		status := strings.ToLower(str(payload["status"]))
		if status != "permission_requested" && status != "user_question_requested" {
			base.Class = transcriptAuditOnly
		}
		return base
	case "ToolDenied":
		return base
	case "MemoryProjectionChanged":
		status := strings.ToLower(str(payload["status"]))
		if status != "failed" && status != "reconcile" {
			base.Class = transcriptAuditOnly
		}
		return base
	case "ModelResponded":
		tool, _, _ := safeModelAction(str(payload["text"]))
		if tool == "done" {
			base.Class = transcriptPermanentConversation
			return base
		}
		if str(payload["spawn_agent"]) != "" || strings.HasPrefix(str(payload["status"]), "workflow_") {
			return base
		}
		base.Class = transcriptAuditOnly
		return base
	case "task.completed":
		base.Class = transcriptPermanentConversation
		return base
	case "TaskCreated":
		status := strings.ToLower(str(payload["status"]))
		switch {
		case terminalTranscriptTaskStatus(status):
			base.Class = transcriptPermanentConversation
		case status == "risk_review", status == "permission_requested", status == "user_question_requested",
			status == "approval_resolved", status == "user_question_resolved", status == "context_engine_failed":
			base.Class = transcriptPermanentOperational
		default:
			base.Class = transcriptAuditOnly
		}
		return base
	case "FileRead":
		if taskID != "" {
			base.Class = transcriptEphemeralActivity
		}
		return base
	case "ToolCallRequested", "ToolCallStarted", "ToolCallCompleted", "ToolCallApprovalRequired",
		"ToolCallFailed", "ToolCallDenied", "ToolCallCancelled":
		base.CallID = str(payload["call_id"])
		base.Family = activityFamily(payload)
		if base.Family != "read" || base.TaskID == "" || base.CallID == "" {
			return base
		}
		if typ == "ToolCallRequested" || typ == "ToolCallStarted" || (typ == "ToolCallCompleted" && len(mediaReferences(ev)) == 0) {
			base.Class = transcriptGroupedActivity
		}
		return base
	default:
		return base
	}
}

func activityFamily(payload map[string]any) string {
	if kind := strings.ToLower(strings.TrimSpace(str(payload["kind"]))); kind != "" {
		if kind == "read" {
			return "read"
		}
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(str(payload["tool"]))) {
	case "read", "list", "search", "code.search", "code.symbols", "code.map", "code.def", "code.refs", "code.impact", "mcp_find":
		return "read"
	default:
		return ""
	}
}

func terminalTranscriptTaskStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "degraded", "failed", "cancelled", "canceled", "aborted", "denied", "interrupted":
		return true
	default:
		return false
	}
}

func activityGroupKey(taskID, family string) string {
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(family) == "" {
		return ""
	}
	return "activity:" + taskID + ":" + family
}

func (g *activityGroup) observe(ev map[string]any) {
	payload, _ := ev["payload"].(map[string]any)
	callID := str(payload["call_id"])
	if callID == "" {
		return
	}
	child, exists := g.children[callID]
	if !exists {
		child.CallID = callID
		g.order = append(g.order, callID)
	}
	if tool := strings.TrimSpace(str(payload["tool"])); tool != "" {
		child.Tool = tool
	}
	if detail := activityDetail(payload); detail != "" {
		child.Detail = detail
	}
	child.Status = activityLifecycleStatus(str(ev["type"]), str(payload["status"]))
	g.children[callID] = child
	if g.Timestamp == "" {
		g.Timestamp = presentationTimestamp(ev)
	}
}

func activityDetail(payload map[string]any) string {
	arguments, _ := payload["arguments"].(map[string]any)
	for _, key := range []string{"path", "pattern", "query", "name", "resource"} {
		if value := valueString(arguments[key]); value != "" {
			return value
		}
	}
	return ""
}

func activityLifecycleStatus(eventType, payloadStatus string) string {
	switch eventType {
	case "ToolCallCompleted":
		return "completed"
	case "ToolCallFailed":
		return "failed"
	case "ToolCallDenied":
		return "denied"
	case "ToolCallCancelled":
		return "cancelled"
	case "ToolCallApprovalRequired":
		return "awaiting approval"
	}
	if status := strings.TrimSpace(payloadStatus); status != "" && status != "requested" {
		return strings.ReplaceAll(status, "_", " ")
	}
	return "running"
}

func (g *activityGroup) remove(callID string) bool {
	if g == nil || callID == "" {
		return false
	}
	if _, ok := g.children[callID]; !ok {
		return false
	}
	delete(g.children, callID)
	for i, id := range g.order {
		if id == callID {
			g.order = append(g.order[:i], g.order[i+1:]...)
			break
		}
	}
	return true
}

func (g *activityGroup) presentation(locale string) eventPresentation {
	p := eventPresentation{
		Key: g.Key, Kind: presentationTool, Title: "activity", Timestamp: g.Timestamp,
		Status: statusSuccess, Summary: fmt.Sprintf("%s %d", g.Family, len(g.order)),
		Collapsible: true, Collapsed: true,
	}
	for _, callID := range g.order {
		child, ok := g.children[callID]
		if !ok {
			continue
		}
		label := strings.TrimSpace(strings.Join(nonEmpty(child.Tool, child.Detail), " "))
		if label == "" {
			label = shortID(child.CallID)
		}
		if child.Status != "" {
			label += " [" + child.Status + "]"
		}
		if child.Status != "completed" {
			p.Status = statusRunning
		}
		p.Body = append(p.Body, label)
	}
	localizePresentation(&p, newLocalizer(locale))
	return p
}

func (m *Model) pushActivityEvent(ev map[string]any, classification transcriptEventClassification) int {
	if m.activityGroups == nil {
		m.activityGroups = make(map[string]*activityGroup)
	}
	key := activityGroupKey(classification.TaskID, classification.Family)
	group := m.activityGroups[key]
	if group == nil {
		group = &activityGroup{Key: key, TaskID: classification.TaskID, Family: classification.Family, children: make(map[string]activityChild)}
		m.activityGroups[key] = group
	}
	existed := m.tr.indexOf(key) >= 0
	before := len(m.tr.lines)
	group.observe(ev)
	m.tr.pushPresentation(group.presentation(m.locale), m.th, m.transcriptWidth())
	if existed {
		return 0
	}
	return maxInt(len(m.tr.lines)-before, 1)
}

func (m *Model) detachActivityCall(classification transcriptEventClassification) {
	if classification.CallID == "" || classification.Family == "" || classification.TaskID == "" {
		return
	}
	key := activityGroupKey(classification.TaskID, classification.Family)
	group := m.activityGroups[key]
	if group == nil || !group.remove(classification.CallID) {
		return
	}
	if len(group.order) == 0 {
		delete(m.activityGroups, key)
		m.tr.removePresentation(key)
		return
	}
	m.tr.pushPresentation(group.presentation(m.locale), m.th, m.transcriptWidth())
}
