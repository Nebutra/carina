package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	harnessWorkspaceFileCap  = 1200
	harnessWorkspaceDepthCap = 12
	harnessPromptBytesCap    = 1 << 20
)

// handleHarnessWorkspaceTree is the bounded, session-scoped file projection
// exposed to authenticated Web/Tauri clients. The local workspace.tree RPC
// keeps its existing array contract for TUI compatibility.
func (d *Daemon) handleHarnessWorkspaceTree(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		TenantID  string `json:"tenant_id"`
		MaxFiles  int    `json:"max_files"`
		MaxDepth  int    `json:"max_depth"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, err := d.requireNamedSession(p.SessionID, params)
	if err != nil {
		return nil, err
	}
	if err := d.gatewaySessionAllowed(sess.SessionID); err != nil {
		return nil, err
	}
	decision, err := d.kern.Request(sess.SessionID, "FileRead", sess.WorkspaceRoot, "")
	if err != nil {
		return nil, err
	}
	if decision.Decision != "allowed" {
		return nil, fmt.Errorf("denied: %s", decision.Reason)
	}
	maxFiles, err := boundedHarnessLimit("max_files", p.MaxFiles, harnessWorkspaceFileCap)
	if err != nil {
		return nil, err
	}
	maxDepth, err := boundedHarnessLimit("max_depth", p.MaxDepth, harnessWorkspaceDepthCap)
	if err != nil {
		return nil, err
	}
	files, truncated, err := d.tools.ScanBounded(sess.WorkspaceRoot, maxFiles, maxDepth)
	if err != nil {
		return nil, err
	}
	return map[string]any{"files": files, "truncated": truncated}, nil
}

func boundedHarnessLimit(name string, requested, ceiling int) (int, error) {
	if requested < 0 {
		return 0, fmt.Errorf("%s must be >= 0", name)
	}
	if requested == 0 {
		return ceiling, nil
	}
	if requested > ceiling {
		return ceiling, nil
	}
	return requested, nil
}

// handleHarnessSubmit is the only remote write composition used by the
// Web/Tauri Harness. It deliberately does not expose the lower-level local
// session, upload, or execution mutation APIs over Gateway transports.
func (d *Daemon) handleHarnessSubmit(params json.RawMessage) (any, error) {
	var p struct {
		SessionID          string              `json:"session_id"`
		WorkspaceRoot      string              `json:"workspace_root"`
		Profile            string              `json:"profile"`
		Prompt             string              `json:"prompt"`
		Agent              string              `json:"agent"`
		Locale             string              `json:"locale"`
		ClientSubmissionID string              `json:"client_submission_id"`
		TenantID           string              `json:"tenant_id"`
		InputMedia         []gatewayMediaInput `json:"input_media"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.Prompt = strings.TrimSpace(p.Prompt)
	if p.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if len(p.Prompt) > harnessPromptBytesCap {
		return nil, fmt.Errorf("prompt exceeds %d byte harness limit", harnessPromptBytesCap)
	}
	tenantID := sessionstore.NormalizeTenantID(p.TenantID)
	var sess *sessionstore.Session
	if strings.TrimSpace(p.SessionID) != "" {
		var err error
		sess, err = d.lookupSession(strings.TrimSpace(p.SessionID), tenantID)
		if err != nil {
			return nil, err
		}
		if err := d.gatewaySessionAllowed(sess.SessionID); err != nil {
			return nil, err
		}
		if root := strings.TrimSpace(p.WorkspaceRoot); root != "" {
			canonical, ok := canonicalExistingDir(root)
			if !ok || canonical != sess.WorkspaceRoot {
				return nil, fmt.Errorf("workspace_root does not match session")
			}
		}
	} else {
		root := strings.TrimSpace(p.WorkspaceRoot)
		if root == "" {
			return nil, fmt.Errorf("workspace_root is required for a new harness session")
		}
		if err := d.gatewayWorkspaceAllowed(root); err != nil {
			return nil, err
		}
		profile := strings.TrimSpace(p.Profile)
		if profile == "" {
			profile = "safe-edit"
		}
		created, err := d.handleSessionCreate(mustRaw(map[string]any{
			"workspace_root": root,
			"profile":        profile,
			"tenant_id":      tenantID,
		}))
		if err != nil {
			return nil, err
		}
		var ok bool
		sess, ok = created.(*sessionstore.Session)
		if !ok || sess == nil {
			return nil, fmt.Errorf("create harness session returned %T", created)
		}
	}

	inputMediaRefs, err := ingestGatewayMedia(d.artifacts, sess.SessionID, p.InputMedia)
	if err != nil {
		return nil, err
	}
	agent := strings.TrimSpace(p.Agent)
	if agent == "" {
		agent = defaultInteractiveAgent
	}
	submit := map[string]any{
		"session_id": sess.SessionID,
		"prompt":     p.Prompt,
		"agent":      agent,
		"mode":       "background",
		"tenant_id":  tenantID,
	}
	if locale := strings.TrimSpace(p.Locale); locale != "" {
		submit["locale"] = locale
	}
	if id := strings.TrimSpace(p.ClientSubmissionID); id != "" {
		submit["client_submission_id"] = id
	}
	if len(inputMediaRefs) > 0 {
		submit["input_media_refs"] = inputMediaRefs
	}
	taskAny, err := d.handleTaskSubmit(mustRaw(submit))
	if err != nil {
		return nil, err
	}
	task, ok := taskAny.(*scheduler.ExecutionRun)
	if !ok || task == nil {
		return nil, fmt.Errorf("submit harness execution returned %T", taskAny)
	}
	return map[string]any{
		"session": d.projectSession(sess, task),
		"execution": map[string]any{
			"task_id": task.RunID,
			"status":  task.Status,
		},
	}, nil
}
