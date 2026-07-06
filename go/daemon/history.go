package daemon

import (
	"encoding/json"
	"fmt"
)

// handleHistoryRecent returns the most recent shared prompt-history entries
// (across sessions and processes). Read-only.
func (d *Daemon) handleHistoryRecent(params json.RawMessage) (any, error) {
	var p struct {
		Limit int `json:"limit"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	entries, err := d.history.Recent(p.Limit)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []string{}
	}
	return map[string]any{"entries": entries, "count": len(entries)}, nil
}
