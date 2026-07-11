package protocolschema

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
