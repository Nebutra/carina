package protocolschema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type eventRegistry struct {
	Types []struct {
		Name    string   `json:"name"`
		Payload []string `json:"payload"`
	} `json:"types"`
}

type eventSchema struct {
	Properties struct {
		Type struct {
			Enum []string `json:"enum"`
		} `json:"type"`
	} `json:"properties"`
}

func TestCheckedInRegistryAndSchema(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "..", "..", "protocol", "jsonrpc", "methods.json")
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(Methods(r)) < 100 {
		t.Fatalf("unexpectedly small registry: %d", len(Methods(r)))
	}
	schema := JSONSchema()
	if schema["$schema"] == nil {
		t.Fatal("schema missing dialect")
	}
	bundle, err := LoadBundle(filepath.Join(filepath.Dir(file), "..", "..", "protocol", "jsonrpc", "schema-bundle.json"), r)
	if err != nil {
		t.Fatal(err)
	}
	generated := GenerateTypeScript(bundle)
	if !strings.Contains(generated, "session.fork") || !strings.Contains(generated, "session.events.unsubscribe") {
		t.Fatal("generated TS bundle missing stable methods")
	}
}

func TestSchemaBundleCoversSDKEventModeAndGoalContract(t *testing.T) {
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

	stream := bundle.Methods["session.events.stream"]
	params, _ := stream.Params["properties"].(map[string]any)
	if _, ok := params["event_mode"]; !ok {
		t.Fatal("session.events.stream schema must accept event_mode")
	}
	result, _ := stream.Result["properties"].(map[string]any)
	if _, ok := result["event_mode"]; !ok {
		t.Fatal("session.events.stream schema must return event_mode")
	}

	submit := bundle.Methods["task.submit"]
	submitParams, _ := submit.Params["properties"].(map[string]any)
	if _, ok := submitParams["success_criteria"]; !ok {
		t.Fatal("task.submit schema must accept success_criteria")
	}
	successCheck, _ := bundle.Defs["success_check"].(map[string]any)
	properties, _ := successCheck["properties"].(map[string]any)
	command, _ := properties["command"].(map[string]any)
	if command["type"] != "string" {
		t.Fatalf("success_check.command schema = %+v, want string", command)
	}
	for _, method := range registry.APIs["task"] {
		if method.Method != "task.submit" {
			continue
		}
		params, _ := method.Params.(map[string]any)
		if _, ok := params["success_criteria"]; !ok {
			t.Fatal("task.submit registry must document success_criteria")
		}
		return
	}
	t.Fatal("task.submit missing from registry")
}

func TestSchemaBundleCoversCheckpointRestoreAndTaskResume(t *testing.T) {
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
	for _, method := range []string{"session.checkpoint.list", "session.checkpoint.preview", "session.checkpoint.summarize", "session.checkpoint.restore", "task.resume"} {
		if _, ok := bundle.Methods[method]; !ok {
			t.Errorf("schema bundle missing %s", method)
		}
	}
	resume := bundle.Methods["task.resume"]
	params, _ := resume.Params["properties"].(map[string]any)
	if _, ok := params["task_id"]; !ok {
		t.Fatal("task.resume schema must require task_id")
	}
	restore := bundle.Methods["session.checkpoint.restore"]
	required, _ := restore.Result["required"].([]any)
	if !containsAnyString(required, "idempotent") || !containsAnyString(required, "reconciliation_required") {
		t.Fatalf("checkpoint restore result schema lacks reconciliation contract: %+v", required)
	}
}

func containsAnyString(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestCheckedInEventRegistryMatchesSchema(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "protocol")
	registryRaw, err := os.ReadFile(filepath.Join(root, "events", "events.json"))
	if err != nil {
		t.Fatal(err)
	}
	schemaRaw, err := os.ReadFile(filepath.Join(root, "schemas", "event.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry eventRegistry
	var schema eventSchema
	if err := json.Unmarshal(registryRaw, &registry); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatal(err)
	}
	want := make(map[string]bool, len(registry.Types))
	for _, typ := range registry.Types {
		if typ.Name == "" || want[typ.Name] {
			t.Fatalf("invalid or duplicate event type %q", typ.Name)
		}
		want[typ.Name] = true
	}
	got := make(map[string]bool, len(schema.Properties.Type.Enum))
	for _, name := range schema.Properties.Type.Enum {
		got[name] = true
	}
	for name := range want {
		if !got[name] {
			t.Errorf("event %s is missing from event.schema.json", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("schema event %s is missing from events.json", name)
		}
	}
}

func TestLifecycleEventSchemaHasConditionalPayloadContracts(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "protocol", "schemas", "event.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	rules, ok := schema["allOf"].([]any)
	if !ok || len(rules) < 4 {
		t.Fatalf("conditional lifecycle rules missing: %T len=%d", schema["allOf"], len(rules))
	}
	encoded := string(raw)
	for _, field := range []string{`"call_id"`, `"artifact_ids"`, `"sequence"`, `"output_preview"`} {
		if !strings.Contains(encoded, field) {
			t.Errorf("schema missing lifecycle field/rule %s", field)
		}
	}
}

func TestConditionalEventPayloadsCoverRegistryContract(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "protocol")
	registryRaw, _ := os.ReadFile(filepath.Join(root, "events", "events.json"))
	schemaRaw, _ := os.ReadFile(filepath.Join(root, "schemas", "event.schema.json"))
	var registry eventRegistry
	var schema map[string]any
	if err := json.Unmarshal(registryRaw, &registry); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]bool{}
	for _, event := range registry.Types {
		want[event.Name] = map[string]bool{}
		for _, field := range event.Payload {
			want[event.Name][field] = true
		}
	}
	for _, rawRule := range schema["allOf"].([]any) {
		rule, _ := rawRule.(map[string]any)
		ifPart, _ := rule["if"].(map[string]any)
		props, _ := ifPart["properties"].(map[string]any)
		typeRule, _ := props["type"].(map[string]any)
		name, _ := typeRule["const"].(string)
		if name == "" {
			continue
		}
		then, _ := rule["then"].(map[string]any)
		thenProps, _ := then["properties"].(map[string]any)
		payload, _ := thenProps["payload"].(map[string]any)
		required, _ := payload["required"].([]any)
		for _, field := range required {
			if s, ok := field.(string); ok && !want[name][s] {
				t.Errorf("schema requires %s.%s but events.json does not declare it", name, s)
			}
		}
	}
}
