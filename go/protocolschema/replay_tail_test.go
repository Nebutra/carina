package protocolschema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEventStreamReplayTailSchemaAndCatalogStayOffDurableEvents(t *testing.T) {
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
	if _, ok := params["replay_tail_version"]; !ok {
		t.Fatal("session.events.stream schema must accept replay_tail_version")
	}
	result, _ := stream.Result["properties"].(map[string]any)
	if _, ok := result["replay_boundary"]; !ok {
		t.Fatal("session.events.stream schema must return optional replay_boundary")
	}
	if _, ok := bundle.Defs["replay_boundary_v1"]; !ok {
		t.Fatal("schema bundle missing replay_boundary_v1")
	}
	if _, ok := bundle.Defs["assistant_message_snapshot"]; !ok {
		t.Fatal("schema bundle missing assistant_message_snapshot")
	}

	raw, err := os.ReadFile(filepath.Join(root, "protocol", "events", "events.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalog eventRegistry
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, event := range catalog.Types {
		if event.Name == "assistant.message.snapshot" {
			t.Fatal("assistant.message.snapshot must not enter the durable event catalog")
		}
	}
}
