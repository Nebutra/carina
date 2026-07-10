package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProtocolEventCatalogMatchesSchemaEnum(t *testing.T) {
	root := repoRootFromHere(t)
	var catalog struct {
		Types []struct {
			Name string `json:"name"`
		} `json:"types"`
	}
	readProtocolJSON(t, filepath.Join(root, "protocol", "events", "events.json"), &catalog)

	var schema struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	readProtocolJSON(t, filepath.Join(root, "protocol", "schemas", "event.schema.json"), &schema)
	enum := map[string]bool{}
	for _, name := range schema.Properties["type"].Enum {
		enum[name] = true
	}
	for _, typ := range catalog.Types {
		if !enum[typ.Name] {
			t.Fatalf("event %q exists in events.json but not event.schema.json enum", typ.Name)
		}
	}
}

func TestProtocolMethodsIncludeMemorySearchModesAndSchedules(t *testing.T) {
	root := repoRootFromHere(t)
	var methods struct {
		APIs map[string][]struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		} `json:"apis"`
	}
	readProtocolJSON(t, filepath.Join(root, "protocol", "jsonrpc", "methods.json"), &methods)

	memorySearch := methodByName(methods.APIs["memory"], "memory.search")
	if memorySearch == nil {
		t.Fatal("methods.json missing memory.search")
	}
	if memorySearch.Params["mode"] == nil || memorySearch.Params["model"] == nil {
		t.Fatalf("memory.search params must expose mode/model for compatibility: %+v", memorySearch.Params)
	}
	for _, method := range []string{"schedule.create", "schedule.list", "schedule.pause", "schedule.resume", "schedule.delete"} {
		if methodByName(methods.APIs["schedule"], method) == nil {
			t.Fatalf("methods.json missing %s", method)
		}
	}
}

func readProtocolJSON(t *testing.T, path string, out any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func methodByName(methods []struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}, name string) *struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
} {
	for i := range methods {
		if methods[i].Method == name {
			return &methods[i]
		}
	}
	return nil
}
