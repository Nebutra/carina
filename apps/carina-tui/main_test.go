package main

import (
	"strings"
	"testing"
)

func TestFormatStatus(t *testing.T) {
	out := formatStatus(map[string]any{
		"version":        "0.5.0",
		"sessions":       2,
		"tasks":          3,
		"workers":        1,
		"tools":          true,
		"uptime_seconds": 9,
		"rpc_endpoint":   "/tmp/carina.sock",
	})
	for _, want := range []string{"Carina Runtime", "version", "0.5.0", "sessions", "2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestFormatSessions(t *testing.T) {
	out := formatSessions([]sessionRow{{
		SessionID: "sess_1", Status: "active", Profile: "safe-edit", WorkspaceRoot: "/repo",
	}})
	for _, want := range []string{"Sessions", "sess_1", "active", "safe-edit", "/repo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("sessions output missing %q:\n%s", want, out)
		}
	}
	if empty := formatSessions(nil); !strings.Contains(empty, "no sessions") {
		t.Fatalf("empty sessions output: %s", empty)
	}
}
