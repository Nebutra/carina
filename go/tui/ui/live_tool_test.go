package ui

import "testing"

func TestLiveToolRegistryKeepsConcurrentCallIdentity(t *testing.T) {
	registry := NewLiveToolRegistry()
	registry.Observe(LiveToolUpdate{CallID: "call_a", Tool: "read", Status: LiveToolRunning, Summary: "a.go"})
	registry.Observe(LiveToolUpdate{CallID: "call_b", Tool: "search", Status: LiveToolRunning, Summary: "needle"})
	registry.Observe(LiveToolUpdate{CallID: "call_a", Status: LiveToolCompleted, Details: []string{"duration_ms: 5"}})

	a, ok := registry.Get("call_a")
	if !ok || a.Status != LiveToolCompleted || a.Tool != "read" {
		t.Fatalf("call_a=%#v ok=%v", a, ok)
	}
	b, ok := registry.Get("call_b")
	if !ok || b.Status != LiveToolRunning || b.Tool != "search" {
		t.Fatalf("call_b=%#v ok=%v", b, ok)
	}
	if registry.Len() != 2 {
		t.Fatalf("registry len=%d", registry.Len())
	}
}

func TestLiveToolCellRejectsLateNonTerminalRegression(t *testing.T) {
	registry := NewLiveToolRegistry()
	registry.Observe(LiveToolUpdate{CallID: "call_1", Status: LiveToolCompleted})
	if _, accepted := registry.Observe(LiveToolUpdate{CallID: "call_1", Status: LiveToolRunning}); accepted {
		t.Fatal("late running update regressed a terminal cell")
	}
	snapshot, _ := registry.Get("call_1")
	if snapshot.Status != LiveToolCompleted {
		t.Fatalf("status=%q", snapshot.Status)
	}
}
