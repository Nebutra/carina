package daemon

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Nebutra/carina/go/protocolschema"
)

const runtimeProtocolVersion = "1.1.0"

func protocolMajor(v string) (int, error) {
	part := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 2)[0]
	n, err := strconv.Atoi(part)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid protocol version %q", v)
	}
	return n, nil
}
func (d *Daemon) runtimeCapabilities() map[string]any {
	return map[string]any{"workflow_control": true, "trusted_channels": true, "extension_inventory": true, "agent_view": true, "checkpoint_restore": true, "worktree_isolation": true, "telemetry_format": "carina-telemetry-json-v1", "sdk_conformance": true}
}
func (d *Daemon) handleRuntimeInitialize(params json.RawMessage) (any, error) {
	var p struct {
		ProtocolVersion string `json:"protocol_version"`
		ClientName      string `json:"client_name"`
		ClientVersion   string `json:"client_version"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	if p.ProtocolVersion == "" {
		p.ProtocolVersion = "1.0.0"
	}
	clientMajor, err := protocolMajor(p.ProtocolVersion)
	if err != nil {
		return nil, err
	}
	serverMajor, _ := protocolMajor(runtimeProtocolVersion)
	if clientMajor != serverMajor {
		return nil, fmt.Errorf("incompatible protocol major: client %s, server %s", p.ProtocolVersion, runtimeProtocolVersion)
	}
	return map[string]any{"runtime_version": Version, "protocol_version": runtimeProtocolVersion, "minimum_protocol_version": "1.0.0", "client_name": p.ClientName, "client_version": p.ClientVersion, "capabilities": d.runtimeCapabilities()}, nil
}
func (d *Daemon) handleRuntimeCapabilities(json.RawMessage) (any, error) {
	return map[string]any{"runtime_version": Version, "protocol_version": runtimeProtocolVersion, "capabilities": d.runtimeCapabilities()}, nil
}
func (d *Daemon) handleRuntimeSchema(json.RawMessage) (any, error) {
	return protocolschema.JSONSchema(), nil
}
