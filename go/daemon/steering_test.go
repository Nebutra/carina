package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/channels"
)

// TestAsyncSteering: a message queued for a task is drained into the agent's
// prompt at the next turn boundary (redirect a running agent without restart).
func TestAsyncSteering(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")

	// Queue a steering message before the loop runs.
	d.steer(task.RunID, "please also add tests")

	cap := &capturingReasoner{}
	d.SetReasoner(cap)
	d.runTask(sess, task)

	if !strings.Contains(cap.lastPrompt, "please also add tests") {
		t.Fatalf("steering message should reach the agent prompt, got:\n%s", cap.lastPrompt)
	}
	// Mailbox must be drained (not re-delivered).
	if len(d.drainMailbox(task.RunID)) != 0 {
		t.Fatal("mailbox should be empty after draining")
	}
}

type lateSteerReasoner struct {
	started chan struct{}
	release chan struct{}
	calls   int
	last    string
}

func (r *lateSteerReasoner) Name() string { return "late-steer" }
func (r *lateSteerReasoner) Think(ctx context.Context, prompt string) (string, error) {
	r.calls++
	r.last = prompt
	if r.calls == 1 {
		close(r.started)
		select {
		case <-r.release:
		case <-ctx.Done():
			return "", context.Cause(ctx)
		}
	}
	return `{"tool":"done","summary":"done"}`, nil
}

func TestSteerArrivingDuringModelInvalidatesStaleDone(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	reasoner := &lateSteerReasoner{started: make(chan struct{}), release: make(chan struct{})}
	d.SetReasoner(reasoner)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")
	done := make(chan struct{})
	go func() {
		d.runTask(sess, task)
		close(done)
	}()
	<-reasoner.started
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id": task.RunID, "message": "include the late requirement", "steer_id": "steer_late",
	})); err != nil {
		t.Fatal(err)
	}
	close(reasoner.release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish after late steer")
	}
	if reasoner.calls != 2 || !strings.Contains(reasoner.last, "include the late requirement") {
		t.Fatalf("late steer did not invalidate stale done: calls=%d prompt=%s", reasoner.calls, reasoner.last)
	}
}

func TestSteerQueuePersistsStableIDsAndReconnectDepth(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")

	params := mustJSON(t, map[string]any{
		"run_id": task.RunID, "message": "add reconnect coverage", "steer_id": "steer_stable", "priority": "normal",
	})
	first, err := d.handleTaskSteer(params)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.handleTaskSteer(params)
	if err != nil {
		t.Fatal(err)
	}
	if first.(map[string]any)["queue_depth"] != 1 || second.(map[string]any)["queue_depth"] != 1 {
		t.Fatalf("idempotent steer changed queue depth: first=%#v second=%#v", first, second)
	}
	records := newRunStore(filepath.Dir(d.runs.dir)).loadExecutionControls()
	record := records[task.RunID]
	if len(record.Normal) != 1 || record.Normal[0].SteerID != "steer_stable" {
		t.Fatalf("reconnected control record = %#v", record)
	}
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id": task.RunID, "message": "different", "steer_id": "steer_stable",
	})); err == nil {
		t.Fatal("reusing steer_id with different content must fail closed")
	}
	status, err := d.handleTaskStatus(mustJSON(t, map[string]any{"run_id": task.RunID}))
	if err != nil || status.(map[string]any)["queue_depth"] != 1 {
		t.Fatalf("status queue projection = %#v err=%v", status, err)
	}
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id": task.RunID, "message": strings.Repeat("x", maxSteerBytes+1),
	})); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized steer must be rejected, got %v", err)
	}
}

func TestSteerQueueBoundRejectsWithoutMutation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")
	box := &taskMailbox{}
	for index := 0; index < maxQueuedSteers; index++ {
		box.pushEntry(queuedSteer{SteerID: fmt.Sprintf("steer_%d", index), Message: "queued", Priority: steerNormal})
	}
	d.mailbox[task.RunID] = box
	if _, err := d.enqueueSteer(task.RunID, "steer_overflow", "overflow", steerNormal); err == nil || !strings.Contains(err.Error(), "queue is full") {
		t.Fatalf("full queue must reject, got %v", err)
	}
	if got := d.queueDepth(task.RunID); got != maxQueuedSteers {
		t.Fatalf("rejected enqueue mutated depth: %d", got)
	}
}

func TestExecutionQueueListTruncatesPreviewAndDropRemoves(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")
	long := strings.Repeat("secret-token-value-", 8)
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id": task.RunID, "message": long, "steer_id": "steer_preview", "priority": "urgent",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id": task.RunID, "message": "second follow-up", "steer_id": "steer_second",
	})); err != nil {
		t.Fatal(err)
	}
	listed, err := d.handleExecutionQueueList(mustJSON(t, map[string]any{
		"run_id": task.RunID, "preview_cells": 12,
	}))
	if err != nil {
		t.Fatal(err)
	}
	payload := listed.(map[string]any)
	if payload["queue_depth"] != 2 {
		t.Fatalf("list depth = %#v", payload["queue_depth"])
	}
	items, ok := payload["items"].([]map[string]any)
	if !ok || len(items) != 2 {
		t.Fatalf("items = %#v", payload["items"])
	}
	first := items[0]
	preview, _ := first["preview"].(string)
	if preview == long || !strings.HasSuffix(preview, "…") || len([]rune(preview)) > 12 {
		t.Fatalf("preview must be truncated to cells, got %q", preview)
	}
	if first["steer_id"] != "steer_preview" || first["priority"] != "urgent" {
		t.Fatalf("first item = %#v", first)
	}
	dropped, err := d.handleExecutionQueueDrop(mustJSON(t, map[string]any{
		"run_id": task.RunID, "steer_id": "steer_preview",
	}))
	if err != nil {
		t.Fatal(err)
	}
	dropPayload := dropped.(map[string]any)
	if dropPayload["dropped"] != true || dropPayload["queue_depth"] != 1 {
		t.Fatalf("drop = %#v", dropPayload)
	}
	if d.queueDepth(task.RunID) != 1 {
		t.Fatalf("depth after drop = %d", d.queueDepth(task.RunID))
	}
	// Idempotent drop of missing id
	again, err := d.handleExecutionQueueDrop(mustJSON(t, map[string]any{
		"run_id": task.RunID, "steer_id": "steer_preview",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if again.(map[string]any)["dropped"] != false {
		t.Fatalf("missing drop should be false: %#v", again)
	}
}

func TestTruncateSteerPreview(t *testing.T) {
	if got := truncateSteerPreview("short", 48); got != "short" {
		t.Fatalf("short = %q", got)
	}
	got := truncateSteerPreview("abcdefghijklmnop", 8)
	if got != "abcdefg…" {
		t.Fatalf("truncated = %q", got)
	}
}

func TestSoftInterruptWaitsForLongToolAndLeavesNoOrphanLifecycle(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	d.SetReasoner(&scriptedReasoner{steps: []string{
		`{"tool":"run","command":["sh","-c","sleep 0.5; echo completed"]}`,
		`{"tool":"done","summary":"done"}`,
	}})
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "run a long tool")
	done := make(chan struct{})
	go func() {
		d.runTask(sess, task)
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for !d.hasActiveToolCall(task.RunID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !d.hasActiveToolCall(task.RunID) {
		t.Fatal("long command never entered active tool lifecycle")
	}
	result, err := d.handleTaskInterrupt(mustJSON(t, map[string]any{"run_id": task.RunID, "mode": "soft"}))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["active_tool"] != true {
		t.Fatalf("interrupt did not report active tool: %#v", result)
	}
	if current, _ := d.sched.Get(task.RunID); current.Status != "running" {
		t.Fatalf("soft interrupt cancelled active tool immediately: status=%s", current.Status)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("soft-interrupted long tool did not reach safe point")
	}
	current, _ := d.sched.Get(task.RunID)
	if current.Status != "paused" {
		t.Fatalf("status after safe-point interrupt = %s, want paused", current.Status)
	}
	if d.runs.loadCheckpoint(task.RunID) == nil {
		t.Fatal("soft interrupt did not leave a resumable checkpoint")
	}
	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var events []struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	started := map[string]bool{}
	terminal := map[string]bool{}
	for _, event := range events {
		callID, _ := event.Payload["call_id"].(string)
		switch event.Type {
		case "ToolCallStarted":
			started[callID] = true
		case "ToolCallCompleted", "ToolCallFailed", "ToolCallDenied", "ToolCallCancelled":
			terminal[callID] = true
		}
	}
	for callID := range started {
		if !terminal[callID] {
			t.Fatalf("orphan ToolCallStarted after soft interrupt: %s", callID)
		}
	}
}

func TestSoftInterruptPausesActiveSessionGoal(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := d.handleGoalSet(mustJSON(t, map[string]any{
		"session_id": sess.SessionID, "objective": "keep going", "auto_continue": true,
	})); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "interrupt me")
	if _, _, err := d.requestSoftInterrupt(task.RunID); err != nil {
		t.Fatal(err)
	}
	if !d.pauseForSoftInterrupt(sess, task, &Transcript{}, 0, "") {
		t.Fatal("soft interrupt did not land")
	}
	current, _ := d.sched.Get(task.RunID)
	if current.Status != "paused" {
		t.Fatalf("run status=%s, want paused", current.Status)
	}
	result, err := d.handleGoalGet(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	goal := result.(map[string]any)["goal"].(sessionGoal)
	if goal.Status != "paused" {
		t.Fatalf("goal status=%s, want paused", goal.Status)
	}
	d.reconcileGoalTask(current)
	after, err := d.handleGoalGet(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	if after.(map[string]any)["goal"].(sessionGoal).Status != "paused" {
		t.Fatal("paused goal resumed after reconcile")
	}
	if n := len(d.sched.List()); n != 1 {
		t.Fatalf("interrupt launched a continuation: %d tasks", n)
	}
}

func TestSoftInterruptWithoutGoalStillPausesRun(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "no goal")
	if _, _, err := d.requestSoftInterrupt(task.RunID); err != nil {
		t.Fatal(err)
	}
	if !d.pauseForSoftInterrupt(sess, task, &Transcript{}, 0, "") {
		t.Fatal("soft interrupt did not land")
	}
	current, _ := d.sched.Get(task.RunID)
	if current.Status != "paused" {
		t.Fatalf("run status=%s, want paused", current.Status)
	}
	result, err := d.handleGoalGet(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["goal"] != nil {
		t.Fatalf("missing goal was created: %#v", result)
	}
}

// TestTaskMailboxDrainOrdersUrgentBeforeNormal: the taskMailbox primitive
// itself must always yield urgent messages first, each tier preserving its
// own FIFO arrival order.
func TestTaskMailboxDrainOrdersUrgentBeforeNormal(t *testing.T) {
	m := &taskMailbox{}
	m.push(steerNormal, "normal-1")
	m.push(steerUrgent, "urgent-1")
	m.push(steerNormal, "normal-2")
	m.push(steerUrgent, "urgent-2")

	got := m.drain()
	want := []string{"urgent-1", "urgent-2", "normal-1", "normal-2"}
	if len(got) != len(want) {
		t.Fatalf("drain() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drain()[%d] = %q, want %q (full: %#v)", i, got[i], want[i], got)
		}
	}
}

// TestTaskMailboxDrainEmpty: draining an empty or nil mailbox must not panic
// and must return no messages.
func TestTaskMailboxDrainEmpty(t *testing.T) {
	var nilBox *taskMailbox
	if got := nilBox.drain(); len(got) != 0 {
		t.Fatalf("nil mailbox drain() = %#v, want empty", got)
	}
	if !nilBox.empty() {
		t.Fatal("nil mailbox should report empty")
	}

	m := &taskMailbox{}
	if got := m.drain(); len(got) != 0 {
		t.Fatalf("zero-value mailbox drain() = %#v, want empty", got)
	}
	if !m.empty() {
		t.Fatal("zero-value mailbox should report empty")
	}
}

// TestParseSteerPriority: only "", "normal", and "urgent" are accepted;
// anything else must fail closed rather than silently default.
func TestParseSteerPriority(t *testing.T) {
	cases := []struct {
		in      string
		want    steerPriority
		wantErr bool
	}{
		{"", steerNormal, false},
		{"normal", steerNormal, false},
		{"urgent", steerUrgent, false},
		{"  urgent  ", steerUrgent, false},
		{"URGENT", "", true},
		{"critical", "", true},
	}
	for _, c := range cases {
		got, err := parseSteerPriority(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseSteerPriority(%q) = %q, nil; want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSteerPriority(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSteerPriority(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestDaemonMailboxDrainPrioritizesUrgent: at the Daemon level, an urgent
// steering message queued after several normal ones must still drain first,
// and draining clears the whole mailbox for that task.
func TestDaemonMailboxDrainPrioritizesUrgent(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")

	d.steer(task.RunID, "please also add tests")
	d.steer(task.RunID, "and update the docs")
	d.steerWithPriority(task.RunID, "STOP: abort the current approach", steerUrgent)

	got := d.drainMailbox(task.RunID)
	want := []string{"STOP: abort the current approach", "please also add tests", "and update the docs"}
	if len(got) != len(want) {
		t.Fatalf("drainMailbox = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drainMailbox[%d] = %q, want %q (full: %#v)", i, got[i], want[i], got)
		}
	}
	if len(d.drainMailbox(task.RunID)) != 0 {
		t.Fatal("mailbox should be empty after draining")
	}
}

// TestTaskSteerAcceptsExplicitPriority: execution.steer's priority param round
// trips through handleTaskSteer into the mailbox and back out via the RPC
// result, and rejects unknown priority values instead of guessing.
func TestTaskSteerAcceptsExplicitPriority(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")

	res, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id":   task.RunID,
		"message":  "drop everything",
		"priority": "urgent",
	}))
	if err != nil {
		t.Fatalf("urgent steer should be accepted: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok || m["priority"] != "urgent" {
		t.Fatalf("handleTaskSteer result = %#v, want priority=urgent", res)
	}

	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id":   task.RunID,
		"message":  "bogus",
		"priority": "critical",
	})); err == nil || !strings.Contains(err.Error(), "invalid priority") {
		t.Fatalf("unknown priority should be rejected, got err=%v", err)
	}

	got := d.drainMailbox(task.RunID)
	if len(got) != 1 || got[0] != "drop everything" {
		t.Fatalf("mailbox after rejected priority = %#v", got)
	}
}

// TestChannelEventSteersUrgentAheadOfQueuedNormalMessage: the ecosystem.go
// channel-event call site must use urgent priority so a time-sensitive
// external event (e.g. a CI failure) jumps ahead of routine steering notes
// already queued for the active task.
func TestChannelEventSteersUrgentAheadOfQueuedNormalMessage(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "watch CI")
	d.sched.SetStatus(task.RunID, "running")

	// A normal-priority note is already queued before the channel event
	// arrives.
	d.steer(task.RunID, "please also add tests")

	secret := []byte(strings.Repeat("c", 32))
	if err := d.channels.Register(channels.Sender{ID: "ci", Secret: secret, Sessions: []string{sess.SessionID}, Kinds: []string{"build"}}); err != nil {
		t.Fatal(err)
	}
	event := channels.Event{ID: "evt-priority", SenderID: "ci", SessionID: sess.SessionID, Kind: "build", Timestamp: time.Now().UTC(), Payload: map[string]any{"status": "failed"}}
	raw, _ := json.Marshal(map[string]any{"event": event, "signature": channels.Sign(secret, event)})
	if _, err := d.handleChannelEventInject(raw); err != nil {
		t.Fatal(err)
	}

	messages := d.drainMailbox(task.RunID)
	if len(messages) != 2 {
		t.Fatalf("expected both messages queued, got %#v", messages)
	}
	if !strings.Contains(messages[0], "CHANNEL EVENT build") {
		t.Fatalf("urgent channel event should drain first, got %#v", messages)
	}
	if !strings.Contains(messages[1], "please also add tests") {
		t.Fatalf("normal message should drain second, got %#v", messages)
	}
}

func TestTaskSteerRejectsUnknownAndTerminalTasks(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")

	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id":  task.RunID,
		"message": " also add tests ",
	})); err != nil {
		t.Fatalf("queued task should accept steering: %v", err)
	}
	if got := d.drainMailbox(task.RunID); len(got) != 1 || got[0] != "also add tests" {
		t.Fatalf("steering mailbox = %#v", got)
	}

	d.sched.SetStatus(task.RunID, "completed")
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id":  task.RunID,
		"message": "too late",
	})); err == nil || !strings.Contains(err.Error(), "cannot be steered") {
		t.Fatalf("terminal task steer error = %v", err)
	}
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"run_id":  "run_missing",
		"message": "hello",
	})); err == nil || !strings.Contains(err.Error(), "unknown execution") {
		t.Fatalf("unknown execution steer error = %v", err)
	}
}

func TestTerminalControlCleanupRemovesDurableQueue(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")
	if _, err := d.enqueueSteer(task.RunID, "steer_terminal", "late", steerNormal); err != nil {
		t.Fatal(err)
	}
	d.sched.SetStatus(task.RunID, "completed")
	if err := d.cleanupTerminalExecutionControl(task.RunID); err != nil {
		t.Fatal(err)
	}
	if d.queueDepth(task.RunID) != 0 {
		t.Fatal("terminal queue remained in memory")
	}
	if _, ok := d.runs.loadExecutionControls()[task.RunID]; ok {
		t.Fatal("terminal queue remained durable")
	}
	if _, err := d.enqueueSteer(task.RunID, "steer_after_terminal", "too late", steerNormal); err == nil {
		t.Fatal("terminal task accepted a raced steer")
	}
}

func TestPausedControlCleanupPreservesFollowUps(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")
	if _, err := d.enqueueSteer(task.RunID, "steer_paused", "after resume", steerNormal); err != nil {
		t.Fatal(err)
	}
	d.sched.SetStatus(task.RunID, "paused")
	if err := d.cleanupTerminalExecutionControl(task.RunID); err != nil {
		t.Fatal(err)
	}
	if d.queueDepth(task.RunID) != 1 {
		t.Fatal("paused cleanup discarded a queued follow-up")
	}
}
