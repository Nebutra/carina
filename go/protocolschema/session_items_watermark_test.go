package protocolschema

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestSessionItemsWatermarkSchemaRetainsLegacyAndEnvelopeForms(t *testing.T) {
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
	items := bundle.Methods["session.items"]
	properties, _ := items.Params["properties"].(map[string]any)
	watermark, _ := properties["watermark_version"].(map[string]any)
	if watermark["const"] != float64(1) {
		t.Fatalf("watermark params schema = %#v", watermark)
	}
	forms, _ := items.Result["oneOf"].([]any)
	if len(forms) != 3 {
		t.Fatalf("session.items result forms = %#v", forms)
	}
	envelope, _ := forms[2].(map[string]any)
	required, _ := envelope["required"].([]any)
	for _, field := range []string{"session_id", "runtime_id", "runtime_epoch", "runtime_process_epoch", "durable_cursor", "items"} {
		if !containsAnyString(required, field) {
			t.Errorf("watermark envelope missing required field %q: %#v", field, required)
		}
	}
}
