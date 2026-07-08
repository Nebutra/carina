package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/contextengine"
)

func TestContextHandlersNoopStatsAndCompress(t *testing.T) {
	eng, err := contextengine.New(contextengine.Config{ContextEngine: contextengine.ModeNoop})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{contextEng: eng}

	compressed, err := d.handleContextCompress(mustRawJSON(t, map[string]any{"content": "hello"}))
	if err != nil {
		t.Fatal(err)
	}
	cr := compressed.(contextengine.CompressResponse)
	if cr.Content != "hello" || cr.Engine != contextengine.ModeNoop {
		t.Fatalf("unexpected compression response: %+v", cr)
	}

	stats, err := d.handleContextStats(nil)
	if err != nil {
		t.Fatal(err)
	}
	local := stats.(map[string]any)["local"].(contextengine.Stats)
	if local.CompressionCalls != 1 {
		t.Fatalf("unexpected stats: %+v", local)
	}
}

func TestContextRetrieveRequiresRef(t *testing.T) {
	eng, err := contextengine.New(contextengine.Config{ContextEngine: contextengine.ModeNoop})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{contextEng: eng}
	_, err = d.handleContextRetrieve(mustRawJSON(t, map[string]any{}))
	if err == nil || !strings.Contains(err.Error(), "hash or ref is required") {
		t.Fatalf("expected missing ref error, got %v", err)
	}
}

func TestHeadroomToolResponseParsing(t *testing.T) {
	compressed := parseHeadroomCompressResponse("original text", `{"compressed":"summary","hash":"abc","original_tokens":10,"compressed_tokens":3,"savings_percent":70,"transforms":["summarize"]}`)
	if compressed.Content != "summary" || compressed.OriginalRef != "abc" || compressed.OriginalTokens != 10 || compressed.CompressedTokens != 3 || len(compressed.Transforms) != 1 {
		t.Fatalf("unexpected parsed compression: %+v", compressed)
	}
	retrieved := parseHeadroomRetrieveResponse("abc", `{"original_content":"original text","source":"ccr"}`)
	if retrieved.Content != "original text" || retrieved.Source != "ccr" || retrieved.Ref != "abc" {
		t.Fatalf("unexpected parsed retrieve: %+v", retrieved)
	}
}

func mustRawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
