package daemon

import (
	"encoding/json"
	"fmt"
)

func (d *Daemon) handleMCPInventory(params json.RawMessage) (any, error) {
	var p struct {
		Verbose bool `json:"verbose"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if d.mcp == nil {
		return map[string]any{"servers": []any{}, "count": 0}, nil
	}
	servers := d.mcp.Inventory(p.Verbose)
	return map[string]any{"servers": servers, "count": len(servers)}, nil
}
