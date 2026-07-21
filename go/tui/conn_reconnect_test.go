package tui

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

// fakeSender collects messages Connect() sends, safe for concurrent use from
// the connection goroutine.
type fakeSender struct {
	mu   sync.Mutex
	msgs []tea.Msg
}

func (f *fakeSender) Send(msg tea.Msg) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, msg)
}

func (f *fakeSender) snapshot() []tea.Msg {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]tea.Msg, len(f.msgs))
	copy(out, f.msgs)
	return out
}

func (f *fakeSender) countEvents(match func(EventMsg) bool) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, msg := range f.msgs {
		if ev, ok := msg.(EventMsg); ok && match(ev) {
			n++
		}
	}
	return n
}

func (f *fakeSender) waitFor(t *testing.T, timeout time.Duration, match func(tea.Msg) bool) tea.Msg {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, m := range f.snapshot() {
			if match(m) {
				return m
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for a matching message; got %+v", f.snapshot())
	return nil
}

func TestQuestionTrackerReopensOnlyUnresolvedRequest(t *testing.T) {
	tracker := newQuestionTracker()
	request := map[string]any{
		"type": "user.question", "question_id": "question_open", "task_id": "task_1",
		"prompt": "Choose", "options": []any{map[string]any{"label": "A", "value": "a"}, map[string]any{"label": "B", "value": "b"}},
	}
	tracker.observeAudit(map[string]any{
		"type": "ToolRequested", "payload": map[string]any{
			"status": "user_question_requested", "question_id": "question_open", "request": request,
		},
	})
	resolved := map[string]any{
		"type": "user.question", "question_id": "question_resolved", "task_id": "task_1",
		"prompt": "Old", "options": []any{map[string]any{"label": "A", "value": "a"}, map[string]any{"label": "B", "value": "b"}},
	}
	tracker.observeAudit(map[string]any{
		"type": "ToolRequested", "payload": map[string]any{
			"status": "user_question_requested", "question_id": "question_resolved", "request": resolved,
		},
	})
	tracker.observeAudit(map[string]any{
		"type": "TaskCreated", "payload": map[string]any{
			"status": "user_question_resolved", "question_id": "question_resolved", "value": "a",
		},
	})

	sender := &fakeSender{}
	tracker.flush(sender, "sess_test", 1)
	messages := sender.snapshot()
	if len(messages) != 1 {
		t.Fatalf("flushed questions = %d, want 1", len(messages))
	}
	event, ok := messages[0].(EventMsg)
	if !ok || event.Raw["question_id"] != "question_open" {
		t.Fatalf("unexpected reopened question: %#v", messages[0])
	}
	if tracker.forwardTransient(request) {
		t.Fatal("replayed question was delivered twice")
	}
	if tracker.forwardTransient(resolved) {
		t.Fatal("resolved question was reopened")
	}
}

func TestQuestionTrackerDropsCrashStaleRequest(t *testing.T) {
	tracker := newQuestionTracker()
	tracker.observeAudit(map[string]any{
		"type": "ToolRequested", "payload": map[string]any{
			"status": "user_question_requested",
			"request": map[string]any{
				"type": "user.question", "question_id": "question_stale", "task_id": "task_1",
				"prompt": "Old question", "options": []any{map[string]any{"label": "A", "value": "a"}},
			},
		},
	})
	call := &fakeCaller{handler: map[string]any{
		"task.user.pending": map[string]any{"question_ids": []string{}},
	}}
	tracker.reconcile(call)
	sender := &fakeSender{}
	tracker.flush(sender, "sess_test", 1)
	if got := len(sender.snapshot()); got != 0 {
		t.Fatalf("stale questions reopened = %d, want 0", got)
	}
}

// shortSocketDir returns a short-path temp directory for a unix domain
// socket: t.TempDir() embeds the full (possibly long) test name, which can
// exceed the ~104-108 byte sockaddr_un limit (macOS in particular) and make
// net.Listen("unix", ...) fail silently in a background goroutine.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", fmt.Sprintf("carina-tui-%d", os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// fakeDaemon runs go/tui/testdata/fakedaemon as a real OS process: killing
// it (not just closing an in-process listener) actually closes its socket
// connections, the way a genuine daemon crash or restart does — an
// in-process fake rpc.Server cannot reproduce that (Server.Close only stops
// accepting new connections; already-accepted ones stay open).
type fakeDaemon struct {
	cmd        *exec.Cmd
	sock       string
	eventsPath string
}

func startFakeDaemon(t *testing.T, bin, dir string) *fakeDaemon {
	t.Helper()
	sock := filepath.Join(dir, "d.sock")
	eventsPath := filepath.Join(dir, "events.ndjson")
	f, err := os.OpenFile(eventsPath, os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"CARINA_FAKEDAEMON_SOCKET="+sock,
		"CARINA_FAKEDAEMON_EVENTS="+eventsPath,
	)
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fakedaemon: %v", err)
	}
	fd := &fakeDaemon{cmd: cmd, sock: sock, eventsPath: eventsPath}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return fd
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	t.Fatalf("fakedaemon socket never appeared at %s; output:\n%s", sock, logs.String())
	return fd
}

func (fd *fakeDaemon) kill(t *testing.T) {
	t.Helper()
	if fd.cmd.Process != nil {
		_ = fd.cmd.Process.Kill()
	}
	_ = fd.cmd.Wait()
}

// publish appends one JSON event line for the fakedaemon to relay as a
// session.events.stream notification.
func (fd *fakeDaemon) publish(t *testing.T, jsonLine string) {
	t.Helper()
	f, err := os.OpenFile(fd.eventsPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(jsonLine + "\n"); err != nil {
		t.Fatal(err)
	}
}

func (fd *fakeDaemon) replaceEvents(t *testing.T, jsonLines ...string) {
	t.Helper()
	content := ""
	for _, line := range jsonLines {
		content += line + "\n"
	}
	if err := os.WriteFile(fd.eventsPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func buildFakeDaemon(t *testing.T) string {
	t.Helper()
	root := connTestRepoRoot(t)
	bin := filepath.Join(shortSocketDir(t), "fakedaemon")
	cmd := exec.Command("go", "build", "-o", bin, "./go/tui/testdata/fakedaemon")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build fakedaemon: %v\n%s", err, out)
	}
	return bin
}

// connTestRepoRoot finds the module root by walking up for go.mod (mirrors
// pty_integration_test.go's repoRoot, duplicated here because that one lives
// in the external tui_test package).
func connTestRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repo root not found")
		}
		dir = parent
	}
}

// TestConnectDeliversSessionReadyAndEvents proves the happy path actually
// works end to end against a real daemon-like process: Connect dials,
// creates a session, subscribes to the event stream, and forwards a
// published event as an EventMsg.
func TestConnectDeliversSessionReadyAndEvents(t *testing.T) {
	bin := buildFakeDaemon(t)
	dir := shortSocketDir(t)
	fd := startFakeDaemon(t, bin, dir)
	defer fd.kill(t)

	fs := &fakeSender{}
	Connect(fs, fd.sock, "", t.TempDir())

	msg := fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		_, ok := m.(SessionReadyMsg)
		return ok
	})
	ready := msg.(SessionReadyMsg)
	if ready.SessionID != "sess_1" {
		t.Fatalf("session id = %q, want sess_1", ready.SessionID)
	}

	fd.publish(t, `{"type":"task.completed","task_id":"t1"}`)

	evMsg := fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["task_id"] == "t1"
	})
	ev := evMsg.(EventMsg)
	if ev.Raw["type"] != "task.completed" {
		t.Fatalf("event type = %v, want task.completed", ev.Raw["type"])
	}
}

func TestConnectFlushesDurableTerminalResultWithoutTransientEnvelope(t *testing.T) {
	bin := buildFakeDaemon(t)
	dir := shortSocketDir(t)
	fd := startFakeDaemon(t, bin, dir)
	defer fd.kill(t)

	fs := &fakeSender{}
	Connect(fs, fd.sock, "sess_1", t.TempDir())
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		_, ok := m.(SessionReadyMsg)
		return ok
	})

	fd.publish(t, `{"type":"TaskCompleted","task_id":"durable-1","actor":"go","payload":{"status":"completed","summary":"durable result"}}`)
	msg := fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["type"] == "task.completed" && ev.Raw["task_id"] == "durable-1"
	})
	ev := msg.(EventMsg).Raw
	if ev["summary"] != "durable result" || ev["status"] != "completed" {
		t.Fatalf("synthesized completion = %#v", ev)
	}
}

// TestConnectLoadsInitialHistory proves resumed sessions render durable audit
// history before relying on the live tail.
func TestConnectLoadsInitialHistory(t *testing.T) {
	bin := buildFakeDaemon(t)
	dir := shortSocketDir(t)
	fd := startFakeDaemon(t, bin, dir)
	defer fd.kill(t)

	fd.publish(t, `{"type":"TaskCreated","task_id":"history-1","actor":"go","payload":{"status":"queued"}}`)

	fs := &fakeSender{}
	Connect(fs, fd.sock, "sess_1", t.TempDir())
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["task_id"] == "history-1"
	})
}

// TestConnectReopensOnlyPendingPermission folds the durable request/resolution
// records before emitting an approval overlay. An unresolved decision is
// restored once; a resolved decision is never reopened.
func TestConnectReopensOnlyPendingPermission(t *testing.T) {
	bin := buildFakeDaemon(t)
	dir := shortSocketDir(t)
	fd := startFakeDaemon(t, bin, dir)
	defer fd.kill(t)

	fd.publish(t, `{"type":"ToolRequested","task_id":"approval-task","actor":"go","permission_decision_id":"dec-open","payload":{"status":"permission_requested","request":{"type":"permission.request","task_id":"approval-task","decision_id":"dec-open","capability":"PatchApply","resource":"patch-1"}}}`)
	fd.publish(t, `{"type":"ToolRequested","task_id":"approval-task","actor":"go","permission_decision_id":"dec-closed","payload":{"status":"permission_requested","request":{"type":"permission.request","task_id":"approval-task","decision_id":"dec-closed","capability":"CommandExec","resource":"make test"}}}`)
	fd.publish(t, `{"type":"TaskCreated","task_id":"approval-task","actor":"operator","permission_decision_id":"dec-closed","payload":{"status":"approval_resolved","decision_id":"dec-closed","granted":true}}`)

	fs := &fakeSender{}
	Connect(fs, fd.sock, "sess_1", t.TempDir())
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["type"] == "permission.request" && ev.Raw["decision_id"] == "dec-open"
	})
	time.Sleep(150 * time.Millisecond)
	if got := fs.countEvents(func(ev EventMsg) bool {
		return ev.Raw["type"] == "permission.request" && ev.Raw["decision_id"] == "dec-open"
	}); got != 1 {
		t.Fatalf("pending decision reopened %d times, want once", got)
	}
	if got := fs.countEvents(func(ev EventMsg) bool {
		return ev.Raw["type"] == "permission.request" && ev.Raw["decision_id"] == "dec-closed"
	}); got != 0 {
		t.Fatalf("resolved decision reopened %d times, want zero", got)
	}
}

// TestConnectDeduplicatesDurableLiveEvent proves a durable event mirrored on
// the live stream is rendered only from the cursor-authoritative attach.
func TestConnectDeduplicatesDurableLiveEvent(t *testing.T) {
	bin := buildFakeDaemon(t)
	dir := shortSocketDir(t)
	fd := startFakeDaemon(t, bin, dir)
	defer fd.kill(t)

	fs := &fakeSender{}
	Connect(fs, fd.sock, "sess_1", t.TempDir())
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		_, ok := m.(SessionReadyMsg)
		return ok
	})

	fd.publish(t, `{"type":"TaskCreated","task_id":"once-1","actor":"go","payload":{"status":"queued"}}`)
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["task_id"] == "once-1"
	})
	time.Sleep(150 * time.Millisecond)
	if got := fs.countEvents(func(ev EventMsg) bool { return ev.Raw["task_id"] == "once-1" }); got != 1 {
		t.Fatalf("durable event delivered %d times, want exactly once", got)
	}

	fd.publish(t, `{"type":"ToolRequested","task_id":"approval-live","actor":"go","permission_decision_id":"dec-live","payload":{"status":"permission_requested","request":{"type":"permission.request","task_id":"approval-live","decision_id":"dec-live","capability":"PatchApply","resource":"patch-live"}}}`)
	fd.publish(t, `{"type":"permission.request","task_id":"approval-live","decision_id":"dec-live","capability":"PatchApply","resource":"patch-live"}`)
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["type"] == "permission.request" && ev.Raw["decision_id"] == "dec-live"
	})
	time.Sleep(150 * time.Millisecond)
	if got := fs.countEvents(func(ev EventMsg) bool {
		return ev.Raw["type"] == "permission.request" && ev.Raw["decision_id"] == "dec-live"
	}); got != 1 {
		t.Fatalf("live permission delivered %d times, want exactly once", got)
	}
}

// TestConnectReconnectsAfterDaemonRestart drives the real reconnect state
// machine (not just backoff() arithmetic): the daemon process is killed out
// from under an established connection, Connect must report the loss,
// retry with visible attempt counts, and — once a new daemon process comes
// up on the same socket path — actually redial, restore the session, and
// resume delivering live events.
func TestConnectReconnectsAfterDaemonRestart(t *testing.T) {
	bin := buildFakeDaemon(t)
	dir := shortSocketDir(t)
	fd := startFakeDaemon(t, bin, dir)

	fs := &fakeSender{}
	Connect(fs, fd.sock, "", t.TempDir())

	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		_, ok := m.(SessionReadyMsg)
		return ok
	})

	// Kill the daemon process out from under the live event-stream
	// connection — a real crash/restart, not a simulated one.
	fd.kill(t)
	// This durable event lands while no stream exists. The reconnect attach
	// must recover it from the cursor rather than waiting for another live
	// notification.
	fd.publish(t, `{"type":"TaskCreated","task_id":"gap-1","actor":"go","payload":{"status":"completed","summary":"finished while disconnected"}}`)

	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		_, ok := m.(ConnLostMsg)
		return ok
	})
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		r, ok := m.(ReconnectingMsg)
		return ok && r.Attempt >= 1
	})

	// Bring the daemon back on the same socket path; Connect must actually
	// redial (not just report loss forever) and restore the session.
	fd2 := startFakeDaemon(t, bin, dir)
	defer fd2.kill(t)

	fs.waitFor(t, 10*time.Second, func(m tea.Msg) bool {
		_, ok := m.(ConnRestoredMsg)
		return ok
	})
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["task_id"] == "gap-1"
	})
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["task_id"] == "gap-1" && ev.Raw["type"] == "task.completed"
	})

	// The reconnected stream must deliver live events again.
	fd2.publish(t, `{"type":"task.completed","task_id":"t2"}`)

	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["task_id"] == "t2"
	})
}

// TestConnectRecoversFromCursorAhead exercises the daemon's compaction
// contract: session.attach clamps a cursor beyond the current log. The client
// must accept the lower checkpoint and continue delivering subsequent live
// durable events.
func TestConnectRecoversFromCursorAhead(t *testing.T) {
	bin := buildFakeDaemon(t)
	dir := shortSocketDir(t)
	fd := startFakeDaemon(t, bin, dir)

	fs := &fakeSender{}
	Connect(fs, fd.sock, "sess_1", t.TempDir())
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		_, ok := m.(SessionReadyMsg)
		return ok
	})
	for _, id := range []string{"old-1", "old-2", "old-3"} {
		fd.publish(t, fmt.Sprintf(`{"type":"TaskCreated","task_id":%q,"actor":"go","payload":{"status":"old"}}`, id))
	}
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["task_id"] == "old-3"
	})

	fd.kill(t)
	// Simulate compaction/reset: the client's cursor is 3 while the daemon's
	// replacement log contains one retained entry.
	fd.replaceEvents(t, `{"type":"TaskCreated","task_id":"retained","actor":"go","payload":{"status":"old"}}`)

	fd2 := startFakeDaemon(t, bin, dir)
	defer fd2.kill(t)
	fs.waitFor(t, 10*time.Second, func(m tea.Msg) bool {
		_, ok := m.(ConnRestoredMsg)
		return ok
	})

	fd2.publish(t, `{"type":"TaskCreated","task_id":"after-compact","actor":"go","payload":{"status":"new"}}`)
	fs.waitFor(t, 5*time.Second, func(m tea.Msg) bool {
		ev, ok := m.(EventMsg)
		return ok && ev.Raw["task_id"] == "after-compact"
	})
}
