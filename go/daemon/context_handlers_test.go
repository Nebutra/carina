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
	if cr.Content != "hello" || cr.Engine != contextengine.ModeNoop || cr.Transformed {
		t.Fatalf("unexpected compression response: %+v", cr)
	}
	if !strings.Contains(cr.Reason, "no bytes were transformed") {
		t.Fatalf("compress reason missing identity sentence: %q", cr.Reason)
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

func mustRawJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
