package daemon

import (
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

	// Approving the plan lets edits through the plan gate.
	d.setPlanMode(sess.SessionID, false)
	obs = d.executeAction(sess, task, &action{Tool: "patch", Path: "a.txt", Content: "y\n"})
	if strings.Contains(obs, "plan mode") {
		t.Fatalf("patch should pass the plan gate after approval, got: %s", obs)
	}
}

func TestPlanModeToolMatrix(t *testing.T) {
	for tool, blocked := range map[string]bool{
		"read": false, "list": false, "search": false,
		"todo": false, "update_plan": false,
		"patch": true, "run": true, "memory": true,
	} {
		if got := planModeBlocksTool(tool); got != blocked {
			t.Fatalf("planModeBlocksTool(%q) = %v, want %v", tool, got, blocked)
		}
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
