package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

type gatewayHTTP struct {
	d              *Daemon
	allowedOrigins []string
}

func (d *Daemon) runGatewayHTTP(addr string, allowedOrigins []string) error {
	if d.gatewayTokens == nil {
		return fmt.Errorf("gateway http requires gateway_token_signing_key_file")
	}
	mux := http.NewServeMux()
	h := &gatewayHTTP{d: d, allowedOrigins: allowedOrigins}
	mux.HandleFunc("/v1/models", h.handleModels)
	mux.HandleFunc("/v1/chat/completions", h.handleChatCompletions)
	mux.HandleFunc("/v1/responses", h.handleResponses)
	mux.HandleFunc("/tools/invoke", h.handleToolsInvoke)
	mux.HandleFunc("/plugins/", h.handlePluginHTTP)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("gateway http listen %s: %w", addr, err)
	}
	srv := &http.Server{Handler: mux}
	d.mu.Lock()
	d.gatewayHTTPServers = append(d.gatewayHTTPServers, srv)
	d.mu.Unlock()
	err = srv.Serve(ln)
	if err == nil || err == http.ErrServerClosed || strings.Contains(err.Error(), "use of closed network connection") {
		return nil
	}
	return err
}

func (h *gatewayHTTP) handleModels(w http.ResponseWriter, r *http.Request) {
	if h.preflight(w, r, http.MethodGet) {
		return
	}
	if r.Method != http.MethodGet {
		h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	if _, ok := h.authorize(w, r, "/v1/models", rpc.ScopeRead); !ok {
		return
	}
	root := strings.TrimSpace(r.URL.Query().Get("workspace_root"))
	data := []map[string]any{
		gatewayModel("carina", "Default Carina agent target"),
		gatewayModel("carina/default", "Default Carina agent target"),
	}
	for _, agent := range sortedAgentInfos(loadAgentSpecs(root), false) {
		data = append(data, gatewayModel("carina/"+agent.Name, agent.Description))
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (h *gatewayHTTP) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if h.preflight(w, r, http.MethodPost) {
		return
	}
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if _, ok := h.authorize(w, r, "/v1/chat/completions", rpc.ScopeWrite); !ok {
		return
	}
	var req chatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	prompt := chatPrompt(req.Messages)
	if strings.TrimSpace(prompt) == "" {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "messages are required")
		return
	}
	if req.Stream {
		h.handleChatCompletionsStream(w, r, req, prompt)
		return
	}
	task, sessionID, err := h.submitAgentTask(r, req.Model, prompt, req.Metadata, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "submit_failed", err.Error())
		return
	}
	task = h.waitTask(task.TaskID, metadataWait(req.Metadata))
	content := taskMessage(task, sessionID)
	resp := map[string]any{
		"id":      "chatcmpl_" + task.TaskID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   normalizedGatewayModel(req.Model),
		"choices": []map[string]any{{
			"index":         0,
			"finish_reason": "stop",
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
		}},
		"carina": gatewayTaskMeta(task, sessionID),
	}
	w.Header().Set("X-Carina-Task-ID", task.TaskID)
	w.Header().Set("X-Carina-Session-ID", sessionID)
	h.writeJSON(w, http.StatusOK, resp)
}

func (h *gatewayHTTP) handleChatCompletionsStream(w http.ResponseWriter, r *http.Request, req chatCompletionRequest, prompt string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, "streaming_unavailable", "response writer does not support streaming")
		return
	}
	task, sessionID, err := h.submitAgentTask(r, req.Model, prompt, req.Metadata, "")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "submit_failed", err.Error())
		return
	}

	stream := &chatCompletionSSE{
		ctx:     r.Context(),
		w:       w,
		flusher: flusher,
		id:      "chatcmpl_" + task.TaskID,
		created: time.Now().Unix(),
		model:   normalizedGatewayModel(req.Model),
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Carina-Task-ID", task.TaskID)
	w.Header().Set("X-Carina-Session-ID", sessionID)
	w.WriteHeader(http.StatusOK)

	// Carina currently emits discrete governed task/session events, not model
	// token chunks. Preserve that truth in the stream: role first, then honest
	// event/status deltas, then the final task summary.
	if err := stream.chunk(map[string]any{"role": "assistant"}, nil); err != nil {
		return
	}

	const pollInterval = 100 * time.Millisecond
	const heartbeatInterval = 15 * time.Second
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	cursor := 0
	lastStatus := ""
	eventReadDegraded := false
	for {
		current, exists := h.d.sched.Get(task.TaskID)
		if !exists {
			current = &scheduler.Task{TaskID: task.TaskID, Status: "queued"}
		}
		if current.Status != lastStatus {
			if err := stream.content(fmt.Sprintf("Carina task status: %s.\n", current.Status)); err != nil {
				return
			}
			lastStatus = current.Status
		}

		events, nextCursor, err := h.gatewayTaskEvents(sessionID, task.TaskID, cursor)
		if err != nil {
			if !eventReadDegraded {
				if writeErr := stream.content("Carina event replay is temporarily unavailable; task status remains live.\n"); writeErr != nil {
					return
				}
				eventReadDegraded = true
			}
		} else {
			cursor = nextCursor
			for _, event := range events {
				if delta := gatewayEventDelta(event); delta != "" {
					if err := stream.content(delta); err != nil {
						return
					}
				}
			}
		}

		if gatewayTaskTerminal(current.Status) {
			if content := strings.TrimSpace(taskMessage(current, sessionID)); content != "" {
				if err := stream.content(content); err != nil {
					return
				}
			}
			finish := "stop"
			if err := stream.chunk(map[string]any{}, &finish); err != nil {
				return
			}
			_ = stream.done()
			return
		}

		select {
		case <-r.Context().Done():
			return
		case <-poll.C:
		case <-heartbeat.C:
			if err := stream.comment("keep-alive"); err != nil {
				return
			}
		}
	}
}

type chatCompletionSSE struct {
	ctx     context.Context
	w       io.Writer
	flusher http.Flusher
	id      string
	created int64
	model   string
}

func (s *chatCompletionSSE) content(content string) error {
	if content == "" {
		return nil
	}
	return s.chunk(map[string]any{"content": content}, nil)
}

func (s *chatCompletionSSE) chunk(delta map[string]any, finishReason *string) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	chunk := map[string]any{
		"id":      s.id,
		"object":  "chat.completion.chunk",
		"created": s.created,
		"model":   s.model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
	}
	raw, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", raw); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *chatCompletionSSE) comment(comment string) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", comment); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *chatCompletionSSE) done() error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if _, err := io.WriteString(s.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (h *gatewayHTTP) gatewayTaskEvents(sessionID, taskID string, since int) ([]itemAuditEvent, int, error) {
	raw, err := h.d.kern.ReadEvents(sessionID)
	if err != nil {
		return nil, since, err
	}
	var all []itemAuditEvent
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, since, fmt.Errorf("decode gateway task events: %w", err)
	}
	if since < 0 {
		since = 0
	}
	if since > len(all) {
		since = len(all)
	}
	filtered := make([]itemAuditEvent, 0, len(all)-since)
	for _, event := range all[since:] {
		if event.TaskID == taskID {
			filtered = append(filtered, event)
		}
	}
	return filtered, len(all), nil
}

func gatewayEventDelta(event itemAuditEvent) string {
	switch event.Type {
	case "TaskCreated":
		if status := gatewayPayloadString(event.Payload, "status"); status != "" {
			return fmt.Sprintf("Carina task event: %s.\n", sanitizeGatewayDelta(status, 120))
		}
		return "Carina task accepted.\n"
	case "ModelRequested":
		return "Carina requested a model response.\n"
	case "RoutingOutcome":
		status := sanitizeGatewayDelta(gatewayPayloadString(event.Payload, "status"), 80)
		if status == "" {
			return "Carina model routing completed.\n"
		}
		return fmt.Sprintf("Carina model routing completed: %s.\n", status)
	case "ModelResponded":
		if gatewayPayloadString(event.Payload, "error") != "" {
			return "Carina model request failed.\n"
		}
		return "Carina received a model response.\n"
	case "CommandStarted":
		command := sanitizeGatewayDelta(gatewayPayloadString(event.Payload, "command"), 240)
		if command == "" {
			return "Carina started a command.\n"
		}
		return fmt.Sprintf("Carina started command: %s\n", command)
	case "CommandOutput":
		chunk := sanitizeGatewayDelta(gatewayPayloadString(event.Payload, "chunk"), 400)
		if chunk == "" {
			return ""
		}
		return "Carina command output:\n" + chunk + "\n"
	case "CommandExited":
		if code, ok := event.Payload["exit_code"]; ok {
			return fmt.Sprintf("Carina command exited with code %v.\n", code)
		}
		return "Carina command exited.\n"
	case "ContextCompacted":
		return "Carina compacted the task context.\n"
	case "ToolApproved":
		return "Carina tool action approved.\n"
	case "ToolDenied":
		return "Carina tool action denied.\n"
	case "PatchProposed":
		return "Carina proposed a workspace patch.\n"
	case "PatchApplied":
		return "Carina applied a workspace patch.\n"
	case "PatchFailed":
		return "Carina workspace patch failed.\n"
	case "RollbackStarted":
		return "Carina started rolling back a workspace patch.\n"
	case "RollbackCompleted":
		return "Carina rolled back a workspace patch.\n"
	default:
		return ""
	}
}

func gatewayPayloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, _ := payload[key].(string)
	return value
}

func sanitizeGatewayDelta(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	for _, r := range value {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	runes := []rune(b.String())
	if maxRunes > 0 && len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "..."
	}
	return string(runes)
}

func (h *gatewayHTTP) handleResponses(w http.ResponseWriter, r *http.Request) {
	if h.preflight(w, r, http.MethodPost) {
		return
	}
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if _, ok := h.authorize(w, r, "/v1/responses", rpc.ScopeWrite); !ok {
		return
	}
	var req responsesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	prompt := responsePrompt(req.Input)
	if strings.TrimSpace(prompt) == "" {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "input is required")
		return
	}
	task, sessionID, err := h.submitAgentTask(r, req.Model, prompt, req.Metadata, req.PreviousResponseID)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "submit_failed", err.Error())
		return
	}
	task = h.waitTask(task.TaskID, metadataWait(req.Metadata))
	respID := "resp_" + task.TaskID
	h.d.mu.Lock()
	h.d.gatewayResponses[respID] = sessionID
	h.d.mu.Unlock()
	resp := map[string]any{
		"id":     respID,
		"object": "response",
		"status": task.Status,
		"model":  normalizedGatewayModel(req.Model),
		"output": []map[string]any{{
			"type": "message",
			"role": "assistant",
			"content": []map[string]any{{
				"type": "output_text",
				"text": taskMessage(task, sessionID),
			}},
		}},
		"carina": gatewayTaskMeta(task, sessionID),
	}
	w.Header().Set("X-Carina-Task-ID", task.TaskID)
	w.Header().Set("X-Carina-Session-ID", sessionID)
	h.writeJSON(w, http.StatusOK, resp)
}

func (h *gatewayHTTP) handleToolsInvoke(w http.ResponseWriter, r *http.Request) {
	if h.preflight(w, r, http.MethodPost) {
		return
	}
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	if _, ok := h.authorize(w, r, "/tools/invoke", rpc.ScopeRead); !ok {
		return
	}
	var req toolInvokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	method := normalizeToolInvokeMethod(req.Tool, req.Action)
	result, err := h.invokeReadOnlyTool(method, req.Args)
	if err != nil {
		if sid := stringArg(req.Args, "session_id"); sid != "" {
			h.d.record(sid, "TaskCreated", "", "go", map[string]any{"status": "tools_invoke_denied", "method": method, "reason": err.Error()}, "")
		}
		h.writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": err.Error(), "method": method})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "method": method, "result": result})
}

func (h *gatewayHTTP) handlePluginHTTP(w http.ResponseWriter, r *http.Request) {
	if h.preflight(w, r, r.Method) {
		return
	}
	if _, ok := h.authorize(w, r, "/plugins/*", rpc.ScopeRead); !ok {
		return
	}
	h.writeJSON(w, http.StatusNotImplemented, map[string]any{
		"ok":    false,
		"error": "plugin HTTP routes are not installed; request-local Gateway scope is required",
	})
}

func (h *gatewayHTTP) authorize(w http.ResponseWriter, r *http.Request, route string, required rpc.Scope) (rpc.GatewayTokenClaims, bool) {
	if !h.applyOrigin(w, r) {
		h.writeError(w, http.StatusForbidden, "origin_forbidden", "gateway http origin not allowed")
		return rpc.GatewayTokenClaims{}, false
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		h.writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
		return rpc.GatewayTokenClaims{}, false
	}
	claims, err := h.d.gatewayTokens.Verify(token, "http")
	if err != nil {
		h.writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return rpc.GatewayTokenClaims{}, false
	}
	if !rpc.RouteAllowed(claims.Routes, route) {
		h.writeError(w, http.StatusForbidden, "route_forbidden", "gateway token route not granted")
		return rpc.GatewayTokenClaims{}, false
	}
	if !gatewayHTTPScopeAllowed(required, claims.Scopes) {
		h.writeError(w, http.StatusForbidden, "scope_forbidden", "gateway token scope not granted")
		return rpc.GatewayTokenClaims{}, false
	}
	return claims, true
}

func (h *gatewayHTTP) preflight(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method != http.MethodOptions {
		return false
	}
	if !h.applyOrigin(w, r) {
		h.writeError(w, http.StatusForbidden, "origin_forbidden", "gateway http origin not allowed")
		return true
	}
	w.Header().Set("Access-Control-Allow-Methods", method+", OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Carina-Workspace")
	w.WriteHeader(http.StatusNoContent)
	return true
}

func (h *gatewayHTTP) applyOrigin(w http.ResponseWriter, r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	for _, allowed := range h.allowedOrigins {
		if strings.EqualFold(strings.TrimSpace(allowed), origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			return true
		}
	}
	return false
}

func (h *gatewayHTTP) submitAgentTask(r *http.Request, model, prompt string, metadata map[string]any, previousResponseID string) (*scheduler.Task, string, error) {
	agent, err := agentFromGatewayModel(model)
	if err != nil {
		return nil, "", err
	}
	sessionID := metadataString(metadata, "session_id")
	if sessionID == "" && previousResponseID != "" {
		h.d.mu.Lock()
		sessionID = h.d.gatewayResponses[previousResponseID]
		h.d.mu.Unlock()
	}
	if sessionID == "" {
		root := metadataString(metadata, "workspace_root")
		if root == "" {
			root = strings.TrimSpace(r.Header.Get("X-Carina-Workspace"))
		}
		if root == "" {
			var err error
			root, err = os.Getwd()
			if err != nil {
				return nil, "", err
			}
		}
		sess, err := h.createGatewaySession(root)
		if err != nil {
			return nil, "", err
		}
		sessionID = sess.SessionID
	}
	taskAny, err := h.d.handleTaskSubmit(mustRaw(map[string]any{
		"session_id": sessionID,
		"prompt":     prompt,
		"agent":      agent,
	}))
	if err != nil {
		return nil, "", err
	}
	task, ok := taskAny.(*scheduler.Task)
	if !ok {
		raw, _ := json.Marshal(taskAny)
		var decoded scheduler.Task
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, "", fmt.Errorf("decode submitted task: %w", err)
		}
		task = &decoded
	}
	return task, sessionID, nil
}

func (h *gatewayHTTP) createGatewaySession(root string) (*sessionstore.Session, error) {
	sessAny, err := h.d.handleSessionCreate(mustRaw(map[string]any{
		"workspace_root": root,
		"profile":        "safe-edit",
	}))
	if err != nil {
		return nil, err
	}
	sess, ok := sessAny.(*sessionstore.Session)
	if ok {
		return sess, nil
	}
	raw, _ := json.Marshal(sessAny)
	var decoded sessionstore.Session
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, fmt.Errorf("decode created session: %w", err)
	}
	return &decoded, nil
}

func (h *gatewayHTTP) waitTask(taskID string, seconds float64) *scheduler.Task {
	if seconds <= 0 {
		if task, ok := h.d.sched.Get(taskID); ok {
			return task
		}
		return &scheduler.Task{TaskID: taskID, Status: "queued"}
	}
	if seconds > 30 {
		seconds = 30
	}
	deadline := time.Now().Add(time.Duration(seconds * float64(time.Second)))
	for {
		task, ok := h.d.sched.Get(taskID)
		if ok && gatewayTaskTerminal(task.Status) {
			return task
		}
		if time.Now().After(deadline) {
			if ok {
				return task
			}
			return &scheduler.Task{TaskID: taskID, Status: "queued"}
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (h *gatewayHTTP) invokeReadOnlyTool(method string, args map[string]any) (any, error) {
	switch method {
	case "daemon.status":
		return h.d.handleStatus(nil)
	case "daemon.metrics":
		return h.d.handleMetrics(nil)
	case "daemon.doctor":
		return h.d.handleDoctor(nil)
	case "agent.list":
		return h.d.handleAgentList(mustRaw(args))
	case "command.list":
		return h.d.handleCommandList(mustRaw(args))
	case "session.list":
		return h.d.handleSessionList(nil)
	case "session.get":
		return h.d.handleSessionGet(mustRaw(args))
	case "workspace.tree":
		return h.d.handleWorkspaceTree(mustRaw(args))
	case "workspace.search":
		return h.d.handleWorkspaceSearch(mustRaw(args))
	case "workspace.file.get":
		return h.d.handleFileGet(mustRaw(args))
	default:
		return nil, fmt.Errorf("tool %q is not in the read-only invoke allowlist", method)
	}
}

func gatewayModel(id, description string) map[string]any {
	return map[string]any{
		"id":          id,
		"object":      "model",
		"created":     0,
		"owned_by":    "nebutra",
		"description": description,
	}
}

type chatCompletionRequest struct {
	Model    string           `json:"model"`
	Messages []gatewayMessage `json:"messages"`
	Stream   bool             `json:"stream"`
	Metadata map[string]any   `json:"metadata"`
}

type gatewayMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type responsesRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	PreviousResponseID string          `json:"previous_response_id"`
	Metadata           map[string]any  `json:"metadata"`
}

type toolInvokeRequest struct {
	Tool           string         `json:"tool"`
	Action         string         `json:"action"`
	Args           map[string]any `json:"args"`
	AgentID        string         `json:"agent_id"`
	SessionKey     string         `json:"session_key"`
	IdempotencyKey string         `json:"idempotency_key"`
}

func chatPrompt(messages []gatewayMessage) string {
	var b strings.Builder
	for _, msg := range messages {
		text := contentText(msg.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		fmt.Fprintf(&b, "%s: %s\n", role, text)
	}
	return strings.TrimSpace(b.String())
}

func responsePrompt(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	if text := contentText(input); strings.TrimSpace(text) != "" {
		return strings.TrimSpace(text)
	}
	var messages []gatewayMessage
	if err := json.Unmarshal(input, &messages); err == nil {
		return chatPrompt(messages)
	}
	return strings.TrimSpace(string(input))
}

func contentText(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			if part.Text != "" {
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(part.Text)
			}
		}
		return b.String()
	}
	var obj struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj.Text
	}
	return ""
}

func agentFromGatewayModel(model string) (string, error) {
	model = normalizedGatewayModel(model)
	switch model {
	case "carina", "carina/default":
		return "build", nil
	}
	if strings.HasPrefix(model, "carina/") {
		agent := strings.TrimPrefix(model, "carina/")
		if agent == "" {
			return "", fmt.Errorf("invalid Carina agent target")
		}
		return agent, nil
	}
	return "", fmt.Errorf("model must be a Carina agent target such as carina/default or carina/build")
}

func normalizedGatewayModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "carina/default"
	}
	return model
}

func gatewayTaskMeta(task *scheduler.Task, sessionID string) map[string]any {
	return map[string]any{
		"task_id":    task.TaskID,
		"session_id": sessionID,
		"status":     task.Status,
	}
}

func taskMessage(task *scheduler.Task, sessionID string) string {
	if gatewayTaskTerminal(task.Status) && strings.TrimSpace(task.Summary) != "" {
		return task.Summary
	}
	return fmt.Sprintf("Carina task %s submitted in session %s (status: %s).", task.TaskID, sessionID, task.Status)
}

func gatewayTaskTerminal(status string) bool {
	switch status {
	case "completed", "degraded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func normalizeToolInvokeMethod(tool, action string) string {
	tool = strings.TrimSpace(tool)
	action = strings.TrimSpace(action)
	if action == "" {
		return tool
	}
	if tool == "" {
		return action
	}
	return tool + "." + action
}

func gatewayHTTPScopeAllowed(required rpc.Scope, allowed []rpc.Scope) bool {
	for _, scope := range allowed {
		if scope == required {
			return true
		}
	}
	return false
}

func bearerToken(header string) string {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func metadataWait(metadata map[string]any) float64 {
	if metadata == nil {
		return 0
	}
	switch v := metadata["wait_seconds"].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		n, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n
	default:
		return 0
	}
}

func stringArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func mustRaw(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

func (h *gatewayHTTP) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *gatewayHTTP) writeError(w http.ResponseWriter, status int, typ, message string) {
	h.writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"type":    typ,
			"message": message,
		},
	})
}
