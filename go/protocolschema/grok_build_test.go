package protocolschema

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestGrokBuildModelInventorySchema(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	registry, err := Load(filepath.Join(root, "protocol", "jsonrpc", "methods.json"))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := LoadBundle(filepath.Join(root, "protocol", "jsonrpc", "schema-bundle.json"), registry)
	if err != nil {
		t.Fatal(err)
	}

	modelList := bundle.Methods["model.list"]
	params, _ := modelList.Params["properties"].(map[string]any)
	for field, typ := range map[string]string{
		"session_id": "string",
		"model_id":   "string",
		"locale":     "string",
		"refresh":    "boolean",
	} {
		schema, _ := params[field].(map[string]any)
		if schema["type"] != typ {
			t.Errorf("model.list %s schema = %#v", field, schema)
		}
	}
	result, _ := modelList.Result["properties"].(map[string]any)
	readiness, _ := result["readiness"].(map[string]any)
	readinessProperties, _ := readiness["properties"].(map[string]any)
	routeKind, _ := readinessProperties["route_kind"].(map[string]any)
	if values, _ := routeKind["enum"].([]any); !containsAnyString(values, "cli_oauth") {
		t.Errorf("model.list readiness route_kind enum = %#v", values)
	}
	provider, _ := bundle.Defs["model_provider"].(map[string]any)
	properties, _ := provider["properties"].(map[string]any)
	for field, value := range map[string]string{
		"source_kind": "grok-build", "source_app": "grok", "source_route": "cli_oauth",
		"source_auth_mode": "cli_oauth",
	} {
		schema, _ := properties[field].(map[string]any)
		values, _ := schema["enum"].([]any)
		if !containsAnyString(values, value) {
			t.Errorf("%s enum does not contain %q: %#v", field, value, values)
		}
	}
	credentialOwner, _ := properties["source_credential_owner"].(map[string]any)
	if credentialOwner["type"] != "string" {
		t.Errorf("source_credential_owner schema = %#v", credentialOwner)
	}
	action, _ := properties["source_action"].(map[string]any)
	actions, _ := action["enum"].([]any)
	for _, value := range []string{"use_cli_session", "login_cli", "update_cli", "retry_probe", "disabled"} {
		if !containsAnyString(actions, value) {
			t.Errorf("source_action enum does not contain %q: %#v", value, actions)
		}
	}
	model, _ := bundle.Defs["model_inventory_model"].(map[string]any)
	modelProperties, _ := model["properties"].(map[string]any)
	status, _ := modelProperties["status"].(map[string]any)
	if values, _ := status["enum"].([]any); !containsAnyString(values, "auth_error") {
		t.Errorf("model status enum = %#v", values)
	}
	statusReason, _ := modelProperties["status_reason"].(map[string]any)
	if statusReason["type"] != "string" {
		t.Errorf("model status_reason schema = %#v", statusReason)
	}
}
