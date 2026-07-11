package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// searchManager builds a Manager directly (no subprocesses) with a public
// "files" server, a public "web" server, and a hidden "internal" server, so
// ranking and filtering behavior is tested against known metadata.
func searchManager() *Manager {
	files := &Client{name: "files", tools: []Tool{
		{
			Name:        "read_file",
			Description: "read one file from disk",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"absolute path of the file"}}}`),
		},
		{
			Name:        "list_directory",
			Description: "list entries under a directory",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"recursive":{"type":"boolean","description":"walk subdirectories too"}}}`),
		},
	}}
	web := &Client{name: "web", tools: []Tool{
		{
			Name:        "web_search",
			Description: "query the public internet",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}}}`),
		},
		{
			Name:        "fetch_page",
			Description: "search result pages can be fetched by URL",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}}}`),
		},
	}}
	internal := &Client{name: "internal", tools: []Tool{
		{Name: "secret_search", Description: "search private managed data"},
	}}
	return &Manager{
		clients: map[string]*Client{"files": files, "web": web, "internal": internal},
		hidden:  map[string]bool{"internal": true},
	}
}

func TestSearchToolsRankingOrder(t *testing.T) {
	m := searchManager()
	// "search" appears in web_search's NAME (weight 3), in fetch_page's
	// DESCRIPTION (weight 2), and nowhere for read_file.
	got := m.SearchTools("search", 10)
	if len(got) != 2 {
		t.Fatalf("expected 2 matches, got %d: %+v", len(got), got)
	}
	if got[0].Name != "web_search" || got[0].Server != "web" {
		t.Fatalf("name match should rank first, got %+v", got[0])
	}
	if got[1].Name != "fetch_page" {
		t.Fatalf("description match should rank second, got %+v", got[1])
	}
	if got[0].Score <= got[1].Score {
		t.Fatalf("scores not descending: %v vs %v", got[0].Score, got[1].Score)
	}
	if len(got[0].InputSchema) == 0 || !strings.Contains(string(got[0].InputSchema), `"query"`) {
		t.Fatalf("match should carry the full input schema, got %q", got[0].InputSchema)
	}
}

func TestSearchToolsSchemaPropertyMatch(t *testing.T) {
	m := searchManager()
	// "recursive" only exists as a schema property name on list_directory.
	got := m.SearchTools("recursive", 10)
	if len(got) != 1 || got[0].Name != "list_directory" {
		t.Fatalf("schema property should match list_directory, got %+v", got)
	}
	// "subdirectories" only exists in a schema property DESCRIPTION.
	got = m.SearchTools("subdirectories", 10)
	if len(got) != 1 || got[0].Name != "list_directory" {
		t.Fatalf("schema property description should match list_directory, got %+v", got)
	}
	// A description match must outrank a schema-text match for the same token.
	m2 := &Manager{
		clients: map[string]*Client{"srv": {name: "srv", tools: []Tool{
			{Name: "tool_a", Description: "handles zebra things"},
			{Name: "tool_b", Description: "other", InputSchema: json.RawMessage(`{"properties":{"zebra":{"type":"string"}}}`)},
		}}},
		hidden: map[string]bool{},
	}
	got = m2.SearchTools("zebra", 10)
	if len(got) != 2 || got[0].Name != "tool_a" || got[1].Name != "tool_b" || got[0].Score <= got[1].Score {
		t.Fatalf("description match should outrank schema match, got %+v", got)
	}
}

func TestSearchToolsHiddenServerExcluded(t *testing.T) {
	m := searchManager()
	// "secret" only matches the hidden server's tool; nothing may leak.
	if got := m.SearchTools("secret", 10); len(got) != 0 {
		t.Fatalf("hidden server metadata leaked: %+v", got)
	}
	// Even a broad query never returns the hidden server.
	for _, match := range m.SearchTools("search private managed data", 10) {
		if match.Server == "internal" {
			t.Fatalf("hidden server leaked through broad query: %+v", match)
		}
	}
}

func TestSearchToolsEmptyQuery(t *testing.T) {
	m := searchManager()
	for _, q := range []string{"", "   ", "___", "!!"} {
		if got := m.SearchTools(q, 10); len(got) != 0 {
			t.Fatalf("query %q should match nothing, got %+v", q, got)
		}
	}
}

func TestSearchToolsLimit(t *testing.T) {
	tools := make([]Tool, 12)
	for i := range tools {
		tools[i] = Tool{Name: fmt.Sprintf("alpha_tool_%02d", i), Description: "alpha helper"}
	}
	m := &Manager{
		clients: map[string]*Client{"srv": {name: "srv", tools: tools}},
		hidden:  map[string]bool{},
	}
	if got := m.SearchTools("alpha", 3); len(got) != 3 {
		t.Fatalf("limit 3 should return 3, got %d", len(got))
	}
	if got := m.SearchTools("alpha", 0); len(got) != defaultSearchLimit {
		t.Fatalf("limit<=0 should default to %d, got %d", defaultSearchLimit, len(got))
	}
}

func TestSearchToolsDeterministicTieBreak(t *testing.T) {
	tie := func(name string) *Client {
		return &Client{name: name, tools: []Tool{{Name: "ping", Description: "same tool"}}}
	}
	m := &Manager{
		clients: map[string]*Client{"bravo": tie("bravo"), "alpha": tie("alpha"), "charlie": tie("charlie")},
		hidden:  map[string]bool{},
	}
	for i := 0; i < 20; i++ {
		got := m.SearchTools("ping", 10)
		if len(got) != 3 || got[0].Server != "alpha" || got[1].Server != "bravo" || got[2].Server != "charlie" {
			t.Fatalf("tie-break not deterministic on run %d: %+v", i, got)
		}
	}
}

func TestFlattenSchemaText(t *testing.T) {
	nested := json.RawMessage(`{
		"type":"object",
		"description":"top level",
		"properties":{
			"outer":{"type":"object","properties":{"inner_flag":{"type":"boolean","description":"deep toggle"}}},
			"items_list":{"type":"array","items":{"type":"object","properties":{"element_id":{"type":"string"}}}}
		}
	}`)
	text := flattenSchemaText(nested)
	for _, want := range []string{"top level", "outer", "inner_flag", "deep toggle", "items_list", "element_id"} {
		if !strings.Contains(text, want) {
			t.Fatalf("flattened schema text missing %q: %q", want, text)
		}
	}
	if flattenSchemaText(nil) != "" {
		t.Fatal("nil schema should flatten to empty")
	}
	if flattenSchemaText(json.RawMessage(`not json`)) != "" {
		t.Fatal("malformed schema should flatten to empty")
	}
}
