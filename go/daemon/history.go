package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
)

// handleHistoryRecent returns prompt history from the requested runtime scope.
// The global default preserves compatibility for older clients; interactive
// frontends should request workspace scope to avoid cross-project recall.
func (d *Daemon) handleHistoryRecent(params json.RawMessage) (any, error) {
	var p struct {
		Limit     int    `json:"limit"`
		Scope     string `json:"scope"`
		SessionID string `json:"session_id"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	scope := strings.ToLower(strings.TrimSpace(p.Scope))
	if scope == "" {
		scope = "global"
	}
	if scope != "global" && scope != "workspace" && scope != "session" {
		return nil, fmt.Errorf("invalid history scope %q: want session, workspace, or global", p.Scope)
	}
	var workspaceRoot, nextModel, nextReasoningEffort string
	if scope != "global" {
		sess, ok := d.store.Get(p.SessionID)
		if !ok {
			return nil, fmt.Errorf("unknown session %s", p.SessionID)
		}
		workspaceRoot = sess.WorkspaceRoot
		nextModel = sess.NextModel
		nextReasoningEffort = sess.NextReasoningEffort
	}
	stored, err := d.history.RecentEntries(0)
	if err != nil {
		return nil, err
	}
	entries := make([]string, 0, minInt(p.Limit, len(stored)))
	for _, entry := range stored {
		include := false
		switch scope {
		case "global":
			include = true
		case "workspace":
			include = entry.WorkspaceRoot == workspaceRoot
		case "session":
			include = entry.SessionID == p.SessionID
		}
		if include {
			entries = append(entries, entry.Text)
		}
	}
	if len(entries) > p.Limit {
		entries = entries[len(entries)-p.Limit:]
	}
	if entries == nil {
		entries = []string{}
	}
	return map[string]any{"entries": entries, "count": len(entries), "scope": scope, "next_model": nextModel, "next_reasoning_effort": nextReasoningEffort}, nil
}
