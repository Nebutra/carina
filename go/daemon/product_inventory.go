package daemon

import (
	"encoding/json"
	"fmt"
	"sort"
)

func (d *Daemon) handleSkillInventory(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	specs := loadSkillSpecs(sess.WorkspaceRoot)
	rows := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		rows = append(rows, map[string]any{"name": spec.Name, "description": spec.Description, "source": spec.Source, "enabled": spec.Enabled, "user_invocable": spec.UserInvocable, "implicit_invocation": spec.ImplicitInvocation, "allowed_tools": spec.AllowedTools})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i]["name"].(string) < rows[j]["name"].(string) })
	return map[string]any{"skills": rows, "count": len(rows), "mutation": "manage files under ~/.carina/skills or <workspace>/.carina/skills"}, nil
}

func (d *Daemon) handleHookInventory(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	var rows []map[string]any
	for _, hook := range loadHooks(sess.WorkspaceRoot) {
		row := map[string]any{"event": hook.Event, "matcher": hook.Matcher, "source": hook.Source, "enabled": !d.safeMode, "timeout_ms": hook.timeout().Milliseconds()}
		if outcome, ok := d.hookOutcome(hook); ok {
			row["health"] = outcome
		}
		rows = append(rows, row)
	}
	return map[string]any{"hooks": rows, "count": len(rows), "safe_mode": d.safeMode, "supported_events": []string{"PreToolUse", "PostToolUse", "SessionStart", "SessionEnd", "Stop"}, "commands_redacted": true}, nil
}

func (d *Daemon) handleProfileInventory(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	effective, err := d.handleProfileDescribe(params)
	if err != nil {
		return nil, err
	}
	return map[string]any{"effective": effective, "profile": sess.PermissionProfile, "source": "session creation policy", "choices": []map[string]any{{"name": "safe-edit", "risk": "recommended"}, {"name": "full-workspace", "risk": "elevated; create a new session explicitly"}}, "mutation": "read-only inventory; profile changes require an explicit governed session boundary"}, nil
}

func (d *Daemon) handleConfigInventory(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	d.planMu.Lock()
	planMode := d.planMode[sess.SessionID]
	d.planMu.Unlock()
	effective := map[string]any{"safe_mode": d.safeMode, "sandbox_commands": d.sandbox.Load(), "interactive_approval": d.interactiveApproval.Load(), "permission_profile": sess.PermissionProfile, "plan_mode": planMode, "model": sess.NextModel, "reasoning_effort": sess.NextReasoningEffort}
	choices := map[string]any{"interaction_mode": []string{"build", "plan"}, "reasoning_effort": []string{"default", "low", "medium", "high", "max", "auto"}, "sandbox": []string{"on", "off (daemon policy may forbid)"}}
	return map[string]any{"effective": effective, "sources": map[string]any{"session": "session store", "runtime": "daemon config/env/CLI (effective value shown)"}, "choices": choices, "mutation": "use dedicated governed commands; this inventory never mutates configuration"}, nil
}
