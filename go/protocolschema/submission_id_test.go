package protocolschema

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestExecutionStartClientSubmissionIDContract(t *testing.T) {
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

	submit := bundle.Methods["execution.start"]
	params, _ := submit.Params["properties"].(map[string]any)
	if _, ok := params["client_submission_id"]; !ok {
		t.Fatal("execution.start schema must accept client_submission_id")
	}
	executionDef, _ := bundle.Defs["execution_run"].(map[string]any)
	result, _ := executionDef["properties"].(map[string]any)
	if _, ok := result["client_submission_id"]; !ok {
		t.Fatal("ExecutionRun schema must expose client_submission_id")
	}

	for _, method := range registry.APIs["execution"] {
		if method.Method != "execution.start" {
			continue
		}
		methodParams, _ := method.Params.(map[string]any)
		if _, ok := methodParams["client_submission_id"]; !ok {
			t.Fatal("execution.start registry must document client_submission_id")
		}
		return
	}
	t.Fatal("execution.start missing from registry")
}
