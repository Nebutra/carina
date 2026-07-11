package daemon

import (
	"encoding/json"
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
	d.steer(task.TaskID, "please also add tests")

	cap := &capturingReasoner{}
	d.SetReasoner(cap)
	d.runTask(sess, task)

	if !strings.Contains(cap.lastPrompt, "please also add tests") {
		t.Fatalf("steering message should reach the agent prompt, got:\n%s", cap.lastPrompt)
	}
	// Mailbox must be drained (not re-delivered).
	if len(d.drainMailbox(task.TaskID)) != 0 {
		t.Fatal("mailbox should be empty after draining")
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

	d.steer(task.TaskID, "please also add tests")
	d.steer(task.TaskID, "and update the docs")
	d.steerWithPriority(task.TaskID, "STOP: abort the current approach", steerUrgent)

	got := d.drainMailbox(task.TaskID)
	want := []string{"STOP: abort the current approach", "please also add tests", "and update the docs"}
	if len(got) != len(want) {
		t.Fatalf("drainMailbox = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("drainMailbox[%d] = %q, want %q (full: %#v)", i, got[i], want[i], got)
		}
	}
	if len(d.drainMailbox(task.TaskID)) != 0 {
		t.Fatal("mailbox should be empty after draining")
	}
}

// TestTaskSteerAcceptsExplicitPriority: task.steer's priority param round
// trips through handleTaskSteer into the mailbox and back out via the RPC
// result, and rejects unknown priority values instead of guessing.
func TestTaskSteerAcceptsExplicitPriority(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "work")

	res, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"task_id":  task.TaskID,
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
		"task_id":  task.TaskID,
		"message":  "bogus",
		"priority": "critical",
	})); err == nil || !strings.Contains(err.Error(), "invalid priority") {
		t.Fatalf("unknown priority should be rejected, got err=%v", err)
	}

	got := d.drainMailbox(task.TaskID)
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
	d.sched.SetStatus(task.TaskID, "running")

	// A normal-priority note is already queued before the channel event
	// arrives.
	d.steer(task.TaskID, "please also add tests")

	secret := []byte(strings.Repeat("c", 32))
	if err := d.channels.Register(channels.Sender{ID: "ci", Secret: secret, Sessions: []string{sess.SessionID}, Kinds: []string{"build"}}); err != nil {
		t.Fatal(err)
	}
	event := channels.Event{ID: "evt-priority", SenderID: "ci", SessionID: sess.SessionID, Kind: "build", Timestamp: time.Now().UTC(), Payload: map[string]any{"status": "failed"}}
	raw, _ := json.Marshal(map[string]any{"event": event, "signature": channels.Sign(secret, event)})
	if _, err := d.handleChannelEventInject(raw); err != nil {
		t.Fatal(err)
	}

	messages := d.drainMailbox(task.TaskID)
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
		"task_id": task.TaskID,
		"message": " also add tests ",
	})); err != nil {
		t.Fatalf("queued task should accept steering: %v", err)
	}
	if got := d.drainMailbox(task.TaskID); len(got) != 1 || got[0] != "also add tests" {
		t.Fatalf("steering mailbox = %#v", got)
	}

	d.sched.SetStatus(task.TaskID, "completed")
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"task_id": task.TaskID,
		"message": "too late",
	})); err == nil || !strings.Contains(err.Error(), "cannot be steered") {
		t.Fatalf("terminal task steer error = %v", err)
	}
	if _, err := d.handleTaskSteer(mustJSON(t, map[string]any{
		"task_id": "task_missing",
		"message": "hello",
	})); err == nil || !strings.Contains(err.Error(), "unknown task") {
		t.Fatalf("unknown task steer error = %v", err)
	}
}
