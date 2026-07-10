package tui

import (
	"strings"
	"testing"
)

func TestApprovalScopeIsExplicitInRPC(t *testing.T) {
	for _, tc := range []struct {
		key   string
		scope string
	}{
		{key: "y", scope: "once"},
		{key: "2", scope: "session"},
		{key: "3", scope: "project"},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			fc := &fakeCaller{handler: map[string]any{
				"task.action.approve": map[string]any{
					"decision": map[string]any{"decision_id": "perm_scope", "decision": "allowed"},
					"scope":    tc.scope,
				},
			}}
			m, _ := newTestModel(fc)
			m.Update(permissionRequestEvent("perm_scope"))
			cmd, handled := m.handleKey(tc.key)
			if !handled {
				t.Fatalf("key %q was not handled", tc.key)
			}
			drain(m, cmd)
			last := fc.last()
			if got := last.params["scope"]; got != tc.scope {
				t.Fatalf("approval scope RPC param = %v, want %s", got, tc.scope)
			}
		})
	}
}

func TestApprovalUsesDaemonReportedScope(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.approve": map[string]any{
			"decision":    map[string]any{"decision_id": "perm_scope", "decision": "allowed"},
			"scope":       "once",
			"grant_error": "audit unavailable",
		},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_scope"))
	cmd, _ := m.handleKey("3")
	drain(m, cmd)
	text := transcriptText(m)
	if !containsAll(text, "Scope: once", "requested project scope was not persisted") {
		t.Fatalf("TUI presented an unpersisted project grant as active:\n%s", text)
	}
}

func containsAll(text string, values ...string) bool {
	for _, value := range values {
		if !strings.Contains(text, value) {
			return false
		}
	}
	return true
}
