package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (d *Daemon) handleSessionRename(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.SessionID, p.Name = strings.TrimSpace(p.SessionID), strings.TrimSpace(p.Name)
	if p.SessionID == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if p.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	return d.store.Rename(p.SessionID, p.Name)
}
