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
	var haveRead, haveDone, haveEdit bool
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
		if spec.Name == "edit" {
			haveEdit = true
		}
	}
	if !haveRead || !haveDone || !haveEdit {
		t.Fatalf("missing read, done, or edit in %+v", specs)
	}
	var firstMCP = -1
	for i, spec := range specs {
		if spec.Name == "mcp" || spec.Name == "mcp_find" {
			if firstMCP < 0 {
				firstMCP = i
			}
			continue
		}
		if firstMCP >= 0 && spec.Name != "done" {
			t.Fatalf("built-in tool %q must stay before MCP wrappers (or be done)", spec.Name)
		}
	}
	if firstMCP < 0 {
		t.Fatal("mcp wrappers missing")
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

	act, err = decodeNativeToolCalls([]modelrouter.ToolCall{{
		Name: "edit", Arguments: json.RawMessage(`{"path":"a.go","old":"foo","new":"bar","intent":"rename"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if act.Tool != "edit" || act.Path != "a.go" || act.Old != "foo" || act.New != "bar" {
		t.Fatalf("native edit = %+v", act)
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
