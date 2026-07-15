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
		"history.recent":   map[string]any{"entries": []string{}},
		"session.get":      map[string]any{"session_id": "sess_test", "status": "active"},
		"profile.describe": map[string]any{"name": "safe-edit"},
		"context.summary":  map[string]any{"checkpoint": map[string]any{"available": true, "transcript_bytes": 42}, "model_context_tokens": map[string]any{"available": false}},
		"daemon.status":    map[string]any{"version": "test", "safe_mode": false},
		"usage.cost":       map[string]any{"total_cost_usd": 0.12, "total_tokens": 42},
		"session.review":   map[string]any{"session_id": "sess_test", "findings": []any{}},
		"memory.status":    map[string]any{"available": true, "entries": 3},
	}}
	m, _ := newTestModel(fc)
	for _, tc := range []struct{ command, method string }{
		{"/status", "session.get"},
		{"/permissions", "profile.describe"},
		{"/context", "context.summary"},
		{"/config", "daemon.status"},
		{"/usage", "usage.cost"},
		{"/review", "session.review"},
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
		if tc.command == "/context" && fc.last().params["session_id"] != "sess_test" {
			t.Fatalf("/context params = %#v, want current session", fc.last().params)
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
	fc := &fakeCaller{handler: map[string]any{"history.recent": map[string]any{"entries": []string{}}}}
	m, _ := newTestModel(fc)
	before := len(fc.calls)
	if cmd := m.slashCommand("/compact"); cmd != nil {
		t.Fatal("/compact returned an RPC command")
	}
	if len(fc.calls) != before {
		t.Fatalf("/compact made an RPC call: %#v", fc.calls[before:])
	}
	got := m.tr.plainText()
	if !strings.Contains(got, "atomically replace") || !strings.Contains(got, "context.compress") {
		t.Fatalf("/compact did not explain the safety boundary:\n%s", got)
	}
}
