package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseActionAcceptsTodoAndPlanAliases(t *testing.T) {
	todo, err := parseAction(`{"tool":"todo","intent":"Track the remaining work","todos":[{"content":"Inspect renderer","status":"in_progress"},{"text":"Run tests","status":"pending"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := todo.incomingChecklist()
	if !ok || len(items) != 2 || items[0].Content != "Inspect renderer" || items[1].Content != "Run tests" {
		t.Fatalf("todos not parsed: %+v err=%v", items, err)
	}

	plan, err := parseAction(`{"tool":"update_plan","plan":[{"step":"Inspect renderer","status":"completed"},{"step":"Run tests","status":"doing"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tool != "update_plan" {
		t.Fatalf("tool = %q", plan.Tool)
	}
	got, err := coerceTodoItems(plan.Plan)
	if err != nil || len(got) != 2 || got[0].Status != "completed" || got[1].Status != "in_progress" {
		t.Fatalf("plan alias = %+v err=%v", got, err)
	}
}

func TestTodoReplacesSessionChecklistAndProjectsArguments(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "plan")

	var requested map[string]any
	d.events.Tap(func(_ string, event map[string]any) {
		if event["type"] != "ToolCallRequested" {
			return
		}
		payload, _ := event["payload"].(map[string]any)
		if payload["tool"] == "todo" {
			requested = payload
		}
	})

	obs := d.executeAction(sess, task, &action{
		Tool: "todo", Intent: "Track remaining work",
		Todos: []todoItem{
			{Content: "Inspect renderer", Status: "in_progress"},
			{Content: "Run tests", Status: "pending"},
			{Content: "Inspect renderer", Status: "in_progress"},
		},
	})
	if !strings.Contains(obs, "Inspect renderer") || !strings.Contains(obs, "(in_progress)") || !strings.Contains(obs, "Run tests") {
		t.Fatalf("checklist observation = %q", obs)
	}
	stored := d.sessionTodos(sess.SessionID)
	if len(stored) != 3 || stored[0].Status != "in_progress" || stored[2].Status != "pending" {
		t.Fatalf("extra in_progress must coerce to pending: %+v", stored)
	}
	if requested == nil {
		t.Fatal("missing ToolCallRequested")
	}
	args, _ := requested["arguments"].(map[string]any)
	raw, _ := json.Marshal(args["todos"])
	if !strings.Contains(string(raw), `"content":"Inspect renderer"`) || !strings.Contains(string(raw), `"status":"in_progress"`) {
		t.Fatalf("lifecycle arguments = %s", raw)
	}

	show := d.executeAction(sess, task, &action{Tool: "todo", Intent: "Show the checklist"})
	if !strings.Contains(show, "Inspect renderer") {
		t.Fatalf("empty todo should echo the stored checklist, got %q", show)
	}

	cleared := d.executeAction(sess, task, &action{Tool: "todo", Intent: "Clear", Todos: []todoItem{}})
	if cleared != "checklist empty" {
		t.Fatalf("clear = %q", cleared)
	}
	if got := d.sessionTodos(sess.SessionID); len(got) != 0 {
		t.Fatalf("cleared store = %+v", got)
	}
}

func TestUpdatePlanWorksInPlanMode(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "plan")
	d.setPlanMode(sess.SessionID, true)

	obs, outcome := d.executeActionOutcome(sess, task, &action{
		Tool: "update_plan", Intent: "Publish the plan",
		Plan: []todoItem{{Content: "Inspect renderer", Status: "completed"}, {Content: "Run tests", Status: "in_progress"}},
	})
	if outcome.status != "completed" {
		t.Fatalf("update_plan in plan mode = %+v obs=%q", outcome, obs)
	}
	if strings.Contains(obs, "unknown tool") || strings.Contains(obs, "plan mode") {
		t.Fatalf("update_plan must dispatch in plan mode, got %q", obs)
	}
	stored := d.sessionTodos(sess.SessionID)
	if len(stored) != 2 || stored[0].Content != "Inspect renderer" || stored[1].Status != "in_progress" {
		t.Fatalf("stored plan = %+v", stored)
	}
}

func TestTodoRejectsOversizedChecklist(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	items := make([]todoItem, maxTodoItems+1)
	for i := range items {
		items[i] = todoItem{Content: "step"}
	}
	outcome := d.executeTodoOutcome(sess, nil, &action{Tool: "todo", Todos: items})
	if outcome.status != "failed" || !strings.Contains(outcome.display, "at most") {
		t.Fatalf("cap = %+v", outcome)
	}
}
