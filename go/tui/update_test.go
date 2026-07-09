package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

// fakeCaller records RPC calls and answers them from a handler map.
type fakeCaller struct {
	calls   []fakeCall
	handler map[string]any // method -> result value (json round-tripped) or error
}

type fakeCall struct {
	method string
	params map[string]any
}

func (f *fakeCaller) Call(method string, params any, result any) error {
	raw, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	f.calls = append(f.calls, fakeCall{method: method, params: p})
	v, ok := f.handler[method]
	if !ok {
		return fmt.Errorf("fake: unhandled method %s", method)
	}
	if err, isErr := v.(error); isErr {
		return err
	}
	if result == nil || v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, result)
}

func (f *fakeCaller) last() fakeCall {
	if len(f.calls) == 0 {
		return fakeCall{}
	}
	return f.calls[len(f.calls)-1]
}

type testClock struct{ now time.Time }

func (c *testClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestModel(fc *fakeCaller) (*Model, *testClock) {
	clock := &testClock{now: time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)}
	m := New(Options{
		Theme:  theme.New(theme.Mono),
		Locale: "en",
		Socket: "/tmp/test-daemon.sock",
		Now:    func() time.Time { return clock.now },
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if fc != nil {
		m.Update(SessionReadyMsg{SessionID: "sess_test", Call: fc})
	}
	return m, clock
}

// drain executes a returned command synchronously and feeds any resulting
// message back into Update, returning the final message it saw.
func drain(m *Model, cmd tea.Cmd) tea.Msg {
	var last tea.Msg
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return last
		}
		last = msg
		if _, ok := msg.(tea.QuitMsg); ok {
			return last
		}
		_, cmd = m.Update(msg)
	}
	return last
}

func transcriptText(m *Model) string { return strings.Join(m.tr.lines, "\n") }

func permissionRequestEvent(decisionID string) EventMsg {
	return EventMsg{Raw: map[string]any{
		"type":        "permission.request",
		"session_id":  "sess_test",
		"task_id":     "tsk_1",
		"decision_id": decisionID,
		"capability":  "command.exec",
		"resource":    "mv a.txt b.txt",
		"reason":      "rule exec-ask matched",
		"label":       "mv a.txt b.txt",
		"timestamp":   "2026-07-09T10:00:00Z",
	}}
}

func TestPermissionRequestOpensApprovalOverlay(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(permissionRequestEvent("perm_1"))
	if m.approval == nil {
		t.Fatal("approval overlay not opened")
	}
	if m.approval.DecisionID != "perm_1" {
		t.Errorf("overlay decision_id = %q, want perm_1", m.approval.DecisionID)
	}
	// Transcript carries the Governed-register approval.required line.
	txt := transcriptText(m)
	for _, want := range []string{"Approval required", "command.exec", "perm_1"} {
		if !strings.Contains(txt, want) {
			t.Errorf("transcript missing %q:\n%s", want, txt)
		}
	}
	// The rendered overlay names the scope options and the artifact.
	body := m.overlayView()
	for _, want := range []string{"mv a.txt b.txt", "rule exec-ask matched", "once", "session", "project", "deny"} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay missing %q:\n%s", want, body)
		}
	}
}

// TestCtrlCHintVisibleBehindOpenApproval: Ctrl-C pressed while the approval
// overlay is open still pushes the "press ctrl+c again to exit" hint into
// the transcript, but View() replaces the entire frame with the overlay
// while one is open (view.go's lipgloss.Place branch) — the transcript
// region is not rendered behind it at all. The hint must be surfaced
// somewhere the operator can actually see while the overlay is up, or the
// cascading-interrupt cascade is invisible for as long as an approval is
// pending, which is exactly when an operator is most likely to want out.
func TestCtrlCHintVisibleBehindOpenApproval(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(permissionRequestEvent("perm_1"))
	if m.approval == nil {
		t.Fatal("approval overlay not opened")
	}

	cmd, handled := m.handleKey("ctrl+c")
	if !handled {
		t.Fatal("ctrl+c not handled while overlay is open")
	}
	drain(m, cmd)

	// The hint must be visible in the actual rendered frame — not just
	// buried in a transcript the overlay is currently covering.
	rendered := m.View().Content
	if !strings.Contains(rendered, "press ctrl+c again") {
		t.Errorf("rendered view while overlay is open does not show the ctrl+c hint:\n%s", rendered)
	}
	// The overlay must still be open — a lone ctrl+c must not dismiss or
	// resolve the pending decision.
	if m.approval == nil {
		t.Fatal("ctrl+c while overlay is open must not close the overlay")
	}
}

func TestApprovalAllowRoundTripsDecisionID(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.approve": map[string]any{
			"decision": map[string]any{"decision_id": "perm_1", "decision": "allowed"},
			"result":   map[string]any{"stdout": "ok"},
		},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_1"))

	cmd, handled := m.handleKey("y")
	if !handled {
		t.Fatal("'y' not handled while overlay open")
	}
	drain(m, cmd)

	last := fc.last()
	if last.method != "task.action.approve" {
		t.Fatalf("rpc method = %q, want task.action.approve", last.method)
	}
	if last.params["decision_id"] != "perm_1" {
		t.Errorf("decision_id roundtrip lost: params = %v", last.params)
	}
	if m.approval != nil {
		t.Error("overlay still open after approval")
	}
	txt := transcriptText(m)
	for _, want := range []string{"Approved:", "Scope: once", "perm_1"} {
		if !strings.Contains(txt, want) {
			t.Errorf("transcript missing %q:\n%s", want, txt)
		}
	}
	if m.Outcome() != OutcomeOK {
		t.Errorf("outcome = %v, want OK", m.Outcome())
	}
}

func TestApprovalDenySetsUserDeniedOutcome(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.deny": map[string]any{"decision_id": "perm_1", "decision": "denied"},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_1"))

	cmd, _ := m.handleKey("n")
	drain(m, cmd)

	last := fc.last()
	if last.method != "task.action.deny" {
		t.Fatalf("rpc method = %q, want task.action.deny", last.method)
	}
	if last.params["decision_id"] != "perm_1" {
		t.Errorf("decision_id roundtrip lost: params = %v", last.params)
	}
	txt := transcriptText(m)
	for _, want := range []string{"Denied:", "perm_1", "audit"} {
		if !strings.Contains(txt, want) {
			t.Errorf("transcript missing %q:\n%s", want, txt)
		}
	}
	if m.Outcome() != OutcomeUserDenied {
		t.Errorf("outcome = %v, want OutcomeUserDenied", m.Outcome())
	}
	if m.Outcome().ExitCode() != 7 {
		t.Errorf("exit code = %d, want 7", m.Outcome().ExitCode())
	}
}

// If the operator approves but the kernel still refuses, that is a policy
// denial, not a user denial — the exit code must distinguish them.
func TestApprovalRefusedByKernelSetsPolicyDenied(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.approve": map[string]any{
			"decision": map[string]any{"decision_id": "perm_1", "decision": "denied", "reason": "policy exec-deny"},
		},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_1"))
	cmd, _ := m.handleKey("1")
	drain(m, cmd)
	if m.Outcome() != OutcomePolicyDenied {
		t.Errorf("outcome = %v, want OutcomePolicyDenied", m.Outcome())
	}
	if m.Outcome().ExitCode() != 3 {
		t.Errorf("exit code = %d, want 3", m.Outcome().ExitCode())
	}
}

// A later allowed decision supersedes an earlier denial: the exit code
// reflects the most recent governance outcome.
func TestOutcomeTracksMostRecentGovernanceEvent(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.deny": map[string]any{"decision_id": "perm_1", "decision": "denied"},
		"task.action.approve": map[string]any{
			"decision": map[string]any{"decision_id": "perm_2", "decision": "allowed"},
		},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_1"))
	cmd, _ := m.handleKey("n")
	drain(m, cmd)
	m.Update(permissionRequestEvent("perm_2"))
	cmd, _ = m.handleKey("y")
	drain(m, cmd)
	if m.Outcome() != OutcomeOK {
		t.Errorf("outcome = %v, want OK after later approval", m.Outcome())
	}
}

// TestSecondPermissionRequestQueuesInsteadOfClobbering reproduces the bug
// where a second permission.request while one overlay is open silently
// replaced the first: the orphaned first decision was still pending
// server-side (a real task really blocked on it), but the operator could
// never see or resolve it — a keypress would only ever resolve the second
// decision_id.
func TestSecondPermissionRequestQueuesInsteadOfClobbering(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.action.approve": map[string]any{
			"decision": map[string]any{"decision_id": "perm_1", "decision": "allowed"},
		},
	}}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_1"))
	m.Update(permissionRequestEvent("perm_2"))

	if m.approval == nil || m.approval.DecisionID != "perm_1" {
		t.Fatalf("a second permission.request must not replace the open overlay; got %+v", m.approval)
	}

	// Resolving the first overlay must not touch perm_2's decision.
	cmd, handled := m.handleKey("y")
	if !handled {
		t.Fatal("'y' not handled while overlay open")
	}
	drain(m, cmd)

	last := fc.last()
	if last.params["decision_id"] != "perm_1" {
		t.Fatalf("resolved the wrong decision: %v", last.params)
	}

	// The queued second request must now surface as the open overlay.
	if m.approval == nil || m.approval.DecisionID != "perm_2" {
		t.Fatalf("queued permission.request did not surface after the first resolved; got %+v", m.approval)
	}
}

// TestEscDismissesOverlayThenSurfacesQueuedRequest: dismissing (esc) the open
// overlay must also advance to the next queued request, not just clear it.
func TestEscDismissesOverlayThenSurfacesQueuedRequest(t *testing.T) {
	fc := &fakeCaller{}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_1"))
	m.Update(permissionRequestEvent("perm_2"))

	cmd, _ := m.handleKey("esc")
	drain(m, cmd)

	if m.approval == nil || m.approval.DecisionID != "perm_2" {
		t.Fatalf("dismissing the first overlay must surface the queued one; got %+v", m.approval)
	}
}

func TestEscDismissesOverlayWithoutResolving(t *testing.T) {
	fc := &fakeCaller{}
	m, _ := newTestModel(fc)
	m.Update(permissionRequestEvent("perm_1"))
	cmd, _ := m.handleKey("esc")
	drain(m, cmd)
	if m.approval != nil {
		t.Error("overlay still open after esc")
	}
	if len(fc.calls) != 0 {
		t.Errorf("esc must not resolve the decision; rpc calls = %v", fc.calls)
	}
	if !strings.Contains(transcriptText(m), "perm_1") {
		t.Error("dismissal must remain observable in the transcript")
	}
}

func TestCtrlCCascade(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_9"},
		"task.cancel": map[string]any{"task_id": "tsk_9", "status": "cancelled"},
	}}
	m, clock := newTestModel(fc)

	// Submit a task so one is in flight.
	m.input.SetValue("do something")
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	if m.inFlightTaskID != "tsk_9" {
		t.Fatalf("inFlightTaskID = %q, want tsk_9", m.inFlightTaskID)
	}

	// First Ctrl-C: cancels the in-flight task over RPC and says so.
	cmd, handled := m.handleKey("ctrl+c")
	if !handled {
		t.Fatal("ctrl+c not handled")
	}
	msg := drain(m, cmd)
	if _, quit := msg.(tea.QuitMsg); quit {
		t.Fatal("first ctrl+c must not quit while a task is in flight")
	}
	if fc.last().method != "task.cancel" {
		t.Fatalf("rpc method = %q, want task.cancel", fc.last().method)
	}
	if fc.last().params["task_id"] != "tsk_9" {
		t.Errorf("task.cancel params = %v", fc.last().params)
	}
	txt := transcriptText(m)
	if !strings.Contains(txt, "Stopped by you") {
		t.Errorf("transcript missing interrupt line:\n%s", txt)
	}
	if !strings.Contains(txt, "ctrl+c") {
		t.Errorf("transcript missing exit hint:\n%s", txt)
	}

	// Second Ctrl-C within 2s exits.
	clock.advance(1 * time.Second)
	cmd, _ = m.handleKey("ctrl+c")
	if msg := cmd(); msg == nil {
		t.Fatal("second ctrl+c returned nil cmd/msg")
	} else if _, quit := msg.(tea.QuitMsg); !quit {
		t.Fatalf("second ctrl+c within 2s must quit, got %T", msg)
	}
}

func TestCtrlCWindowExpires(t *testing.T) {
	m, clock := newTestModel(&fakeCaller{})
	cmd, _ := m.handleKey("ctrl+c")
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("first ctrl+c must not quit")
		}
	}
	clock.advance(3 * time.Second) // window expired
	cmd, _ = m.handleKey("ctrl+c")
	if cmd != nil {
		if _, quit := cmd().(tea.QuitMsg); quit {
			t.Fatal("ctrl+c after window expiry must re-arm, not quit")
		}
	}
	clock.advance(1 * time.Second)
	cmd, _ = m.handleKey("ctrl+c")
	if cmd == nil {
		t.Fatal("armed ctrl+c returned nil cmd")
	}
	if _, quit := cmd().(tea.QuitMsg); !quit {
		t.Fatal("second ctrl+c within window must quit")
	}
}

// TestCtrlCDisarmedByInterveningActivity: the double-press cascade is a
// deliberate "press it again to really mean it" gesture. If something else
// happens between the two presses — the operator typed a character, or the
// daemon streamed an event — a Ctrl-C that then arrives within the stale 2s
// window is a fresh first press (cancel), not the second press of a
// cascade it was never part of. Unconditionally quitting here would exit
// the TUI on a Ctrl-C the operator did not intend as "confirm exit".
func TestCtrlCDisarmedByInterveningActivity(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_9"},
		"task.cancel": map[string]any{"task_id": "tsk_9", "status": "cancelled"},
	}}
	m, clock := newTestModel(fc)

	m.input.SetValue("do something")
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)

	// First Ctrl-C: cancels, arms the window.
	cmd, _ = m.handleKey("ctrl+c")
	if msg := drain(m, cmd); func() bool { _, q := msg.(tea.QuitMsg); return q }() {
		t.Fatal("first ctrl+c must not quit")
	}

	// Intervening activity within the window: an unrelated keystroke,
	// delivered the way the real program delivers it (through Update, not
	// handleKey directly — the disarm lives in Update).
	clock.advance(500 * time.Millisecond)
	m.Update(tea.KeyPressMsg{Text: "a", Code: 'a'})

	// A Ctrl-C still within the original 2s window must NOT be treated as
	// the second press of the earlier cascade — it must re-arm (there is no
	// task left in flight to cancel, so it just re-shows the hint), not quit.
	clock.advance(500 * time.Millisecond)
	cmd, _ = m.handleKey("ctrl+c")
	if cmd != nil {
		if msg := cmd(); func() bool { _, q := msg.(tea.QuitMsg); return q }() {
			t.Fatal("ctrl+c disarmed by intervening activity must not quit")
		}
	}
	if !strings.Contains(transcriptText(m), "press ctrl+c again within 2s to exit") {
		t.Error("disarmed ctrl+c must re-show the exit hint, not silently exit")
	}
}

func TestPasteCollapse(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_2"},
	}}
	m, _ := newTestModel(fc)

	// Multi-line paste (with \r endings, as tmux delivers them) collapses to
	// a visible notice and is held for the next submission.
	m.Update(tea.PasteMsg{Content: "line one\rline two\rline three"})
	if !strings.Contains(transcriptText(m), "[Pasted 3 lines]") {
		t.Errorf("transcript missing paste collapse notice:\n%s", transcriptText(m))
	}
	if m.input.Value() != "" {
		t.Errorf("multi-line paste leaked into input: %q", m.input.Value())
	}

	m.input.SetValue("apply this")
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	if fc.last().method != "task.submit" {
		t.Fatalf("rpc method = %q, want task.submit", fc.last().method)
	}
	prompt, _ := fc.last().params["prompt"].(string)
	for _, want := range []string{"apply this", "line one", "line three"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("submitted prompt missing %q: %q", want, prompt)
		}
	}

	// Single-line paste flows into the input box unchanged.
	m.Update(tea.PasteMsg{Content: "hello 中文"})
	if got := m.input.Value(); got != "hello 中文" {
		t.Errorf("single-line paste: input = %q, want %q", got, "hello 中文")
	}
}

func TestDegradeBannerLifecycle(t *testing.T) {
	m, _ := newTestModel(nil) // no session yet
	if m.banner() != "" {
		t.Errorf("banner before any failure = %q, want empty", m.banner())
	}
	m.Update(ConnLostMsg{Err: errors.New("dial unix: no such file")})
	b := m.banner()
	for _, want := range []string{"Daemon unreachable", "/tmp/test-daemon.sock", "carina-daemon"} {
		if !strings.Contains(b, want) {
			t.Errorf("banner missing %q: %q", want, b)
		}
	}
	m.Update(ReconnectingMsg{Attempt: 3})
	if b := m.banner(); !strings.Contains(b, "3") {
		t.Errorf("banner missing attempt count: %q", b)
	}
	// The banner is composited into the rendered frame — never a silent freeze.
	if v := m.View(); !strings.Contains(v.Content, "Daemon unreachable") {
		t.Error("degrade banner not visible in rendered view")
	}
	m.Update(ConnRestoredMsg{SessionID: "sess_test"})
	if m.banner() != "" {
		t.Errorf("banner after restore = %q, want empty", m.banner())
	}
	if !strings.Contains(transcriptText(m), "reconnected") {
		t.Error("reconnect must be observable in the transcript")
	}
}

func TestZhDegradeBanner(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "zh", Socket: "/tmp/s.sock", Now: func() time.Time { return clock.now }})
	m.Update(ConnLostMsg{Err: errors.New("x")})
	if b := m.banner(); !strings.Contains(b, "无法连接守护进程") {
		t.Errorf("zh banner = %q", b)
	}
}

// TestZhVariantDegradeBanner: main.go passes --locale through to Options.Locale
// without microcopy normalization (it only normalizes the DetectLocale
// fallback, not an explicit flag value), so the reconnect-attempt suffix
// must itself normalize the locale rather than doing a raw == "zh" compare
// — otherwise a real-world locale variant like "zh-CN" or "zh_TW.UTF-8"
// gets the (already-normalized, correctly zh) Degrade banner text but the
// English "(reconnecting, attempt N)" suffix bolted onto it.
func TestZhVariantDegradeBanner(t *testing.T) {
	for _, loc := range []string{"zh-CN", "zh_TW.UTF-8", "ZH_HK"} {
		t.Run(loc, func(t *testing.T) {
			clock := &testClock{now: time.Unix(0, 0)}
			m := New(Options{Theme: theme.New(theme.Mono), Locale: loc, Socket: "/tmp/s.sock", Now: func() time.Time { return clock.now }})
			m.Update(ReconnectingMsg{Attempt: 2})
			b := m.banner()
			if !strings.Contains(b, "无法连接守护进程") {
				t.Errorf("banner for locale %q = %q, want zh degrade text", loc, b)
			}
			if !strings.Contains(b, "正在重连") {
				t.Errorf("banner for locale %q = %q, want zh reconnect suffix, not the en fallback", loc, b)
			}
		})
	}
}

func TestOutcomeDaemonUnreachable(t *testing.T) {
	m, _ := newTestModel(nil)
	m.Update(ConnLostMsg{Err: errors.New("dial failed")})
	if m.Outcome() != OutcomeDaemonUnreachable {
		t.Errorf("outcome = %v, want OutcomeDaemonUnreachable", m.Outcome())
	}
	if m.Outcome().ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5", m.Outcome().ExitCode())
	}
	// Once a session exists the outcome is governed by session events again.
	m.Update(SessionReadyMsg{SessionID: "sess_1", Call: &fakeCaller{}})
	if m.Outcome() != OutcomeOK {
		t.Errorf("outcome after connect = %v, want OK", m.Outcome())
	}
}

// TestOutcomeDuringInitialConnectIsDaemonUnreachable: quitting before the
// very first dial ever resolves (no SessionReadyMsg, no ConnLostMsg yet —
// still ConnConnecting) must exit as daemon-unreachable, not OK. A quit at
// this point means no session was ever established; reporting exit 0 would
// tell an orchestrating script the run succeeded when nothing happened.
func TestOutcomeDuringInitialConnectIsDaemonUnreachable(t *testing.T) {
	m, _ := newTestModel(nil)
	if m.conn != ConnConnecting {
		t.Fatalf("precondition: conn = %v, want ConnConnecting", m.conn)
	}
	if m.Outcome() != OutcomeDaemonUnreachable {
		t.Errorf("outcome while still connecting = %v, want OutcomeDaemonUnreachable", m.Outcome())
	}
	if m.Outcome().ExitCode() != 5 {
		t.Errorf("exit code = %d, want 5", m.Outcome().ExitCode())
	}
}

func TestTaskCompletedClearsInFlight(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_5"},
	}}
	m, _ := newTestModel(fc)
	m.input.SetValue("run")
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	m.Update(EventMsg{Raw: map[string]any{
		"type": "task.completed", "task_id": "tsk_5", "status": "completed",
		"timestamp": "2026-07-09T10:00:01Z",
	}})
	if m.inFlightTaskID != "" {
		t.Errorf("inFlightTaskID = %q, want cleared", m.inFlightTaskID)
	}
}

// RPC failures on submit are rendered, never silently swallowed.
func TestSubmitFailureIsObservable(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": errors.New("scheduler rejected"),
	}}
	m, _ := newTestModel(fc)
	m.input.SetValue("run")
	cmd, _ := m.handleKey("enter")
	drain(m, cmd)
	if !strings.Contains(transcriptText(m), "scheduler rejected") {
		t.Errorf("submit failure not visible:\n%s", transcriptText(m))
	}
}

// View must not panic before the first WindowSizeMsg (bubbles textinput
// panics on negative width — the layout clamps).
func TestViewBeforeWindowSize(t *testing.T) {
	clock := &testClock{now: time.Unix(0, 0)}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en", Socket: "/tmp/s.sock", Now: func() time.Time { return clock.now }})
	_ = m.View() // must not panic
}
