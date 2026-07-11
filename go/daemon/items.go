package daemon

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/rpc"
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
const sessionProjectionVersion = "1.0.0"

type projectionCursorClaims struct {
	Version    int    `json:"v"`
	SessionID  string `json:"sid"`
	Projection string `json:"projection"`
	Epoch      string `json:"epoch"`
	Position   int    `json:"position"`
}
type projectionCursorAuthority struct{ key []byte }

var projectionCursorAuthorities = struct {
	sync.Mutex
	byStateDir map[string]*projectionCursorAuthority
}{byStateDir: make(map[string]*projectionCursorAuthority)}

type ProjectionCursorError struct {
	Code              string `json:"code"`
	ProjectionVersion string `json:"projection_version"`
	Recovery          string `json:"recovery"`
	SnapshotMethod    string `json:"snapshot_method"`
	EarliestCursor    string `json:"earliest_cursor,omitempty"`
}

func (d *Daemon) projectionCursorAuthority() (*projectionCursorAuthority, error) {
	projectionCursorAuthorities.Lock()
	defer projectionCursorAuthorities.Unlock()
	if authority := projectionCursorAuthorities.byStateDir[d.stateDir]; authority != nil {
		return authority, nil
	}
	if err := os.MkdirAll(d.stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("projection cursor state: %w", err)
	}
	path := filepath.Join(d.stateDir, "projection-cursor.key")
	key, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, fmt.Errorf("projection cursor entropy: %w", err)
		}
		createErr := persistProjectionCursorKey(d.stateDir, path, key)
		if os.IsExist(createErr) {
			key, err = os.ReadFile(path)
		} else {
			err = createErr
		}
	}
	if err != nil {
		return nil, fmt.Errorf("projection cursor key: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("projection cursor key must have mode 0600")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("projection cursor key must be 32 bytes")
	}
	authority := &projectionCursorAuthority{key: append([]byte{}, key...)}
	projectionCursorAuthorities.byStateDir[d.stateDir] = authority
	return authority, nil
}

func persistProjectionCursorKey(stateDir, path string, key []byte) error {
	file, err := os.CreateTemp(stateDir, ".projection-cursor-*.tmp")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err = file.Chmod(0o600); err == nil {
		_, err = file.Write(key)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(tmp, path); err != nil {
		return err
	}
	dir, openErr := os.Open(stateDir)
	if openErr == nil {
		syncErr := dir.Sync()
		_ = dir.Close()
		if syncErr != nil {
			return syncErr
		}
	}
	return nil
}

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
	items, err := d.loadSessionItems(p.SessionID)
	if err != nil {
		return nil, err
	}
	if len(p.Cursor) == 0 && p.Limit == nil {
		return items, nil
	}
	authority, err := d.projectionCursorAuthority()
	if err != nil {
		return nil, err
	}
	epoch := projectionEpoch(items)
	cursor, err := authority.decode(p.SessionID, epoch, p.Cursor)
	if err != nil {
		return nil, err
	}
	limit := 50
	if p.Limit != nil {
		limit = *p.Limit
	}
	return paginateSessionItems(authority, p.SessionID, epoch, items, cursor, limit)
}

func (d *Daemon) loadSessionItems(id string) ([]SessionItemEvent, error) {
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
	return items, nil
}

func paginateSessionItems(authority *projectionCursorAuthority, sessionID, epoch string, items []SessionItemEvent, cursor, limit int) (map[string]any, error) {
	if cursor < 0 {
		return nil, fmt.Errorf("cursor must be non-negative")
	}
	if cursor > len(items) {
		return nil, authority.cursorFailure("cursor_expired", sessionID, epoch, "fetch a fresh session.items snapshot without cursor")
	}
	if limit < 1 || limit > 200 {
		return nil, fmt.Errorf("limit must be between 1 and 200")
	}
	end := cursor + limit
	if end > len(items) {
		end = len(items)
	}
	result := map[string]any{"data": items[cursor:end], "projection_version": sessionProjectionVersion}
	if end < len(items) {
		result["next_cursor"] = authority.encode(sessionID, epoch, end)
	}
	return result, nil
}

func (a *projectionCursorAuthority) encode(sessionID, epoch string, index int) string {
	return a.encodeClaims(projectionCursorClaims{Version: 1, SessionID: sessionID, Projection: sessionProjectionVersion, Epoch: epoch, Position: index})
}
func (a *projectionCursorAuthority) encodeClaims(claims projectionCursorClaims) string {
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write([]byte(encoded))
	return "cp1." + encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (a *projectionCursorAuthority) decode(sessionID, epoch string, raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var cursor string
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return 0, a.cursorFailure("invalid_cursor", sessionID, epoch, "discard the cursor and request session.items without cursor")
	}
	parts := strings.Split(cursor, ".")
	if len(parts) != 3 || parts[0] != "cp1" {
		return 0, a.cursorFailure("invalid_cursor", sessionID, epoch, "discard the cursor and request session.items without cursor")
	}
	mac := hmac.New(sha256.New, a.key)
	_, _ = mac.Write([]byte(parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return 0, a.cursorFailure("invalid_cursor", sessionID, epoch, "discard the cursor and request session.items without cursor")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, a.cursorFailure("invalid_cursor", sessionID, epoch, "discard the cursor and request session.items without cursor")
	}
	var claims projectionCursorClaims
	if json.Unmarshal(payload, &claims) != nil || claims.Version != 1 || claims.SessionID != sessionID || claims.Projection != sessionProjectionVersion || claims.Position < 0 {
		return 0, a.cursorFailure("invalid_cursor", sessionID, epoch, "discard the cursor and request session.items without cursor")
	}
	if claims.Epoch != epoch {
		return 0, a.cursorFailure("cursor_expired", sessionID, epoch, "fetch a fresh snapshot; the projection epoch changed")
	}
	return claims.Position, nil
}

func (a *projectionCursorAuthority) cursorFailure(code, sessionID, epoch, hint string) *rpc.Error {
	data := ProjectionCursorError{Code: code, ProjectionVersion: sessionProjectionVersion, Recovery: hint, SnapshotMethod: "session.items", EarliestCursor: a.encode(sessionID, epoch, 0)}
	return &rpc.Error{Code: -32010, Message: code, Data: data}
}
func projectionEpoch(items []SessionItemEvent) string {
	source := "empty"
	if len(items) > 0 {
		source = nonempty(items[0].SourceEventID, items[0].SessionID)
	}
	sum := sha256.Sum256([]byte(sessionProjectionVersion + "\x00" + source))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}

type SessionReview struct {
	SessionID         string         `json:"session_id"`
	ProjectionVersion string         `json:"projection_version"`
	SourceCursor      string         `json:"source_cursor"`
	State             string         `json:"state"`
	Summary           string         `json:"summary,omitempty"`
	WaitingReason     string         `json:"waiting_reason,omitempty"`
	Intent            string         `json:"intent,omitempty"`
	SuccessCriteria   []any          `json:"success_criteria"`
	Changes           []*SessionItem `json:"changes"`
	Commands          []*SessionItem `json:"commands"`
	Tools             []*SessionItem `json:"tools"`
	Checks            []*SessionItem `json:"checks"`
	Diagnostics       []*SessionItem `json:"diagnostics"`
	PolicyDecisions   []*SessionItem `json:"policy_decisions"`
	Questions         []*SessionItem `json:"questions"`
	Conflicts         []*SessionItem `json:"conflicts"`
	RiskAndPolicy     []*SessionItem `json:"risk_and_policy"`
	Artifacts         []string       `json:"artifact_ids"`
	Rollback          map[string]any `json:"rollback"`
	Stats             map[string]int `json:"stats"`
}

func (d *Daemon) handleSessionReview(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	items, err := d.loadSessionItems(id)
	if err != nil {
		return nil, err
	}
	authority, err := d.projectionCursorAuthority()
	if err != nil {
		return nil, err
	}
	return projectSessionReview(id, items, authority.encode(id, projectionEpoch(items), len(items))), nil
}

func projectSessionReview(sessionID string, events []SessionItemEvent, sourceCursor string) SessionReview {
	review := SessionReview{
		SessionID: sessionID, ProjectionVersion: sessionProjectionVersion,
		SourceCursor: sourceCursor, State: "active",
		Changes: []*SessionItem{}, Commands: []*SessionItem{}, Tools: []*SessionItem{},
		Checks: []*SessionItem{}, Diagnostics: []*SessionItem{}, PolicyDecisions: []*SessionItem{}, Questions: []*SessionItem{}, Conflicts: []*SessionItem{},
		SuccessCriteria: []any{}, RiskAndPolicy: []*SessionItem{}, Artifacts: []string{}, Rollback: map[string]any{"available": false, "patch_ids": []string{}}, Stats: map[string]int{},
	}
	artifacts := map[string]bool{}
	rollbackIDs := []string{}
	itemsByID := map[string]*SessionItem{}
	itemOrder := []string{}
	for _, event := range events {
		switch event.Type {
		case "turn.started":
			review.State = "active"
			review.Summary = ""
			review.WaitingReason = ""
			if intent := nonempty(stringField(event.Details, "prompt"), stringField(event.Details, "user_prompt")); intent != "" {
				review.Intent = intent
			}
			if criteria, ok := event.Details["success_criteria"].([]any); ok {
				review.SuccessCriteria = append([]any{}, criteria...)
			}
		case "turn.completed":
			review.State = "completed"
			review.Summary = stringField(event.Details, "summary")
		case "turn.failed":
			review.State = nonempty(stringField(event.Details, "status"), "failed")
			review.Summary = nonempty(stringField(event.Details, "summary"), stringField(event.Details, "error"))
		case "thread.completed":
			review.State = "completed"
		}
		item := event.Item
		if item == nil || item.ID == "" {
			continue
		}
		key := item.Type + "\x00" + item.ID
		if _, exists := itemsByID[key]; !exists {
			itemOrder = append(itemOrder, key)
		}
		itemsByID[key] = cloneItem(item)
	}
	for _, key := range itemOrder {
		item := itemsByID[key]
		review.Stats[item.Type]++
		switch item.Type {
		case "file_change", "turn_net_diff":
			review.Changes = append(review.Changes, cloneItem(item))
		case "command_execution":
			review.Commands = append(review.Commands, cloneItem(item))
		case "tool_call":
			tool := stringField(item.Details, "tool")
			kind := stringField(item.Details, "kind")
			if tool == "run" || kind == "command" {
				review.Commands = append(review.Commands, cloneItem(item))
			} else {
				review.Tools = append(review.Tools, cloneItem(item))
			}
			if tool == "goal_check" || kind == "goal_check" || item.Details["check"] == true || item.Details["verification"] == true {
				review.Checks = append(review.Checks, cloneItem(item))
			}
		case "approval", "question":
			review.Questions = append(review.Questions, cloneItem(item))
			if item.Status == "requested" {
				if item.Type == "approval" {
					review.WaitingReason = "waiting_approval"
				} else {
					review.WaitingReason = "waiting_input"
				}
			}
		case "risk_review", "error":
			review.RiskAndPolicy = append(review.RiskAndPolicy, cloneItem(item))
			if item.Type == "risk_review" {
				review.PolicyDecisions = append(review.PolicyDecisions, cloneItem(item))
			} else {
				review.Diagnostics = append(review.Diagnostics, cloneItem(item))
			}
		}
		if item.Type == "file_change" && strings.Contains(strings.ToLower(stringField(item.Details, "error")), "conflict") {
			review.Conflicts = append(review.Conflicts, cloneItem(item))
		}
		if item.Type == "turn_net_diff" {
			for _, patch := range mapSliceField(item.Details, "patches") {
				if stringField(patch, "status") == "applied" && stringField(patch, "rollback_pointer") != "" {
					rollbackIDs = appendUnique(rollbackIDs, stringField(patch, "patch_id"))
				}
			}
		}
		for _, artifactID := range stringSliceField(item.Details, "artifact_ids") {
			if !artifacts[artifactID] {
				artifacts[artifactID] = true
				review.Artifacts = append(review.Artifacts, artifactID)
			}
		}
	}
	if review.WaitingReason != "" && review.State == "active" {
		review.State = "needs_input"
	}
	review.Rollback = map[string]any{"available": len(rollbackIDs) > 0, "patch_ids": rollbackIDs}
	return review
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
		case "ToolRequested":
			status := stringField(ev.Payload, "status")
			if status == "permission_requested" || status == "user_question_requested" {
				kind := "approval"
				if status == "user_question_requested" {
					kind = "question"
				}
				details := copyMap(ev.Payload)
				if request, ok := ev.Payload["request"].(map[string]any); ok {
					mergeMap(details, request)
				}
				id := nonempty(stringField(details, "decision_id"), stringField(details, "question_id"))
				if id == "" {
					id = fallbackItemID(kind, ev, i)
				}
				item := &SessionItem{ID: id, Type: kind, Status: "requested", TaskID: ev.TaskID, StartedAt: ev.Timestamp, Details: details}
				out = append(out, itemEvent("item.started", sessionID, ev, item))
			}

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
			if payloadStatus := stringField(ev.Payload, "status"); payloadStatus == "timed_out" {
				item.Status = payloadStatus
			}
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
				case "approval_resolved", "user_question_resolved":
					kind := "approval"
					id := stringField(ev.Payload, "decision_id")
					if status == "user_question_resolved" {
						kind = "question"
						id = stringField(ev.Payload, "question_id")
					}
					if id == "" {
						id = fallbackItemID(kind, ev, i)
					}
					item := &SessionItem{ID: id, Type: kind, Status: "resolved", TaskID: ev.TaskID, StartedAt: ev.Timestamp, CompletedAt: ev.Timestamp, Details: copyMap(ev.Payload)}
					out = append(out, itemEvent("item.completed", sessionID, ev, item))
				case "post_edit_diagnostics":
					item := &SessionItem{ID: fallbackItemID("diag", ev, i), Type: "error", Status: "failed", TaskID: ev.TaskID, StartedAt: ev.Timestamp, CompletedAt: ev.Timestamp, Details: copyMap(ev.Payload)}
					out = append(out, itemEvent("item.completed", sessionID, ev, item))
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
	copySelected(details, ev.Payload, "status", "prompt", "user_prompt", "model", "agent", "success_criteria")
	if ev.Type != "" {
		details["source_type"] = ev.Type
	}
	return details
}

func mapSliceField(m map[string]any, key string) []map[string]any {
	if m == nil {
		return nil
	}
	switch list := m[key].(type) {
	case []map[string]any:
		return append([]map[string]any{}, list...)
	case []any:
		out := make([]map[string]any, 0, len(list))
		for _, value := range list {
			if item, ok := value.(map[string]any); ok {
				out = append(out, item)
			}
		}
		return out
	}
	return nil
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
