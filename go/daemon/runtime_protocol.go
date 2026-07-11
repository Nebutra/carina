package daemon

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Nebutra/carina/go/protocolschema"
)

const runtimeProtocolVersion = "1.2.0"

func protocolMajor(v string) (int, error) {
	part := strings.SplitN(strings.TrimPrefix(v, "v"), ".", 2)[0]
	n, err := strconv.Atoi(part)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid protocol version %q", v)
	}
	return n, nil
}
func (d *Daemon) runtimeCapabilities() map[string]any {
	methods := map[string]bool{}
	if d.server != nil {
		for _, desc := range d.server.MethodDescriptors() {
			methods[desc.Method] = true
		}
	}
	return map[string]any{"workflow_control": methods["workflow.run"] && methods["workflow.resume"], "trusted_channels": methods["channel.event.inject"], "extension_inventory": methods["extension.list"], "agent_view": methods["agent.view"], "checkpoint_restore": methods["session.checkpoint.restore"], "worktree_isolation": methods["worktree.create"], "event_unsubscribe": methods["session.events.unsubscribe"], "pagination": methods["session.items"], "event_schema_version": "0.2.0", "tool_call_lifecycle": true, "runtime_stage_timeline": true, "telemetry_format": "carina-telemetry-json-v1", "telemetry_enabled": d.telemetry != nil && d.telemetry.Enabled(), "safe_mode": d.safeMode, "sdk_conformance": true}
}
func (d *Daemon) handleRuntimeInitialize(params json.RawMessage) (any, error) {
	var p struct {
		ProtocolVersion string `json:"protocol_version"`
		ClientName      string `json:"client_name"`
		ClientVersion   string `json:"client_version"`
		SchemaVersion   string `json:"schema_version"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	if p.ProtocolVersion == "" {
		p.ProtocolVersion = "1.0.0"
	}
	if p.SchemaVersion != "" && p.SchemaVersion != "1.2.0" {
		return nil, fmt.Errorf("protocol schema mismatch: client %s, server 1.2.0", p.SchemaVersion)
	}
	clientMajor, err := protocolMajor(p.ProtocolVersion)
	if err != nil {
		return nil, err
	}
	serverMajor, _ := protocolMajor(runtimeProtocolVersion)
	if clientMajor != serverMajor {
		return nil, fmt.Errorf("incompatible protocol major: client %s, server %s", p.ProtocolVersion, runtimeProtocolVersion)
	}
	return map[string]any{"runtime_version": Version, "protocol_version": runtimeProtocolVersion, "schema_version": "1.2.0", "minimum_protocol_version": "1.0.0", "client_name": p.ClientName, "client_version": p.ClientVersion, "capabilities": d.runtimeCapabilities(), "legacy_calls_allowed": true, "legacy_deprecation": "clients should initialize before other calls; enforcement is planned for protocol 2.0"}, nil
}
func (d *Daemon) handleRuntimeCapabilities(json.RawMessage) (any, error) {
	return map[string]any{"runtime_version": Version, "protocol_version": runtimeProtocolVersion, "capabilities": d.runtimeCapabilities()}, nil
}
func (d *Daemon) handleRuntimeSchema(json.RawMessage) (any, error) {
	return protocolschema.JSONSchema(), nil
}
