package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	d.sched.SetStatus(task.TaskID, "running")

	// A normal-priority steering note is already queued; the mode-switch
	// notice must still drain first (urgent tier).
	d.steer(task.TaskID, "please also add tests")

	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"on":         true,
	})); err != nil {
		t.Fatalf("handlePlanMode: %v", err)
	}
	msgs := d.drainMailbox(task.TaskID)
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
	msgs = d.drainMailbox(task.TaskID)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "MODE SWITCH") || !strings.Contains(msgs[0], "plan mode is now OFF") {
		t.Fatalf("expected a single OFF mode-switch notice, got %#v", msgs)
	}

	// No active task in the session: notice injection is a no-op, not an error.
	d.sched.SetStatus(task.TaskID, "completed")
	if _, err := d.handlePlanMode(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"on":         true,
	})); err != nil {
		t.Fatalf("handlePlanMode with no active task: %v", err)
	}
	if got := d.drainMailbox(task.TaskID); len(got) != 0 {
		t.Fatalf("no active task should mean no mailbox notice, got %#v", got)
	}
}
