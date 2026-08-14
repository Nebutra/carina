package protocolschema

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestModelPreferenceConflictSchemaIsRetained(t *testing.T) {
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

	for _, method := range []string{"session.model.set", "execution.start", "execution.retry"} {
		errorSchema, ok := bundle.Methods[method].Errors["-32011"].(map[string]any)
		if !ok || errorSchema["$ref"] != "#/$defs/model_preference_conflict" {
			t.Errorf("%s -32011 schema = %#v", method, bundle.Methods[method].Errors["-32011"])
		}
	}
	if errors := bundle.Methods["session.model.get"].Errors; len(errors) != 0 {
		t.Fatalf("method without declared errors became incompatible: %#v", errors)
	}
	conflict, ok := bundle.Defs["model_preference_conflict"].(map[string]any)
	if !ok {
		t.Fatal("model_preference_conflict definition is missing")
	}
	required, _ := conflict["required"].([]any)
	for _, field := range []string{
		"expected_model_preference_revision",
		"actual_model_preference_revision",
		"current",
		"recovery",
	} {
		if !containsAnyString(required, field) {
			t.Errorf("model_preference_conflict missing required field %q: %#v", field, required)
		}
	}
}
