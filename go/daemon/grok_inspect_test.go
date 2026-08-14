package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

const validGrokInspectReportJSON = `{"grokVersion":"1.0.3","channel":"stable","cwd":"/tmp/carina-work","projectRoot":null,"projectTrusted":false,"projectInstructions":[],"permissions":{"sources":[],"loaded":0,"skipped":[],"mcpServerAllowlist":[],"marketplaceAllowlist":[],"managedSettingsExists":false,"managedSettingsActive":false},"loginPolicy":{"disableApiKeyAuth":true,"forceLoginTeamUuid":null,"apiKeyAuthDisabled":true},"hooks":[],"skills":[],"agents":[],"plugins":[],"marketplaces":[],"mcpServers":[],"lspServers":[],"configSources":{"layers":[{"role":"user","path":"/tmp/carina-config.toml"}]},"externalCompat":{"remoteSettingsLoaded":false,"cells":[{"vendor":"cursor","surface":"skills","enabled":false,"source":"env"}]}}`

func TestDecodeGrokCLIInspectReportRequiresExactShape(t *testing.T) {
	if _, ok := decodeGrokCLIInspectReport([]byte(validGrokInspectReportJSON)); !ok {
		t.Fatal("valid report was rejected")
	}
	for name, raw := range map[string]string{
		"unknown top-level field": strings.Replace(validGrokInspectReportJSON, `"channel":"stable"`, `"channel":"stable","futureSurface":{}`, 1),
		"missing required field":  strings.Replace(validGrokInspectReportJSON, `"projectTrusted":false,`, "", 1),
		"duplicate field":         strings.Replace(validGrokInspectReportJSON, `"channel":"stable"`, `"channel":"stable","channel":"beta"`, 1),
		"duplicate nested field":  strings.Replace(validGrokInspectReportJSON, `"loaded":0`, `"loaded":0,"loaded":1`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := decodeGrokCLIInspectReport([]byte(raw)); ok {
				t.Fatal("unsafe report was accepted")
			}
		})
	}
}

func TestGrokCLIInspectNestedSafetyObjectsRequireExactShape(t *testing.T) {
	for name, test := range map[string]struct {
		raw   string
		valid func(json.RawMessage) bool
	}{
		"agent": {
			raw:   `[{"name":"Explore","description":"built in","source":{"type":"builtin","futureSource":false}}]`,
			valid: inspectAgentsBuiltinOnly,
		},
		"permissions": {
			raw:   `{"sources":[],"loaded":0,"skipped":[],"mcpServerAllowlist":[],"marketplaceAllowlist":[],"managedSettingsExists":false,"managedSettingsActive":false,"futurePolicy":false}`,
			valid: inspectPermissionsEmpty,
		},
		"config layer": {
			raw:   `{"layers":[{"role":"user","path":"/tmp/carina-config.toml","futureLayer":false}]}`,
			valid: func(raw json.RawMessage) bool { return inspectConfigLayersCarinaOnly(raw, "/tmp/carina-config.toml") },
		},
		"login policy": {
			raw:   `{"disableApiKeyAuth":true,"forceLoginTeamUuid":null,"apiKeyAuthDisabled":true,"futureAuth":false}`,
			valid: inspectOAuthOnly,
		},
		"external compatibility": {
			raw:   `{"remoteSettingsLoaded":false,"cells":[{"vendor":"cursor","surface":"skills","enabled":false,"source":"env","futureCompat":false}]}`,
			valid: inspectExternalCompatDisabled,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if test.valid(json.RawMessage(test.raw)) {
				t.Fatal("unknown nested safety field was accepted")
			}
		})
	}
}

func TestGrokCLIInspectRejectsUnsafeKnownValues(t *testing.T) {
	if !inspectAgentsBuiltinOnly(json.RawMessage(`[{"name":"Explore","description":"built in","source":{"type":"builtin"}}]`)) {
		t.Fatal("exact built-in agent was rejected")
	}
	if inspectAgentsBuiltinOnly(json.RawMessage(`[{"name":"Explore","description":"user agent","source":{"type":"user","path":"/tmp/agent.md"}}]`)) {
		t.Fatal("external agent was accepted")
	}
	if inspectOAuthOnly(json.RawMessage(`{"disableApiKeyAuth":true,"forceLoginTeamUuid":null,"apiKeyAuthDisabled":false}`)) {
		t.Fatal("resolved API-key authentication was accepted")
	}
	if inspectExternalCompatDisabled(json.RawMessage(`{"remoteSettingsLoaded":false,"cells":[{"vendor":"cursor","surface":"skills","enabled":true,"source":"env"}]}`)) {
		t.Fatal("enabled compatibility surface was accepted")
	}
	if inspectPermissionsEmpty(json.RawMessage(`{"sources":[],"loaded":0,"skipped":[],"mcpServerAllowlist":[],"marketplaceAllowlist":[],"managedSettingsExists":false,"managedSettingsActive":false,"enforced":[{"setting":"alwaysApprove","enabled":true,"source":"managed-settings.json"}]}`)) {
		t.Fatal("enforced permission policy was accepted")
	}
}
