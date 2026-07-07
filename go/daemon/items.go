package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
)

type itemAuditEvent struct {
	EventID              string         `json:"event_id"`
	SessionID            string         `json:"session_id"`
	TaskID               string         `json:"task_id,omitempty"`
	Type                 string         `json:"type"`
	Actor                string         `json:"actor,omitempty"`
	Timestamp            string         `json:"timestamp,omitempty"`
	Payload              map[string]any `json:"payload,omitempty"`
	PermissionDecisionID string         `json:"permission_decision_id,omitempty"`
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
	Item          *SessionItem   `json:"item,omitempty"`
}

type SessionItem struct {
	ID          string         `json:"id"`
	Type        string         `json:"type"`
	Status      string         `json:"status"`
	TaskID      string         `json:"task_id,omitempty"`
	StartedAt   string         `json:"started_at,omitempty"`
	CompletedAt string         `json:"completed_at,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

type commandProjection struct {
	item   *SessionItem
	stdout []string
	stderr []string
}

func (d *Daemon) handleSessionItems(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	raw, err := d.kern.ReadEvents(id)
	if err != nil {
		return nil, err
	}
	var events []itemAuditEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, fmt.Errorf("session.items: decode audit events: %w", err)
	}
	return projectSessionItems(id, events), nil
}

func projectSessionItems(sessionID string, events []itemAuditEvent) []SessionItemEvent {
	out := make([]SessionItemEvent, 0, len(events)+1)
	started := SessionItemEvent{Type: "thread.started", SessionID: sessionID}
	if len(events) > 0 {
		started.SourceEventID = events[0].EventID
		started.Timestamp = events[0].Timestamp
		if sessionID == "" {
			started.SessionID = events[0].SessionID
		}
	}
	out = append(out, started)

	seenTurns := map[string]bool{}
	openByID := map[string]*commandProjection{}
	openByTask := map[string][]*commandProjection{}

	for i, ev := range events {
		if sessionID == "" {
			sessionID = ev.SessionID
		}
		if ev.TaskID != "" && !seenTurns[ev.TaskID] {
			seenTurns[ev.TaskID] = true
			out = append(out, SessionItemEvent{
				Type:          "turn.started",
				SessionID:     nonempty(sessionID, ev.SessionID),
				TurnID:        ev.TaskID,
				TaskID:        ev.TaskID,
				SourceEventID: ev.EventID,
				Timestamp:     ev.Timestamp,
				Details:       turnStartDetails(ev),
			})
		}

		switch ev.Type {
		case "ModelResponded":
			item := &SessionItem{
				ID:          fallbackItemID("msg", ev, i),
				Type:        "agent_message",
				Status:      modelStatus(ev.Payload),
				TaskID:      ev.TaskID,
				StartedAt:   ev.Timestamp,
				CompletedAt: ev.Timestamp,
				Details:     modelResponseDetails(ev),
			}
			out = append(out, itemEvent("item.completed", sessionID, ev, item))
			if item.Status == "failed" {
				out = append(out, turnEvent("turn.failed", sessionID, ev, map[string]any{
					"status": "failed",
					"error":  stringField(ev.Payload, "error"),
				}))
			}

		case "CommandStarted":
			item := newCommandItem(ev, fallbackItemID("cmd", ev, i))
			cmd := &commandProjection{item: item}
			openByID[item.ID] = cmd
			openByTask[ev.TaskID] = append(openByTask[ev.TaskID], cmd)
			out = append(out, itemEvent("item.started", sessionID, ev, item))

		case "CommandOutput":
			cmd := findOpenCommand(ev, openByID, openByTask)
			if cmd == nil {
				cmd = &commandProjection{item: newCommandItem(ev, fallbackItemID("cmd", ev, i))}
				openByID[cmd.item.ID] = cmd
				openByTask[ev.TaskID] = append(openByTask[ev.TaskID], cmd)
			}
			stream, chunk := stringField(ev.Payload, "stream"), stringField(ev.Payload, "chunk")
			if stream == "stderr" {
				cmd.stderr = append(cmd.stderr, chunk)
			} else {
				stream = "stdout"
				cmd.stdout = append(cmd.stdout, chunk)
			}
			cmd.item.Status = "running"
			setCommandOutputDetails(cmd)
			cmd.item.Details["last_stream"] = stream
			cmd.item.Details["last_chunk"] = chunk
			out = append(out, itemEvent("item.updated", sessionID, ev, cmd.item))

		case "CommandExited":
			cmd := findOpenCommand(ev, openByID, openByTask)
			if cmd == nil {
				cmd = &commandProjection{item: newCommandItem(ev, fallbackItemID("cmd", ev, i))}
			}
			cmd.item.Status = commandExitStatus(ev.Payload)
			cmd.item.CompletedAt = ev.Timestamp
			copySelected(cmd.item.Details, ev.Payload, "exit_code", "duration_ms", "error")
			setCommandOutputDetails(cmd)
			out = append(out, itemEvent("item.completed", sessionID, ev, cmd.item))
			delete(openByID, cmd.item.ID)
			openByTask[ev.TaskID] = removeCommand(openByTask[ev.TaskID], cmd)

		case "PatchProposed":
			out = append(out, fileChangeEvent(sessionID, ev, fallbackItemID("file", ev, i), "proposed"))
		case "PatchApplied":
			out = append(out, fileChangeEvent(sessionID, ev, fallbackItemID("file", ev, i), "applied"))
		case "PatchFailed":
			out = append(out, fileChangeEvent(sessionID, ev, fallbackItemID("file", ev, i), "failed"))
		case "RollbackCompleted":
			out = append(out, fileChangeEvent(sessionID, ev, fallbackItemID("file", ev, i), "rolled_back"))
		case "PolicyViolation":
			item := &SessionItem{
				ID:          fallbackItemID("err", ev, i),
				Type:        "error",
				Status:      "failed",
				TaskID:      ev.TaskID,
				StartedAt:   ev.Timestamp,
				CompletedAt: ev.Timestamp,
				Details:     copyMap(ev.Payload),
			}
			item.Details["source_type"] = ev.Type
			out = append(out, itemEvent("item.completed", sessionID, ev, item))
		}

		if ev.Type == "TaskCreated" {
			if status := stringField(ev.Payload, "status"); status != "" {
				switch status {
				case "risk_review":
					out = append(out, riskReviewEvent(sessionID, ev, fallbackItemID("risk", ev, i)))
				case "completed":
					out = append(out, turnEvent("turn.completed", sessionID, ev, copyMap(ev.Payload)))
				case "degraded", "failed", "cancelled":
					out = append(out, turnEvent("turn.failed", sessionID, ev, copyMap(ev.Payload)))
				}
			}
		}
	}
	return out
}

func newCommandItem(ev itemAuditEvent, fallbackID string) *SessionItem {
	id := stringField(ev.Payload, "command_id")
	if id == "" {
		id = fallbackID
	}
	details := map[string]any{}
	copySelected(details, ev.Payload, "command", "cwd", "risk_level", "package_mutation")
	if ev.Actor != "" {
		details["actor"] = ev.Actor
	}
	if ev.PermissionDecisionID != "" {
		details["permission_decision_id"] = ev.PermissionDecisionID
	}
	return &SessionItem{
		ID:        id,
		Type:      "command_execution",
		Status:    "started",
		TaskID:    ev.TaskID,
		StartedAt: ev.Timestamp,
		Details:   details,
	}
}

func fileChangeEvent(sessionID string, ev itemAuditEvent, id, status string) SessionItemEvent {
	item := &SessionItem{
		ID:          id,
		Type:        "file_change",
		Status:      status,
		TaskID:      ev.TaskID,
		StartedAt:   ev.Timestamp,
		CompletedAt: ev.Timestamp,
		Details:     copyMap(ev.Payload),
	}
	return itemEvent("item.completed", sessionID, ev, item)
}

func riskReviewEvent(sessionID string, ev itemAuditEvent, fallbackID string) SessionItemEvent {
	id := stringField(ev.Payload, "decision_id")
	if id == "" {
		id = fallbackID
	}
	status := "completed"
	if stringField(ev.Payload, "outcome") == "deny" {
		status = "failed"
	}
	details := copyMap(ev.Payload)
	if ev.PermissionDecisionID != "" {
		details["permission_decision_id"] = ev.PermissionDecisionID
	}
	item := &SessionItem{
		ID:          id,
		Type:        "risk_review",
		Status:      status,
		TaskID:      ev.TaskID,
		StartedAt:   ev.Timestamp,
		CompletedAt: ev.Timestamp,
		Details:     details,
	}
	return itemEvent("item.completed", sessionID, ev, item)
}

func itemEvent(eventType, sessionID string, ev itemAuditEvent, item *SessionItem) SessionItemEvent {
	return SessionItemEvent{
		Type:          eventType,
		SessionID:     nonempty(sessionID, ev.SessionID),
		TurnID:        ev.TaskID,
		TaskID:        ev.TaskID,
		ItemID:        item.ID,
		SourceEventID: ev.EventID,
		Timestamp:     ev.Timestamp,
		Item:          cloneItem(item),
	}
}

func turnEvent(eventType, sessionID string, ev itemAuditEvent, details map[string]any) SessionItemEvent {
	return SessionItemEvent{
		Type:          eventType,
		SessionID:     nonempty(sessionID, ev.SessionID),
		TurnID:        ev.TaskID,
		TaskID:        ev.TaskID,
		SourceEventID: ev.EventID,
		Timestamp:     ev.Timestamp,
		Details:       details,
	}
}

func turnStartDetails(ev itemAuditEvent) map[string]any {
	details := map[string]any{}
	copySelected(details, ev.Payload, "status", "prompt", "model", "agent")
	if ev.Type != "" {
		details["source_type"] = ev.Type
	}
	return details
}

func modelResponseDetails(ev itemAuditEvent) map[string]any {
	details := copyMap(ev.Payload)
	if ev.Actor != "" {
		details["actor"] = ev.Actor
	}
	return details
}

func modelStatus(payload map[string]any) string {
	if stringField(payload, "error") != "" {
		return "failed"
	}
	return "completed"
}

func commandExitStatus(payload map[string]any) string {
	if stringField(payload, "error") != "" {
		return "failed"
	}
	if v, ok := payload["exit_code"]; ok && fmt.Sprint(v) != "0" {
		return "failed"
	}
	return "completed"
}

func findOpenCommand(ev itemAuditEvent, byID map[string]*commandProjection, byTask map[string][]*commandProjection) *commandProjection {
	if id := stringField(ev.Payload, "command_id"); id != "" {
		return byID[id]
	}
	list := byTask[ev.TaskID]
	if len(list) == 0 {
		return nil
	}
	return list[len(list)-1]
}

func removeCommand(list []*commandProjection, target *commandProjection) []*commandProjection {
	for i, cmd := range list {
		if cmd == target {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

func setCommandOutputDetails(cmd *commandProjection) {
	if len(cmd.stdout) > 0 {
		cmd.item.Details["stdout"] = strings.Join(cmd.stdout, "")
	}
	if len(cmd.stderr) > 0 {
		cmd.item.Details["stderr"] = strings.Join(cmd.stderr, "")
	}
	if len(cmd.stdout) > 0 || len(cmd.stderr) > 0 {
		cmd.item.Details["aggregated_output"] = strings.Join(append(append([]string{}, cmd.stdout...), cmd.stderr...), "")
	}
}

func fallbackItemID(prefix string, ev itemAuditEvent, index int) string {
	if ev.EventID != "" {
		return prefix + "_" + ev.EventID
	}
	return fmt.Sprintf("%s_%06d", prefix, index)
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func copySelected(dst map[string]any, src map[string]any, keys ...string) {
	for _, key := range keys {
		if v, ok := src[key]; ok {
			dst[key] = v
		}
	}
}

func copyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func cloneItem(item *SessionItem) *SessionItem {
	if item == nil {
		return nil
	}
	clone := *item
	clone.Details = copyMap(item.Details)
	return &clone
}

func nonempty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
