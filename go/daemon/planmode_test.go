package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/scheduler"
)

// TestPlanMode: while plan mode is on, exploration (list/read) is allowed but
// edits/commands are blocked; approving the plan lets them through.
func TestPlanMode(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	os.WriteFile(filepath.Join(ws, "a.txt"), []byte("x\n"), 0o600)
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "x")

	d.setPlanMode(sess.SessionID, true)

	// Read-only exploration is allowed in plan mode.
	if obs := d.executeAction(sess, task, &action{Tool: "read", Path: "a.txt"}); strings.Contains(obs, "plan mode") {
		t.Fatalf("read should be allowed in plan mode, got: %s", obs)
	}
	// Edits are blocked.
	obs := d.executeAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "y\n"})
	if !strings.Contains(obs, "plan mode") {
		t.Fatalf("patch should be blocked in plan mode, got: %s", obs)
	}
	// Commands are blocked.
	if obs := d.executeAction(sess, task, &action{Tool: "run", Command: []string{"echo", "hi"}}); !strings.Contains(obs, "plan mode") {
		t.Fatalf("run should be blocked in plan mode, got: %s", obs)
	}
	if _, outcome := d.executeActionOutcome(sess, task, &action{Tool: "future_unknown_tool"}); outcome.status != "denied" || outcome.errorCategory != "plan_mode" {
		t.Fatalf("unknown tool must fail closed at the Plan gate, got: %+v", outcome)
	}

	// Approving the plan lets edits through the plan gate.
	d.setPlanMode(sess.SessionID, false)
	obs = d.executeAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "y\n"})
	if strings.Contains(obs, "plan mode") {
		t.Fatalf("patch should pass the plan gate after approval, got: %s", obs)
	}
}

func TestPlanModeToolMatrix(t *testing.T) {
	tests := []struct {
		tool    string
		blocked bool
	}{
		{tool: "list"}, {tool: "read"}, {tool: "search"},
		{tool: "code.search"}, {tool: "code.symbols"}, {tool: "code.map"},
		{tool: "code.def"}, {tool: "code.refs"}, {tool: "code.impact"},
		{tool: "todo"}, {tool: "update_plan"}, {tool: "ask_user"},
		{tool: "done"}, {tool: "mcp_find"}, {tool: "spawn"},
		{tool: "patch", blocked: true}, {tool: "edit", blocked: true}, {tool: "run", blocked: true},
		{tool: "web.fetch", blocked: true}, {tool: "web.search", blocked: true},
		{tool: "memory", blocked: true}, {tool: "mcp", blocked: true},
		{tool: "workflow", blocked: true}, {tool: "best_of_n", blocked: true},
		{tool: "swarm_publish", blocked: true}, {tool: "swarm_receive", blocked: true},
		{tool: "future_unknown_tool", blocked: true},
	}
	for _, test := range tests {
		t.Run(test.tool, func(t *testing.T) {
			if got := planModeBlocksTool(test.tool); got != test.blocked {
				t.Fatalf("planModeBlocksTool(%q) = %v, want %v", test.tool, got, test.blocked)
			}
		})
	}
}

func TestPlanModeDenialRunsBeforeEffectfulPreToolHook(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	hookOutput := filepath.Join(ws, "plan-hook-ran")
	if err := os.MkdirAll(filepath.Join(ws, ".carina"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks, err := json.Marshal([]HookSpec{{
		Event:   "PreToolUse",
		Matcher: "patch",
		Command: []string{"sh", "-c", `touch "$1"`, "hook", hookOutput},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, ".carina", "hooks.json"), hooks, 0o600); err != nil {
		t.Fatal(err)
	}
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "probe Plan hook ordering")
	d.setPlanMode(sess.SessionID, true)

	_, outcome := d.executeActionOutcome(sess, task, &action{
		Tool: "patch", Path: "blocked.txt", Content: "blocked\n",
	})
	if outcome.status != "denied" || outcome.errorCategory != "plan_mode" {
		t.Fatalf("Plan patch outcome = %+v", outcome)
	}
	if _, err := os.Stat(hookOutput); !os.IsNotExist(err) {
		t.Fatalf("Plan-denied tool ran its effectful PreToolUse hook: %v", err)
	}
}

func TestPlanModeCannotExitWithoutApproval(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"on":         true,
	})); err != nil {
		t.Fatal(err)
	}

	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"on":         false,
	})); err == nil || !strings.Contains(err.Error(), "session.approve_plan") {
		t.Fatalf("direct plan exit error = %v", err)
	}
	stored, ok := d.store.Get(sess.SessionID)
	if !ok || !stored.PlanMode || !d.isPlanMode(sess.SessionID) {
		t.Fatalf("rejected direct exit changed plan state: stored=%+v runtime=%v", stored, d.isPlanMode(sess.SessionID))
	}
}

// TestPlanModeSwitchNoticeInjection: session.plan_mode and session.approve_plan
// queue an urgent mode-switch notice into the active task's mailbox (the
// two-tier taskMailbox landed for steer_vs_queue_priority), so a task already
// running sees the switch at the next turn boundary rather than only
// inferring it from a subsequent tool denial.
func TestPlanModeSwitchNoticeInjection(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "x")
	d.sched.SetStatus(task.RunID, "running")

	// A normal-priority steering note is already queued; the mode-switch
	// notice must still drain first (urgent tier).
	d.steer(task.RunID, "please also add tests")

	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"on":         true,
	})); err != nil {
		t.Fatalf("handlePlanMode: %v", err)
	}
	msgs := d.drainMailbox(task.RunID)
	if len(msgs) != 2 {
		t.Fatalf("expected mode-switch notice plus queued steering note, got %#v", msgs)
	}
	if !strings.Contains(msgs[0], "MODE SWITCH") || !strings.Contains(msgs[0], "plan mode is now ON") {
		t.Fatalf("mode-switch notice should drain first (urgent), got %#v", msgs)
	}
	if !strings.Contains(msgs[1], "please also add tests") {
		t.Fatalf("normal-priority note should drain second, got %#v", msgs)
	}

	// handleApprovePlan queues the OFF notice.
	if _, err := d.handleApprovePlan(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
	})); err != nil {
		t.Fatalf("handleApprovePlan: %v", err)
	}
	msgs = d.drainMailbox(task.RunID)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "MODE SWITCH") || !strings.Contains(msgs[0], "plan mode is now OFF") {
		t.Fatalf("expected a single OFF mode-switch notice, got %#v", msgs)
	}

	// No active task in the session: notice injection is a no-op, not an error.
	d.sched.SetStatus(task.RunID, "completed")
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"on":         true,
	})); err != nil {
		t.Fatalf("handlePlanMode with no active task: %v", err)
	}
	if got := d.drainMailbox(task.RunID); len(got) != 0 {
		t.Fatalf("no active task should mean no mailbox notice, got %#v", got)
	}
}

func TestApproveCompletedPlanSubmitsOneInheritedBuildTask(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	plan := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "design it", "openai/gpt-5", "plan", nil)
	d.sched.SetLocale(plan.RunID, "zh")
	d.sched.SetModelState(plan.RunID, "openai/gpt-5", "openai/gpt-5")
	d.sched.SetReasoningEffortState(plan.RunID, "high", "high")
	if _, err := d.sched.SetTerminalResultFenced(plan.RunID, 0, "completed", "1. inspect\n2. implement\n3. verify", nil); err != nil {
		t.Fatal(err)
	}
	d.sched.SetResultKind(plan.RunID, "plan")
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{"session_id": sess.SessionID, "on": true})); err != nil {
		t.Fatal(err)
	}

	resultAny, err := d.handleApprovePlan(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	build, ok := result["task"].(*scheduler.ExecutionRun)
	if !ok || build == nil {
		t.Fatalf("approval result missing build task: %#v", result)
	}
	if build.Agent != "build" || build.Model != "openai/gpt-5" || build.RequestedReasoningEffort != "high" || build.Locale != "zh" {
		t.Fatalf("build task did not inherit plan execution choices: %+v", build)
	}
	if build.UserPrompt != "Implement this approved plan:\n\n1. inspect\n2. implement\n3. verify" {
		t.Fatalf("build prompt = %q", build.UserPrompt)
	}

	secondAny, err := d.handleApprovePlan(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	second := secondAny.(map[string]any)["task"].(*scheduler.ExecutionRun)
	if second.RunID != build.RunID {
		t.Fatalf("repeated approval returned %s, want %s", second.RunID, build.RunID)
	}
	if got := len(d.sched.List()); got != 2 {
		t.Fatalf("repeated approval created duplicate tasks: %d", got)
	}
}

func TestApprovePlanExpectedRunRejectsStaleWithoutMutation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)

	stale := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "first plan", "", "plan", nil)
	if _, err := d.sched.SetTerminalResultFenced(stale.RunID, 0, "completed", "1. old", nil); err != nil {
		t.Fatal(err)
	}
	d.sched.SetResultKind(stale.RunID, "plan")
	current := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "revised plan", "", "plan", nil)
	if _, err := d.sched.SetTerminalResultFenced(current.RunID, 0, "completed", "1. current", nil); err != nil {
		t.Fatal(err)
	}
	d.sched.SetResultKind(current.RunID, "plan")
	if latest := d.latestSessionTask(sess.SessionID); latest == nil || latest.RunID != current.RunID {
		t.Fatalf("latest task = %+v, want %s", latest, current.RunID)
	}
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{"session_id": sess.SessionID, "on": true})); err != nil {
		t.Fatal(err)
	}

	beforeTasks := len(d.sched.List())
	_, err := d.handleApprovePlan(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"run_id":     stale.RunID,
	}))
	if err == nil || !strings.Contains(err.Error(), "stale plan run") {
		t.Fatalf("stale approval error = %v", err)
	}
	stored, ok := d.store.Get(sess.SessionID)
	if !ok || !stored.PlanMode || !d.isPlanMode(sess.SessionID) {
		t.Fatalf("stale approval changed Plan mode: stored=%+v runtime=%v", stored, d.isPlanMode(sess.SessionID))
	}
	if got := len(d.sched.List()); got != beforeTasks {
		t.Fatalf("stale approval changed task count: got %d want %d", got, beforeTasks)
	}
	if build := d.approvedPlanBuildTask(sess.SessionID, ""); build != nil {
		t.Fatalf("stale approval created build task: %+v", build)
	}
}

func TestApprovePlanExplicitEmptyRunIDDoesNotUseCompatibilityPath(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{"session_id": sess.SessionID, "on": true})); err != nil {
		t.Fatal(err)
	}

	if _, err := d.handleApprovePlan(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"run_id":     "   ",
	})); err == nil || !strings.Contains(err.Error(), "run_id must not be empty") {
		t.Fatalf("empty run_id error = %v", err)
	}
	stored, _ := d.store.Get(sess.SessionID)
	if !stored.PlanMode || !d.isPlanMode(sess.SessionID) {
		t.Fatalf("empty run_id changed Plan mode: stored=%+v runtime=%v", stored, d.isPlanMode(sess.SessionID))
	}
}

func TestApprovePlanExpectedRunRequiresLatestCompletedTypedPlan(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	tests := []struct {
		name       string
		agent      string
		status     string
		resultKind string
		summary    string
	}{
		{name: "answer", agent: "plan", status: "completed", resultKind: "answer", summary: "hello"},
		{name: "active", agent: "plan", status: "running", resultKind: "plan", summary: "1. inspect"},
		{name: "build agent", agent: "build", status: "completed", resultKind: "plan", summary: "1. inspect"},
		{name: "empty summary", agent: "plan", status: "completed", resultKind: "plan"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sess, _ := d.store.CreateSession(ws, "full-workspace")
			d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
			run := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, test.name, "", test.agent, nil)
			d.sched.SetResultKind(run.RunID, test.resultKind)
			if test.status == "completed" {
				if _, err := d.sched.SetTerminalResultFenced(run.RunID, 0, "completed", test.summary, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				d.sched.SetStatus(run.RunID, test.status)
			}
			if _, err := d.handlePlanMode(mustJSON(t, map[string]any{"session_id": sess.SessionID, "on": true})); err != nil {
				t.Fatal(err)
			}

			if _, err := d.handleApprovePlan(mustJSON(t, map[string]any{
				"session_id": sess.SessionID,
				"run_id":     run.RunID,
			})); err == nil || !strings.Contains(err.Error(), "latest completed typed plan") {
				t.Fatalf("approval error = %v", err)
			}
			stored, _ := d.store.Get(sess.SessionID)
			if !stored.PlanMode || !d.isPlanMode(sess.SessionID) {
				t.Fatalf("invalid expected run changed Plan mode: stored=%+v runtime=%v", stored, d.isPlanMode(sess.SessionID))
			}
		})
	}
}

func TestApprovePlanExpectedRunRetryIsIdempotent(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	plan := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "design it", "", "plan", nil)
	if _, err := d.sched.SetTerminalResultFenced(plan.RunID, 0, "completed", "1. inspect\n2. implement", nil); err != nil {
		t.Fatal(err)
	}
	d.sched.SetResultKind(plan.RunID, "plan")
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{"session_id": sess.SessionID, "on": true})); err != nil {
		t.Fatal(err)
	}
	params := mustJSON(t, map[string]any{"session_id": sess.SessionID, "run_id": plan.RunID})

	firstAny, err := d.handleApprovePlan(params)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(map[string]any)["task"].(*scheduler.ExecutionRun)
	secondAny, err := d.handleApprovePlan(params)
	if err != nil {
		t.Fatal(err)
	}
	second := secondAny.(map[string]any)["task"].(*scheduler.ExecutionRun)
	if second.RunID != first.RunID || len(d.sched.List()) != 2 {
		t.Fatalf("expected-run retry was not idempotent: first=%s second=%s tasks=%d", first.RunID, second.RunID, len(d.sched.List()))
	}

	omittedAny, err := d.handleApprovePlan(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	omitted := omittedAny.(map[string]any)["task"].(*scheduler.ExecutionRun)
	if omitted.RunID != first.RunID || len(d.sched.List()) != 2 {
		t.Fatalf("omitted-run retry drifted: first=%s omitted=%s tasks=%d", first.RunID, omitted.RunID, len(d.sched.List()))
	}
}

func TestApprovePlanExpectedRunRetryRejectsASupersededApproval(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)

	approve := func(prompt, summary string) (*scheduler.ExecutionRun, *scheduler.ExecutionRun) {
		plan := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, prompt, "", "plan", nil)
		if _, err := d.sched.SetTerminalResultFenced(plan.RunID, 0, "completed", summary, nil); err != nil {
			t.Fatal(err)
		}
		d.sched.SetResultKind(plan.RunID, "plan")
		if _, err := d.handlePlanMode(mustJSON(t, map[string]any{"session_id": sess.SessionID, "on": true})); err != nil {
			t.Fatal(err)
		}
		approvedAny, err := d.handleApprovePlan(mustJSON(t, map[string]any{
			"session_id": sess.SessionID,
			"run_id":     plan.RunID,
		}))
		if err != nil {
			t.Fatal(err)
		}
		return plan, approvedAny.(map[string]any)["task"].(*scheduler.ExecutionRun)
	}

	firstPlan, firstBuild := approve("first plan", "1. first")
	if _, err := d.sched.SetTerminalResultFenced(firstBuild.RunID, 0, "completed", "implemented first", nil); err != nil {
		t.Fatal(err)
	}
	_, secondBuild := approve("second plan", "1. second")
	// A later lifecycle update to the old Build must not reclaim approval identity.
	d.sched.SetStatus(firstBuild.RunID, "completed")
	beforeTasks := len(d.sched.List())

	_, err := d.handleApprovePlan(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"run_id":     firstPlan.RunID,
	}))
	if err == nil || !strings.Contains(err.Error(), "stale plan run") {
		t.Fatalf("superseded approval retry error = %v", err)
	}
	if got := len(d.sched.List()); got != beforeTasks {
		t.Fatalf("superseded retry changed task count: got %d want %d", got, beforeTasks)
	}
	if latest := d.approvedPlanBuildTask(sess.SessionID, ""); latest == nil || latest.RunID != secondBuild.RunID {
		t.Fatalf("latest approved Build = %+v, want %s", latest, secondBuild.RunID)
	}
}

func TestPlanAgentResultKindControlsReviewability(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []string
		want  string
	}{
		{name: "answer", steps: []string{`{"tool":"done","summary":"你好！","result_kind":"answer"}`}, want: "answer"},
		{name: "plan", steps: []string{`{"tool":"done","summary":"1. inspect\n2. implement\n3. verify","result_kind":"plan"}`}, want: "plan"},
		{name: "missing is corrected", steps: []string{
			`{"tool":"done","summary":"你好！"}`,
			`{"tool":"done","summary":"你好！","result_kind":"answer"}`,
		}, want: "answer"},
		{name: "invalid is corrected", steps: []string{
			`{"tool":"done","summary":"你好！","result_kind":"conversation"}`,
			`{"tool":"done","summary":"你好！","result_kind":"answer"}`,
		}, want: "answer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, ws := newLoopDaemon(t)
			defer d.Close()
			d.SetReasoner(&scriptedReasoner{steps: tc.steps})
			sess, _ := d.store.CreateSession(ws, "safe-edit")
			d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
			task := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "你好", "", "plan", nil)
			d.runTask(sess, task)
			got, ok := d.sched.Get(task.RunID)
			if !ok || got.Status != "completed" || got.ResultKind != tc.want {
				t.Fatalf("completed run = %+v, want result_kind %q", got, tc.want)
			}
		})
	}
}

func TestApprovePlanAnswerOnlyExitsPlanMode(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	answer := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "你好", "", "plan", nil)
	d.sched.SetResultKind(answer.RunID, "answer")
	if _, err := d.sched.SetTerminalResultFenced(answer.RunID, 0, "completed", "你好！", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{"session_id": sess.SessionID, "on": true})); err != nil {
		t.Fatal(err)
	}
	resultAny, err := d.handleApprovePlan(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["task"] != nil || d.isPlanMode(sess.SessionID) || len(d.sched.List()) != 1 {
		t.Fatalf("answer approval must only exit Plan mode: %#v", result)
	}
}

func TestApproveActivePlanOnlyExitsModeAndNotifiesTask(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil)
	plan := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "design it", "", "plan", nil)
	d.sched.SetStatus(plan.RunID, "running")
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{"session_id": sess.SessionID, "on": true})); err != nil {
		t.Fatal(err)
	}
	_ = d.drainMailbox(plan.RunID)

	resultAny, err := d.handleApprovePlan(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	result := resultAny.(map[string]any)
	if result["task"] != nil || d.isPlanMode(sess.SessionID) {
		t.Fatalf("active approval must only exit mode: %#v", result)
	}
	msgs := d.drainMailbox(plan.RunID)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "plan mode is now OFF") {
		t.Fatalf("active plan task did not receive OFF notice: %#v", msgs)
	}
	if got := len(d.sched.List()); got != 1 {
		t.Fatalf("active plan approval created another task: %d", got)
	}
}
