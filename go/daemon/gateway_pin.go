package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
)

// gatewayWorkspacePin, when set, binds Gateway HTTP and remote (WebSocket/TCP)
// session-bearing work to one local workspace. This is not ACP, not
// multi-tenant SaaS, and does not change the local Unix-socket owner contract.

func (d *Daemon) configureGatewayWorkspacePin(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		d.gatewayWorkspacePin = ""
		return nil
	}
	canon, ok := canonicalExistingDir(raw)
	if !ok {
		return fmt.Errorf("gateway workspace pin is not an existing directory")
	}
	d.gatewayWorkspacePin = canon
	return nil
}

func (d *Daemon) gatewayWorkspaceAllowed(root string) error {
	if d == nil || d.gatewayWorkspacePin == "" {
		return nil
	}
	canon, ok := canonicalExistingDir(strings.TrimSpace(root))
	if !ok {
		return fmt.Errorf("gateway workspace is not an existing directory")
	}
	if canon != d.gatewayWorkspacePin {
		return fmt.Errorf("gateway workspace is pinned and does not match this request")
	}
	return nil
}

func (d *Daemon) gatewaySessionAllowed(sessionID string) error {
	if d == nil || d.gatewayWorkspacePin == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	sess, ok := d.store.Get(sessionID)
	if !ok || sess == nil {
		return fmt.Errorf("session not found")
	}
	return d.gatewayWorkspaceAllowed(sess.WorkspaceRoot)
}

func (d *Daemon) gatewayRunAllowed(runID string) error {
	if d == nil || d.gatewayWorkspacePin == "" || strings.TrimSpace(runID) == "" {
		return nil
	}
	task, ok := d.sched.Get(runID)
	if !ok || task == nil {
		return fmt.Errorf("execution not found")
	}
	return d.gatewaySessionAllowed(task.SessionID)
}

// gatewayRemoteParamsAllowed is the WebSocket/TCP params guard. Unix-socket
// dispatch never calls it.
func (d *Daemon) gatewayRemoteParamsAllowed(method string, params json.RawMessage) error {
	if d == nil || d.gatewayWorkspacePin == "" {
		return nil
	}
	if gatewayPinExemptMethod(method) {
		return nil
	}
	if method == "session.list" {
		return fmt.Errorf("gateway workspace is pinned and does not allow unscoped session.list")
	}
	var p struct {
		SessionID     string `json:"session_id"`
		WorkspaceRoot string `json:"workspace_root"`
		RunID         string `json:"run_id"`
	}
	if len(params) > 0 && string(params) != "null" {
		if err := json.Unmarshal(params, &p); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	bound := false
	if strings.TrimSpace(p.WorkspaceRoot) != "" {
		if err := d.gatewayWorkspaceAllowed(p.WorkspaceRoot); err != nil {
			return err
		}
		bound = true
	}
	if strings.TrimSpace(p.SessionID) != "" {
		if err := d.gatewaySessionAllowed(p.SessionID); err != nil {
			return err
		}
		bound = true
	}
	if strings.TrimSpace(p.RunID) != "" {
		if err := d.gatewayRunAllowed(p.RunID); err != nil {
			return err
		}
		bound = true
	}
	if gatewayPinRequiresBind(method) && !bound {
		return fmt.Errorf("gateway workspace is pinned and this request is not bound to it")
	}
	return nil
}

func gatewayPinExemptMethod(method string) bool {
	if strings.HasPrefix(method, "worker.") || strings.HasPrefix(method, "work.") {
		return true
	}
	switch method {
	case "gateway.hello", "gateway.methods",
		"daemon.status", "daemon.doctor", "daemon.metrics",
		"runtime.initialize", "runtime.capabilities", "runtime.registry_schema",
		"model.list", "telemetry.status", "usage.cost", "backpressure.status",
		"profile.describe":
		return true
	default:
		return false
	}
}

func gatewayPinRequiresBind(method string) bool {
	switch method {
	case "execution.list", "agent.list", "command.list":
		return true
	default:
		return false
	}
}

func (d *Daemon) gatewayDoctor() map[string]any {
	pin := ""
	if d != nil {
		pin = d.gatewayWorkspacePin
	}
	out := map[string]any{
		"ok":            true,
		"workspace_pin": pin != "",
	}
	if pin != "" {
		out["workspace"] = pin
	}
	return out
}
