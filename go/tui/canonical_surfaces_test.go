package tui

import (
	"strings"
	"testing"
)

func canonicalFixture() []map[string]any {
	return []map[string]any{
		{"type": "thread.started", "session_id": "sess_test", "details": map[string]any{"profile": "safe-edit"}},
		{"type": "item.completed", "session_id": "sess_test", "item": map[string]any{"id": "item_hidden", "type": "permission", "status": "denied", "details": map[string]any{"resource": "hidden-secret-marker"}}},
	}
}

func TestCanonicalTranscriptLoadsSessionItemsIncludingHiddenEvents(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"history.recent": map[string]any{"entries": []string{}}, "session.items": canonicalFixture()}}
	m, _ := newTestModel(fc)
	cmd := m.slashCommand("/transcript")
	if cmd == nil || m.transcriptPager == nil || !m.transcriptPager.loading {
		t.Fatalf("canonical transcript did not enter loading state: %#v", m.transcriptPager)
	}
	m.Update(cmd())
	if got := m.transcriptPager.text; !strings.Contains(got, "hidden-secret-marker") || !strings.Contains(got, "item_hidden") {
		t.Fatalf("canonical transcript dropped hidden item:\n%s", got)
	}
	if fc.last().method != "session.items" {
		t.Fatalf("transcript called %q, want session.items", fc.last().method)
	}
}

func TestCanonicalSearchAndRecapUseSessionItems(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"history.recent": map[string]any{"entries": []string{}}, "session.items": canonicalFixture()}}
	m, _ := newTestModel(fc)
	m.push("visible conversation only")

	search := m.slashCommand("/search hidden-secret-marker")
	m.Update(search())
	if got := m.tr.plainText(); !strings.Contains(got, "canonical search (1)") || !strings.Contains(got, "item_hidden") {
		t.Fatalf("canonical search result missing:\n%s", got)
	}

	recap := m.slashCommand("/recap")
	m.Update(recap())
	if got := m.tr.plainText(); !strings.Contains(got, "hidden-secret-marker") {
		t.Fatalf("canonical recap dropped hidden item:\n%s", got)
	}
}

func TestOperationalSlashSurfacesUseExistingDaemonRPCs(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"history.recent":    map[string]any{"entries": []string{}},
		"session.get":       map[string]any{"session_id": "sess_test", "status": "active"},
		"profile.inventory": map[string]any{"profile": "safe-edit", "source": "session creation policy", "effective": map[string]any{"name": "safe-edit"}, "choices": []any{map[string]any{"name": "safe-edit"}}},
		"context.summary":   map[string]any{"checkpoint": map[string]any{"available": true, "transcript_bytes": 42}, "model_context_tokens": map[string]any{"available": false}},
		"config.inventory":  map[string]any{"effective": map[string]any{"safe_mode": false}, "sources": map[string]any{"runtime": "daemon config"}},
		"usage.cost":        map[string]any{"total_cost_usd": 0.12, "total_tokens": 42},
		"session.review":    map[string]any{"session_id": "sess_test", "findings": []any{}},
		"memory.status":     map[string]any{"available": true, "entries": 3},
	}}
	m, _ := newTestModel(fc)
	for _, tc := range []struct{ command, method string }{
		{"/status", "session.get"},
		{"/permissions", "profile.inventory"},
		{"/context", "context.summary"},
		{"/config raw", "config.inventory"},
		{"/usage", "usage.cost"},
		{"/session-review", "session.review"},
		{"/memory", "memory.status"},
	} {
		cmd := m.slashCommand(tc.command)
		if cmd == nil {
			t.Fatalf("%s returned no RPC command", tc.command)
		}
		m.Update(cmd())
		if got := fc.last().method; got != tc.method {
			t.Fatalf("%s called %s, want %s", tc.command, got, tc.method)
		}
		if (tc.command == "/context" || tc.command == "/permissions" || tc.command == "/config") && fc.last().params["session_id"] != "sess_test" {
			t.Fatalf("%s params = %#v, want current session", tc.command, fc.last().params)
		}
		if got := m.tr.plainText(); strings.Contains(got, `{"`) {
			t.Fatalf("%s rendered a raw JSON dump:\n%s", tc.command, got)
		}
	}
}

func TestCompactDoesNotUseDiagnosticCompression(t *testing.T) {
	if !validSlashCommand("/compact") {
		t.Fatal("/compact is not registered as a valid slash command")
	}
	fc := &fakeCaller{handler: map[string]any{"history.recent": map[string]any{"entries": []string{}}, "session.checkpoint.compact": map[string]any{"compacted": true, "task_id": "tsk", "checkpoint_id": "tsk:2:3", "status": "paused"}}}
	m, _ := newTestModel(fc)
	before := len(fc.calls)
	cmd := m.slashCommand("/compact")
	if cmd == nil {
		t.Fatal("/compact did not return an RPC command")
	}
	m.Update(cmd())
	if len(fc.calls) != before+1 || fc.last().method != "session.checkpoint.compact" {
		t.Fatalf("/compact calls: %#v", fc.calls[before:])
	}
	if fc.last().method == "context.compress" {
		t.Fatal("/compact used diagnostic context compression")
	}
}
