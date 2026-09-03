package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/localruntime"
	sessionstore "github.com/Nebutra/carina/go/session-store"
	carinatelemetry "github.com/Nebutra/carina/go/telemetry"
)

func callerTenantID(params json.RawMessage) string {
	if len(params) == 0 || string(params) == "null" {
		return sessionstore.LocalTenantID
	}
	var p struct {
		TenantID string `json:"tenant_id"`
	}
	if json.Unmarshal(params, &p) != nil {
		return sessionstore.LocalTenantID
	}
	return sessionstore.NormalizeTenantID(p.TenantID)
}

func (d *Daemon) lookupSession(sessionID, tenantID string) (*sessionstore.Session, error) {
	if d == nil || d.store == nil {
		return nil, fmt.Errorf("unknown session %s", sessionID)
	}
	sess, ok := d.store.Visible(sessionID, tenantID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", sessionID)
	}
	return sess, nil
}

func (d *Daemon) requireSession(params json.RawMessage) (*sessionstore.Session, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	return d.lookupSession(id, callerTenantID(params))
}

func (d *Daemon) requireNamedSession(sessionID string, params json.RawMessage) (*sessionstore.Session, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	return d.lookupSession(sessionID, callerTenantID(params))
}

func (d *Daemon) sessionTenantID(sessionID string) string {
	if d == nil || d.store == nil || strings.TrimSpace(sessionID) == "" {
		return sessionstore.LocalTenantID
	}
	sess, ok := d.store.Get(sessionID)
	if !ok || sess == nil {
		return sessionstore.LocalTenantID
	}
	return sessionstore.NormalizeTenantID(sess.TenantID)
}

func tenantSessionRequiresSandbox(sess *sessionstore.Session) bool {
	if sess == nil {
		return false
	}
	return !sessionstore.SameTenant(sess.TenantID, sessionstore.LocalTenantID)
}

func (d *Daemon) commandSandbox(sess *sessionstore.Session) bool {
	if tenantSessionRequiresSandbox(sess) {
		return true
	}
	return d != nil && d.sandbox.Load()
}

func (d *Daemon) sessionAttribution(sessionID string, attr carinatelemetry.Attribution) carinatelemetry.Attribution {
	attr.SessionID = sessionID
	attr.TenantID = d.sessionTenantID(sessionID)
	if d != nil && d.store != nil {
		if sess, ok := d.store.Get(sessionID); ok && sess != nil && attr.WorkspaceID == "" {
			attr.WorkspaceID = sess.WorkspaceID
		}
	}
	return attr
}

func (d *Daemon) createSessionForTenant(tenantID, workspaceRoot, profile, approvalMode string) (*sessionstore.Session, error) {
	tenantID = sessionstore.NormalizeTenantID(tenantID)
	if d.runtimeSpec != nil && d.runtimeSpec.Mode == localruntime.ModeWorkspace {
		return d.store.CreateSessionModeForWorkspaceTenant(d.runtimeSpec.Workspace.ID, workspaceRoot, profile, approvalMode, tenantID)
	}
	return d.store.CreateSessionModeForTenant(tenantID, workspaceRoot, profile, approvalMode)
}
