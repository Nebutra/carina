package tui

import (
	"encoding/json"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestGoalCommandSetsBudgetAndUsesManualContinue(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"goal.set":      map[string]any{"session_id": "sess-1", "objective": "ship safely", "status": "active", "token_budget": 500, "tokens_used": 0, "time_used_seconds": 0, "continuations_used": 0, "max_continuations": 8},
		"goal.continue": map[string]any{"task_id": "tsk-1"},
		"goal.get": map[string]any{"goal": map[string]any{
			"session_id": "sess-1", "objective": "ship safely", "status": "active", "token_budget": 500, "tokens_used": 120, "time_used_seconds": 9, "continuations_used": 1, "max_continuations": 8,
		}},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess-1", fc
	msg := m.slashCommand("/goal --auto --tokens 500 --max-continuations 5 ship safely")()
	m.Update(msg)
	if len(fc.calls) != 1 || fc.calls[0].method != "goal.set" {
		t.Fatalf("calls = %#v", fc.calls)
	}
	if fc.calls[0].params["token_budget"] != float64(500) || fc.calls[0].params["objective"] != "ship safely" {
		t.Fatalf("set params = %#v", fc.calls[0].params)
	}
	if fc.calls[0].params["auto_continue"] != true || fc.calls[0].params["max_continuations"] != float64(5) {
		t.Fatalf("auto continuation params = %#v", fc.calls[0].params)
	}
	msg = m.slashCommand("/goal continue")()
	m.Update(msg)
	if len(fc.calls) != 3 || fc.calls[1].method != "goal.continue" || fc.calls[2].method != "goal.get" {
		t.Fatalf("continue calls = %#v", fc.calls)
	}
	if m.goal == nil || m.goal.ContinuationsUsed != 1 || m.goal.TokensUsed != 120 {
		t.Fatalf("continue did not apply authoritative goal refresh: %#v", m.goal)
	}
}

func TestGoalStatusResponseUpdatesFooterState(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "zh"})
	raw, _ := json.Marshal(goalView{Objective: "完成集成", Status: "paused", TokenBudget: 100, TokensUsed: 20, MaxContinuations: 8})
	var goal goalView
	_ = json.Unmarshal(raw, &goal)
	m.handleGoalRPC(goalRPCMsg{action: "status", goal: &goal})
	if m.goal == nil || m.goal.Status != "paused" {
		t.Fatalf("goal = %#v", m.goal)
	}
	if got := transcriptText(m); got == "" {
		t.Fatal("localized goal status was not rendered")
	}
}
