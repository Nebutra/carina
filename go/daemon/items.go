package daemon

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
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

type patchProjection struct {
	patchID         string
	taskID          string
	proposedAt      string
	completedAt     string
	affectedFiles   []string
	reason          string
	applied         bool
	failed          bool
	rolledBack      bool
	error           string
	newHash         string
	rollbackPointer string
}

type itemCacheEntry struct {
	eventCount int
	items      []SessionItemEvent
	used       time.Time
}

var sessionItemsCache = struct {
	sync.Mutex
	entries map[string]itemCacheEntry
}{entries: make(map[string]itemCacheEntry)}

const sessionItemsCacheLimit = 128

func (d *Daemon) handleSessionItems(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string          `json:"session_id"`
		Cursor    json.RawMessage `json:"cursor"`
		Limit     *int            `json:"limit"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.SessionID == "" {
		return nil, fmt.Errorf("session_id required")
	}
	id := p.SessionID
	raw, err := d.kern.ReadEvents(id)
	if err != nil {
		return nil, err
	}
	var events []itemAuditEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, fmt.Errorf("session.items: decode audit events: %w", err)
	}
	cacheKey := fmt.Sprintf("%p:%s", d, id)
	now := time.Now()
	sessionItemsCache.Lock()
	cached, ok := sessionItemsCache.entries[cacheKey]
	if !ok || cached.eventCount != len(events) {
		cached = itemCacheEntry{eventCount: len(events), items: projectSessionItems(id, events), used: now}
		sessionItemsCache.entries[cacheKey] = cached
	} else {
		cached.used = now
		sessionItemsCache.entries[cacheKey] = cached
	}
	if len(sessionItemsCache.entries) > sessionItemsCacheLimit {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range sessionItemsCache.entries {
			if oldestKey == "" || entry.used.Before(oldest) {
				oldestKey, oldest = key, entry.used
			}
		}
		delete(sessionItemsCache.entries, oldestKey)
	}
	items := append([]SessionItemEvent(nil), cached.items...)
	sessionItemsCache.Unlock()
	if len(p.Cursor) == 0 && p.Limit == nil {
		return items, nil
	}
	cursor, err := decodeItemsCursor(p.Cursor)
	if err != nil {
		return nil, err
	}
	limit := 50
	if p.Limit != nil {
		limit = *p.Limit
	}
	return paginateSessionItems(items, cursor, limit)
}

func paginateSessionItems(items []SessionItemEvent, cursor, limit int) (map[string]any, error) {
	if cursor < 0 {
		return nil, fmt.Errorf("cursor must be non-negative")
	}
	if cursor > len(items) {
		cursor = len(items)
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("limit must be between 1 and 200")
	}
	end := cursor + limit
	if end > len(items) {
		end = len(items)
	}
	result := map[string]any{"data": items[cursor:end]}
	if end < len(items) {
		result["next_cursor"] = encodeItemsCursor(end)
	}
	return result, nil
}

func encodeItemsCursor(index int) string { return fmt.Sprintf("items:v1:%d", index) }
func decodeItemsCursor(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return 0, fmt.Errorf("cursor must be an opaque string")
	}
	const prefix = "items:v1:"
	if !strings.HasPrefix(cursor, prefix) {
		return 0, fmt.Errorf("invalid session.items cursor")
	}
	index, err := strconv.Atoi(strings.TrimPrefix(cursor, prefix))
	if err != nil || index < 0 {
		return 0, fmt.Errorf("invalid session.items cursor")
	}
	return index, nil
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
	toolCalls := map[string]*SessionItem{}
	activeRunByTask := map[string]string{}
	patches := map[string]*patchProjection{}
	patchesByTask := map[string][]string{}
	emittedDiff := map[string]bool{}

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
		case "ToolCallRequested":
			callID := stringField(ev.Payload, "call_id")
			if callID == "" {
				callID = fallbackItemID("tool", ev, i)
			}
			item := &SessionItem{
				ID: callID, Type: "tool_call", Status: "requested", TaskID: ev.TaskID,
				StartedAt: ev.Timestamp, Details: copyMap(ev.Payload),
			}
			toolCalls[callID] = item
			if stringField(ev.Payload, "tool") == "run" {
				activeRunByTask[ev.TaskID] = callID
			}
			out = append(out, itemEvent("item.started", sessionID, ev, item))

		case "ToolCallStarted":
			callID := stringField(ev.Payload, "call_id")
			item := toolCalls[callID]
			if item == nil {
				item = newToolCallItem(ev, callID, i)
				toolCalls[item.ID] = item
			}
			item.Status = "running"
			mergeMap(item.Details, ev.Payload)
			out = append(out, itemEvent("item.updated", sessionID, ev, item))

		case "ToolCallCompleted", "ToolCallFailed", "ToolCallDenied", "ToolCallCancelled":
			callID := stringField(ev.Payload, "call_id")
			item := toolCalls[callID]
			if item == nil {
				item = newToolCallItem(ev, callID, i)
			}
			item.Status = toolCallItemStatus(ev.Type)
			item.CompletedAt = ev.Timestamp
			mergeMap(item.Details, ev.Payload)
			out = append(out, itemEvent("item.completed", sessionID, ev, item))
			delete(toolCalls, item.ID)
			if activeRunByTask[ev.TaskID] == item.ID {
				delete(activeRunByTask, ev.TaskID)
			}

		case "RuntimeStageChanged":
			out = append(out, SessionItemEvent{
				Type: "runtime.stage_changed", SessionID: nonempty(sessionID, ev.SessionID),
				TurnID: ev.TaskID, TaskID: ev.TaskID, ItemID: stringField(ev.Payload, "call_id"),
				SourceEventID: ev.EventID, Timestamp: ev.Timestamp, Details: copyMap(ev.Payload),
			})

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
			if activeRunByTask[ev.TaskID] != "" {
				break
			}
			item := newCommandItem(ev, fallbackItemID("cmd", ev, i))
			cmd := &commandProjection{item: item}
			openByID[item.ID] = cmd
			openByTask[ev.TaskID] = append(openByTask[ev.TaskID], cmd)
			out = append(out, itemEvent("item.started", sessionID, ev, item))

		case "CommandOutput":
			if activeRunByTask[ev.TaskID] != "" {
				break
			}
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
			if activeRunByTask[ev.TaskID] != "" {
				break
			}
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
			trackPatchProjection(patches, patchesByTask, ev)
			out = append(out, fileChangeEvent(sessionID, ev, fallbackItemID("file", ev, i), "proposed"))
		case "PatchApplied":
			trackPatchProjection(patches, patchesByTask, ev)
			out = append(out, fileChangeEvent(sessionID, ev, fallbackItemID("file", ev, i), "applied"))
		case "PatchFailed":
			trackPatchProjection(patches, patchesByTask, ev)
			out = append(out, fileChangeEvent(sessionID, ev, fallbackItemID("file", ev, i), "failed"))
		case "RollbackCompleted":
			trackPatchProjection(patches, patchesByTask, ev)
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
					out = appendTurnNetDiff(out, sessionID, ev, patches, patchesByTask, emittedDiff)
					out = append(out, turnEvent("turn.completed", sessionID, ev, copyMap(ev.Payload)))
				case "degraded", "failed", "cancelled":
					out = appendTurnNetDiff(out, sessionID, ev, patches, patchesByTask, emittedDiff)
					out = append(out, turnEvent("turn.failed", sessionID, ev, copyMap(ev.Payload)))
				}
			}
		}
	}
	return out
}

func newToolCallItem(ev itemAuditEvent, callID string, index int) *SessionItem {
	if callID == "" {
		callID = fallbackItemID("tool", ev, index)
	}
	return &SessionItem{
		ID: callID, Type: "tool_call", Status: "running", TaskID: ev.TaskID,
		StartedAt: ev.Timestamp, Details: copyMap(ev.Payload),
	}
}

func toolCallItemStatus(eventType string) string {
	switch eventType {
	case "ToolCallCompleted":
		return "completed"
	case "ToolCallDenied":
		return "denied"
	case "ToolCallCancelled":
		return "cancelled"
	default:
		return "failed"
	}
}

func mergeMap(dst, src map[string]any) {
	if dst == nil {
		return
	}
	for key, value := range src {
		dst[key] = value
	}
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

func trackPatchProjection(patches map[string]*patchProjection, byTask map[string][]string, ev itemAuditEvent) {
	patchID := stringField(ev.Payload, "patch_id")
	if patchID == "" {
		return
	}
	p := patches[patchID]
	if p == nil {
		p = &patchProjection{patchID: patchID}
		patches[patchID] = p
	}
	if ev.TaskID != "" && p.taskID == "" {
		p.taskID = ev.TaskID
		byTask[ev.TaskID] = appendUnique(byTask[ev.TaskID], patchID)
	}
	switch ev.Type {
	case "PatchProposed":
		p.proposedAt = ev.Timestamp
		p.reason = stringField(ev.Payload, "reason")
		p.affectedFiles = stringSliceField(ev.Payload, "affected_files")
	case "PatchApplied":
		p.applied = true
		p.completedAt = ev.Timestamp
		p.newHash = stringField(ev.Payload, "new_hash")
		p.rollbackPointer = stringField(ev.Payload, "rollback_pointer")
	case "PatchFailed":
		p.failed = true
		p.completedAt = ev.Timestamp
		p.error = stringField(ev.Payload, "error")
	case "RollbackCompleted":
		p.rolledBack = true
		p.completedAt = ev.Timestamp
	}
}

func appendTurnNetDiff(out []SessionItemEvent, sessionID string, ev itemAuditEvent, patches map[string]*patchProjection, byTask map[string][]string, emitted map[string]bool) []SessionItemEvent {
	taskID := ev.TaskID
	if taskID == "" || emitted[taskID] {
		return out
	}
	var list []*patchProjection
	for _, patchID := range byTask[taskID] {
		if p := patches[patchID]; p != nil && p.hasDiffSignal() {
			list = append(list, p)
		}
	}
	if len(list) == 0 {
		return out
	}
	emitted[taskID] = true
	return append(out, turnNetDiffEvent(sessionID, ev, list))
}

func (p *patchProjection) hasDiffSignal() bool {
	return p.applied || p.failed || p.rolledBack
}

func turnNetDiffEvent(sessionID string, ev itemAuditEvent, patches []*patchProjection) SessionItemEvent {
	var activeFiles []string
	var revertedFiles []string
	var failedFiles []string
	patchDetails := make([]map[string]any, 0, len(patches))
	status := "completed"
	for _, p := range patches {
		patchStatus := "proposed"
		switch {
		case p.rolledBack:
			patchStatus = "reverted"
			revertedFiles = appendUniqueStrings(revertedFiles, p.affectedFiles...)
		case p.failed:
			patchStatus = "failed"
			failedFiles = appendUniqueStrings(failedFiles, p.affectedFiles...)
			if len(activeFiles) == 0 {
				status = "failed"
			}
		case p.applied:
			patchStatus = "applied"
			activeFiles = appendUniqueStrings(activeFiles, p.affectedFiles...)
		}
		detail := map[string]any{
			"patch_id":       p.patchID,
			"status":         patchStatus,
			"affected_files": append([]string{}, p.affectedFiles...),
		}
		if p.reason != "" {
			detail["reason"] = p.reason
		}
		if p.newHash != "" {
			detail["new_hash"] = p.newHash
		}
		if p.rollbackPointer != "" {
			detail["rollback_pointer"] = p.rollbackPointer
		}
		if p.error != "" {
			detail["error"] = p.error
		}
		patchDetails = append(patchDetails, detail)
	}
	item := &SessionItem{
		ID:          "diff_" + ev.TaskID,
		Type:        "turn_net_diff",
		Status:      status,
		TaskID:      ev.TaskID,
		StartedAt:   ev.Timestamp,
		CompletedAt: ev.Timestamp,
		Details: map[string]any{
			"patch_count":    len(patches),
			"active_files":   activeFiles,
			"reverted_files": revertedFiles,
			"failed_files":   failedFiles,
			"patches":        patchDetails,
		},
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

func stringSliceField(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch list := v.(type) {
	case []string:
		return append([]string{}, list...)
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func appendUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}

func appendUniqueStrings(list []string, values ...string) []string {
	for _, v := range values {
		list = appendUnique(list, v)
	}
	return list
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
