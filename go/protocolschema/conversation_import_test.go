package protocolschema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConversationImportSchemasAreRetained(t *testing.T) {
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

	discover, ok := bundle.Methods["conversation.import.discover"]
	if !ok {
		t.Fatal("conversation.import.discover schema is missing")
	}
	discoverProperties, _ := discover.Params["properties"].(map[string]any)
	sources, _ := discoverProperties["sources"].(map[string]any)
	sourceItems, _ := sources["items"].(map[string]any)
	if sources["maxItems"] != float64(2) || !containsAnyString(sourceItems["enum"].([]any), "claude-code") || !containsAnyString(sourceItems["enum"].([]any), "codex") {
		t.Fatalf("discover source schema = %#v", sources)
	}
	discoverResult, _ := discover.Result["properties"].(map[string]any)
	conversations, _ := discoverResult["conversations"].(map[string]any)
	conversationItems, _ := conversations["items"].(map[string]any)
	if conversationItems["$ref"] != "#/$defs/conversation_import_candidate" {
		t.Fatalf("discover result schema = %#v", discover.Result)
	}

	apply, ok := bundle.Methods["conversation.import.apply"]
	if !ok {
		t.Fatal("conversation.import.apply schema is missing")
	}
	applyProperties, _ := apply.Params["properties"].(map[string]any)
	selections, _ := applyProperties["selections"].(map[string]any)
	if selections["minItems"] != float64(1) || selections["maxItems"] != float64(100) {
		t.Fatalf("apply selection bounds = %#v", selections)
	}
	selection, _ := bundle.Defs["conversation_import_selection"].(map[string]any)
	required, _ := selection["required"].([]any)
	for _, field := range []string{"source", "path", "conversation_id"} {
		if !containsAnyString(required, field) {
			t.Errorf("conversation_import_selection missing required field %q: %#v", field, required)
		}
	}
	receipt, ok := bundle.Defs["conversation_import_receipt"].(map[string]any)
	if !ok {
		t.Fatal("conversation_import_receipt definition is missing")
	}
	receiptProperties, _ := receipt["properties"].(map[string]any)
	status, _ := receiptProperties["status"].(map[string]any)
	for _, value := range []string{"imported", "updated", "up_to_date", "partial", "failed"} {
		if !containsAnyString(status["enum"].([]any), value) {
			t.Errorf("conversation import receipt status is missing %q", value)
		}
	}
}

func TestConversationImportedEventDeclaresExecutablePayload(t *testing.T) {
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
	var schema map[string]any
	if err := json.Unmarshal(registryRaw, &registry); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		t.Fatal(err)
	}

	var catalogPayload []string
	for _, event := range registry.Types {
		if event.Name == "ConversationImported" {
			catalogPayload = event.Payload
			break
		}
	}
	for _, field := range []string{"source", "source_conversation_id", "source_path", "source_message_id", "source_timestamp", "role", "content", "fingerprint", "batch_id"} {
		found := false
		for _, declared := range catalogPayload {
			if declared == field {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("ConversationImported catalog payload is missing %q", field)
		}
	}

	foundRule := false
	for _, rawRule := range schema["allOf"].([]any) {
		rule, _ := rawRule.(map[string]any)
		ifPart, _ := rule["if"].(map[string]any)
		ifProperties, _ := ifPart["properties"].(map[string]any)
		typeRule, _ := ifProperties["type"].(map[string]any)
		if typeRule["const"] != "ConversationImported" {
			continue
		}
		foundRule = true
		then, _ := rule["then"].(map[string]any)
		thenProperties, _ := then["properties"].(map[string]any)
		payload, _ := thenProperties["payload"].(map[string]any)
		required, _ := payload["required"].([]any)
		for _, field := range catalogPayload {
			if !containsAnyString(required, field) {
				t.Errorf("ConversationImported schema does not require catalog field %q", field)
			}
		}
		payloadProperties, _ := payload["properties"].(map[string]any)
		fingerprint, _ := payloadProperties["fingerprint"].(map[string]any)
		if fingerprint["pattern"] != "^sha256:[0-9a-f]{64}$" {
			t.Fatalf("ConversationImported fingerprint schema = %#v", fingerprint)
		}
	}
	if !foundRule {
		t.Fatal("ConversationImported conditional payload schema is missing")
	}
}
