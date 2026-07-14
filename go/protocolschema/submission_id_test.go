package protocolschema

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestTaskSubmitClientSubmissionIDContract(t *testing.T) {
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

	submit := bundle.Methods["task.submit"]
	params, _ := submit.Params["properties"].(map[string]any)
	if _, ok := params["client_submission_id"]; !ok {
		t.Fatal("task.submit schema must accept client_submission_id")
	}
	taskDef, _ := bundle.Defs["task"].(map[string]any)
	result, _ := taskDef["properties"].(map[string]any)
	if _, ok := result["client_submission_id"]; !ok {
		t.Fatal("Task schema must expose client_submission_id")
	}

	for _, method := range registry.APIs["task"] {
		if method.Method != "task.submit" {
			continue
		}
		methodParams, _ := method.Params.(map[string]any)
		if _, ok := methodParams["client_submission_id"]; !ok {
			t.Fatal("task.submit registry must document client_submission_id")
		}
		return
	}
	t.Fatal("task.submit missing from registry")
}
