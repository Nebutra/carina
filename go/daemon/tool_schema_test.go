package daemon

import (
	"encoding/json"
	"testing"

	modelrouter "github.com/Nebutra/carina/go/model-router"
	"github.com/Nebutra/carina/go/provider"
)

func TestCarinaToolSpecsIncludeDoneAndRead(t *testing.T) {
	specs := carinaToolSpecs()
	if len(specs) < 8 {
		t.Fatalf("tool catalog too small: %d", len(specs))
	}
	var haveRead, haveDone bool
	for _, spec := range specs {
		if spec.Name == "read" {
			haveRead = true
			if spec.Parameters["type"] != "object" {
				t.Fatalf("read schema = %+v", spec.Parameters)
			}
		}
		if spec.Name == "done" {
			haveDone = true
		}
	}
	if !haveRead || !haveDone {
		t.Fatalf("missing read or done in %+v", specs)
	}
}

func TestDecodeNativeToolCallsReadAndBatch(t *testing.T) {
	act, err := decodeNativeToolCalls([]modelrouter.ToolCall{{
		Name: "read", Arguments: json.RawMessage(`{"path":"main.go","intent":"inspect"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if act.Tool != "read" || act.Path != "main.go" {
		t.Fatalf("read action = %+v", act)
	}

	batch, err := decodeNativeToolCalls([]modelrouter.ToolCall{
		{Name: "read", Arguments: json.RawMessage(`{"path":"a.go","intent":"a"}`)},
		{Name: "search", Arguments: json.RawMessage(`{"pattern":"foo","intent":"b"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Actions) != 2 {
		t.Fatalf("batch = %+v", batch)
	}

	if _, err := decodeNativeToolCalls([]modelrouter.ToolCall{
		{Name: "read", Arguments: json.RawMessage(`{"path":"a.go","intent":"a"}`)},
		{Name: "patch", Arguments: json.RawMessage(`{"path":"a.go","content":"x","intent":"edit"}`)},
	}); err == nil {
		t.Fatal("mixed read/write native set must be rejected")
	}
}

func TestCatalogModelToolCallIsFailClosed(t *testing.T) {
	cat := provider.Catalog{
		"http": {ID: "http", Models: map[string]provider.Model{
			"capable": {ID: "capable", ToolCall: true},
			"text":    {ID: "text", ToolCall: false},
		}},
	}
	if !catalogModelToolCall(cat, "http/capable") {
		t.Fatal("catalogued ToolCall=true must be eligible")
	}
	if catalogModelToolCall(cat, "http/text") {
		t.Fatal("ToolCall=false must stay JSON-only")
	}
	if catalogModelToolCall(cat, "http/unknown") || catalogModelToolCall(cat, "unknown") {
		t.Fatal("unknown ids must not advertise native tools")
	}
}
