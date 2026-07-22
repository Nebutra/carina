package tui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

type taskNode struct {
	ID             string
	ParentID       string
	Kind           string
	Label          string
	Status         string
	RequestedModel string
	EffectiveModel string
	Order          int
}

// taskGraph is a compact projection, not a second source of truth. Durable
// replay and transient completion events both fold through observeEvent, so
// reconnects reconstruct the same tree as a live session.
type taskGraph struct {
	nodes map[string]*taskNode
	order []string
	seq   int
}

func (g *taskGraph) ensure(id, parentID, kind, label, status string) *taskNode {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	if g.nodes == nil {
		g.nodes = make(map[string]*taskNode)
	}
	node := g.nodes[id]
	if node == nil {
		g.seq++
		node = &taskNode{ID: id, Order: g.seq}
		g.nodes[id] = node
		g.order = append(g.order, id)
	}
	if parentID != "" {
		node.ParentID = parentID
	}
	if kind != "" {
		node.Kind = kind
	}
	if label != "" {
		node.Label = label
	}
	if status != "" {
		node.Status = normalizeTaskStatus(status)
	}
	return node
}

func normalizeTaskStatus(status string) string {
	status = strings.TrimSpace(strings.ToLower(status))
	if outcome := normalizeConversationOutcome(status); outcome != outcomeNone {
		return outcome.taskStatus()
	}
	switch {
	case status == "":
		return "running"
	case strings.HasSuffix(status, "_completed"):
		return "completed"
	case strings.Contains(status, "_failed"):
		return "failed"
	case strings.Contains(status, "approval") || strings.Contains(status, "question") || strings.Contains(status, "review"):
		return "waiting"
	case status == "queued":
		return "queued"
	case status == "paused" || status == "checkpoint_restored":
		return "paused"
	case status == "interrupted":
		return "interrupted"
	default:
		return "running"
	}
}

func (g *taskGraph) observeConversation(p conversationProjection) {
	if taskID := p.Evidence.ActiveTaskID; taskID != "" {
		status := "running"
		switch p.Activity {
		case activityWaitingApproval, activityWaitingQuestion:
			status = "waiting"
		case activityInterrupted:
			status = "interrupted"
		}
		g.setTask(taskID, status)
	}
	if taskID := p.Evidence.TerminalID; taskID != "" && p.Outcome != outcomeNone {
		g.setTask(taskID, p.Outcome.taskStatus())
	}
}

func (g *taskGraph) observeEvent(ev map[string]any) {
	typ := str(ev["type"])
	payload, _ := ev["payload"].(map[string]any)
	taskID := str(ev["task_id"])
	if taskID == "" {
		taskID = str(payload["task_id"])
	}

	switch typ {
	case "TaskCreated":
		status := str(payload["status"])
		if workflow := str(payload["workflow"]); workflow != "" || strings.HasPrefix(status, "workflow_") {
			runID := str(payload["run_id"])
			if runID == "" {
				runID = taskID + ":workflow:" + workflow
			}
			node := g.ensure(runID, taskID, "workflow", workflow, status)
			if node != nil && (status == "workflow_completed" || status == "workflow_failed") {
				g.completeChildren(runID, node.Status)
			}
			return
		}
		label := str(payload["user_prompt"])
		if label == "" {
			label = firstValue(payload, "agent", "summary", "reason")
		}
		node := g.ensure(taskID, "", "task", label, status)
		if node != nil {
			node.RequestedModel = str(payload["requested_model"])
			node.EffectiveModel = str(payload["effective_model"])
		}
	case "RoutingOutcome":
		if node := g.ensure(taskID, "", "task", "", ""); node != nil && str(payload["status"]) == "succeeded" {
			provider, model := str(payload["provider"]), str(payload["model"])
			if provider != "" && model != "" && !strings.HasPrefix(model, provider+"/") {
				model = provider + "/" + model
			}
			if model != "" {
				node.EffectiveModel = model
			}
			if requested := str(payload["requested_model"]); requested != "" {
				node.RequestedModel = requested
			}
		}
	case "ToolApproved":
		if agent := str(payload["spawn_agent"]); agent != "" {
			child := str(payload["child_session"])
			g.ensure(child, taskID, "subagent", agent, "running")
			return
		}
		if runID, step := str(payload["run_id"]), str(payload["step"]); runID != "" && step != "" {
			g.ensure(runID, taskID, "workflow", str(payload["workflow"]), "running")
			g.ensure(runID+":"+step, runID, "step", strings.TrimSpace(step+" "+str(payload["agent"])), "running")
		}
	case "ModelResponded":
		if child := str(payload["child_session"]); child != "" && str(payload["spawn_agent"]) != "" {
			g.ensure(child, taskID, "subagent", str(payload["spawn_agent"]), "completed")
			return
		}
		if runID := str(payload["run_id"]); runID != "" && strings.HasPrefix(str(payload["status"]), "workflow_") {
			node := g.ensure(runID, taskID, "workflow", str(payload["workflow"]), str(payload["status"]))
			if node != nil {
				g.completeChildren(runID, node.Status)
			}
		}
	}
}

func (g *taskGraph) setTask(id, status string) {
	g.ensure(id, "", "task", "", status)
}

func (g *taskGraph) completeChildren(parentID, status string) {
	for _, node := range g.nodes {
		if node.ParentID == parentID && !terminalTaskStatus(node.Status) {
			node.Status = status
		}
	}
}

func (g *taskGraph) activeCount() int {
	n := 0
	for _, node := range g.nodes {
		if node.Status != "paused" && !terminalTaskStatus(node.Status) {
			n++
		}
	}
	return n
}

func (g *taskGraph) lines(m *Model, width, limit int) []string {
	if width <= 0 || limit <= 0 || len(g.nodes) == 0 {
		return nil
	}
	ordered := append([]string(nil), g.order...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return g.nodes[ordered[i]].Order < g.nodes[ordered[j]].Order
	})

	// Active/error work is always visible. Completed nodes are folded into one
	// summary when the tree would otherwise dominate the transcript.
	var visible []*taskNode
	completed := 0
	for _, id := range ordered {
		node := g.nodes[id]
		if node == nil {
			continue
		}
		if node.Status == "completed" {
			completed++
			continue
		}
		visible = append(visible, node)
	}
	if len(visible) == 0 {
		return nil
	}

	header := m.text(MsgTasksHeader, MessageArgs{"active": g.activeCount(), "done": completed})
	// A single root task is ambient context, not a dashboard. Render it as one
	// rail; the full header/tree appears only when hierarchy or concurrency
	// makes the counts useful.
	compactRoot := len(visible) == 1 && visible[0].ParentID == ""
	var out []string
	if !compactRoot {
		out = append(out, fitLine(m.th.Style(theme.RoleMuted).Render(header), width))
	}
	for _, node := range visible {
		if len(out) >= limit {
			break
		}
		prefix := "-"
		if compactRoot {
			prefix = ""
		}
		if node.ParentID != "" {
			prefix = "  `-"
		}
		glyph := taskStatusGlyph(m.th, node.Status)
		label := node.Label
		if label == "" {
			label = node.ID
		}
		line := m.text(MsgTaskLine, MessageArgs{
			"prefix": prefix, "glyph": glyph, "kind": m.taskKindText(node.Kind),
			"label": label, "status": m.taskStatusText(node.Status),
		})
		out = append(out, fitLine(line, width))
	}
	headerRows := 1
	if compactRoot {
		headerRows = 0
	}
	if len(visible)+headerRows > limit {
		out[limit-1] = fitLine(m.countText(MsgTasksMore, len(visible)-(limit-headerRows), nil), width)
	}
	return out
}

func (m *Model) taskKindText(kind string) string {
	switch kind {
	case "subagent":
		return m.text(MsgTranscriptSubagent, nil)
	case "workflow":
		return m.text(MsgTranscriptWorkflow, nil)
	case "step":
		return m.text(MsgTranscriptStep, nil)
	default:
		return m.text(MsgTranscriptTask, nil)
	}
}

func (m *Model) taskStatusText(status string) string {
	switch status {
	case "completed":
		return m.text(MsgTaskStatusCompleted, nil)
	case "failed":
		return m.text(MsgTaskStatusFailed, nil)
	case "cancelled":
		return m.text(MsgTaskStatusCancelled, nil)
	case "degraded":
		return m.text(MsgTaskStatusDegraded, nil)
	case "waiting":
		return m.text(MsgTaskStatusWaiting, nil)
	case "queued":
		return m.text(MsgTaskStatusQueued, nil)
	case "paused":
		return m.text(MsgTaskStatusPaused, nil)
	case "interrupted":
		return m.text(MsgTaskStatusInterrupted, nil)
	default:
		return m.text(MsgTaskStatusRunning, nil)
	}
}

func taskStatusGlyph(th theme.Theme, status string) string {
	switch status {
	case "completed":
		return glyphOK(th)
	case "failed", "cancelled", "degraded":
		return glyphFailed(th)
	case "waiting", "interrupted":
		return glyphNeedsAuth(th)
	case "paused":
		return glyphNeutral(th)
	default:
		return glyphRunning(th)
	}
}

func visualLinesFit(lines []string, width int) bool {
	for _, line := range lines {
		if ansi.StringWidth(line) > width {
			return false
		}
	}
	return true
}
