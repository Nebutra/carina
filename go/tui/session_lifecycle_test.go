package tui

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

func TestConnectionControllerSwitchClosesBoundStreamAndAdvancesGeneration(t *testing.T) {
	controller := NewConnectionController()
	sid, gen := controller.state("sess_old")
	if sid != "sess_old" || gen == 0 {
		t.Fatalf("initial=%s/%d", sid, gen)
	}
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	client := rpc.NewClient(clientConn, clientConn, clientConn)
	controller.bind(client)
	if err := controller.Switch("sess_new"); err != nil {
		t.Fatal(err)
	}
	_ = serverConn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1)
	if _, err := serverConn.Read(buf); err == nil {
		t.Fatal("bound stream remained open")
	}
	sid2, gen2 := controller.state("sess_old")
	if sid2 != "sess_new" || gen2 <= gen {
		t.Fatalf("switched=%s/%d old=%d", sid2, gen2, gen)
	}
}

func TestNewSessionCreatesThenSwitches(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.create": map[string]any{"session_id": "sess_new", "status": "active"}}}
	m, _ := newTestModel(fc)
	m.workspaceRoot = t.TempDir()
	var switched string
	m.switchSession = func(id string) error { switched = id; return nil }
	cmd := m.slashCommand("/new")
	if cmd == nil {
		t.Fatal("/new returned no command")
	}
	m.Update(cmd())
	if switched != "sess_new" || m.pendingSessionID != "sess_new" {
		t.Fatalf("switch=%q pending=%q", switched, m.pendingSessionID)
	}
	if got := fc.last().method; got != "session.create" {
		t.Fatalf("method=%s", got)
	}
}

func TestComposerSessionCommandsDoNotBlockOnTheirOwnDraft(t *testing.T) {
	tests := []struct {
		command string
		method  string
		result  map[string]any
	}{
		{"/new", "session.create", map[string]any{"session_id": "sess_new", "status": "active"}},
		{"/fork", "session.fork", map[string]any{"session_id": "sess_fork", "status": "active"}},
	}
	for _, tc := range tests {
		t.Run(tc.command, func(t *testing.T) {
			fc := &fakeCaller{handler: map[string]any{tc.method: tc.result}}
			m, _ := newTestModel(fc)
			m.workspaceRoot = t.TempDir()
			m.switchSession = func(string) error { return nil }
			m.input.SetValue(tc.command)
			cmd := m.submit()
			if cmd == nil {
				t.Fatalf("%s was blocked by its composer draft: %s", tc.command, m.tr.plainText())
			}
			m.Update(cmd())
			if got := fc.last().method; got != tc.method {
				t.Fatalf("method=%q want %q", got, tc.method)
			}
			if m.input.Value() != "" {
				t.Fatalf("command draft was not consumed: %q", m.input.Value())
			}
		})
	}
}

func TestResumePickerListsHistoricalSessionsAndResumesSelection(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"session.list":   []map[string]any{{"session_id": "sess_test", "status": "active"}, {"session_id": "sess_old", "status": "paused", "parent_id": "sess_parent"}},
		"session.resume": map[string]any{"session_id": "sess_old", "status": "active"},
	}}
	m, _ := newTestModel(fc)
	var switched string
	m.switchSession = func(id string) error { switched = id; return nil }
	drain(m, m.slashCommand("/resume"))
	if m.sessionPicker == nil || len(m.sessionPicker.items) != 1 {
		t.Fatalf("picker=%#v", m.sessionPicker)
	}
	cmd, handled := m.sessionPickerKey("enter")
	if !handled || cmd == nil {
		t.Fatal("picker did not resume")
	}
	m.Update(cmd())
	if switched != "sess_old" {
		t.Fatalf("switched=%q", switched)
	}
}

func TestSessionSwitchRejectsDraftAndStaleEvents(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("unsent")
	if cmd := m.newSession(); cmd != nil {
		t.Fatal("switch allowed unsent draft")
	}
	if !strings.Contains(m.tr.plainText(), "current draft") {
		t.Fatalf("missing blocker: %s", m.tr.plainText())
	}
	m.input.SetValue("")
	m.sessionID = "sess_new"
	m.sessionGeneration = 2
	m.Update(EventMsg{SessionID: "sess_old", Generation: 1, Raw: map[string]any{"type": "ModelResponded", "payload": map[string]any{"text": "stale-marker"}}})
	if strings.Contains(m.tr.plainText(), "stale-marker") {
		t.Fatal("stale session event rendered")
	}
}

func TestSubmissionLeaseTransferKeepsOldLeaseOnFailure(t *testing.T) {
	dir := t.TempDir()
	old := newSubmissionJournal(dir, "/workspace")
	other := newSubmissionJournal(dir, "/workspace")
	if err := old.acquire("sess_old"); err != nil {
		t.Fatal(err)
	}
	defer old.close()
	if err := other.acquire("sess_new"); err != nil {
		t.Fatal(err)
	}
	defer other.close()
	if err := old.transfer("sess_new"); err == nil {
		t.Fatal("transfer unexpectedly acquired occupied destination")
	}
	if old.leaseSession != "sess_old" || old.lease == nil {
		t.Fatalf("old lease lost: session=%q", old.leaseSession)
	}
}

func TestForkUsesCurrentSessionLineageRPC(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.fork": map[string]any{"session_id": "sess_child", "parent_id": "sess_test", "status": "active"}}}
	m, _ := newTestModel(fc)
	var switched string
	m.switchSession = func(id string) error { switched = id; return nil }
	cmd := m.slashCommand("/fork task_done")
	if cmd == nil {
		t.Fatal("no fork command")
	}
	m.Update(cmd())
	last := fc.last()
	if last.method != "session.fork" || last.params["session_id"] != "sess_test" || last.params["last_task_id"] != "task_done" || switched != "sess_child" {
		t.Fatalf("call=%#v switched=%q", last, switched)
	}
}

func TestSessionPickerLocalizesChineseAndRetriesAfterErrorWithOldItems(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.list": []map[string]any{}}}
	m, _ := newTestModel(fc)
	m.locale = string(LocaleChinese)
	m.sessionPicker = &sessionPickerState{generation: 1, loadError: true, items: []sessionListItem{{SessionID: "sess_old", Status: "paused", ParentID: "sess_parent"}}, status: m.text(MsgSessionPickerFailed, MessageArgs{"error": "x"})}
	view := m.sessionPickerView()
	for _, want := range []string{"恢复会话", "分叉自", "按 r 重试"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker missing %q: %s", want, view)
		}
	}
	cmd, handled := m.sessionPickerKey("r")
	if !handled || cmd == nil || m.sessionPicker == nil || !m.sessionPicker.loading {
		t.Fatalf("error retry failed: handled=%v picker=%#v", handled, m.sessionPicker)
	}
}

func TestStaleTrackerFlushAndConnectionMessagesCannotAffectNewSession(t *testing.T) {
	tracker := newCompletionTracker()
	tracker.queue("task_old", map[string]any{"type": "task.completed", "task_id": "task_old", "status": "failed", "summary": "stale-marker"})
	sender := &fakeSender{}
	tracker.flush(sender, "sess_old", 1)
	m, _ := newTestModel(&fakeCaller{})
	m.sessionID = "sess_new"
	m.sessionGeneration = 2
	m.conn = ConnConnected
	for _, raw := range sender.snapshot() {
		m.Update(raw)
	}
	m.Update(ConnLostMsg{SessionID: "sess_old", Generation: 1, Err: errors.New("old lost")})
	m.Update(ReconnectingMsg{SessionID: "sess_old", Generation: 1, Attempt: 9})
	if strings.Contains(m.tr.plainText(), "stale-marker") || m.conn != ConnConnected {
		t.Fatalf("stale messages changed new session: conn=%v transcript=%s", m.conn, m.tr.plainText())
	}
}

func TestPendingSessionSwitchFreezesSubmissionAndPreservesDraft(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": map[string]any{"task_id": "wrong"}}}
	m, _ := newTestModel(fc)
	m.pendingSessionID = "sess_new"
	m.input.SetValue("keep this draft")
	if cmd := m.submit(); cmd != nil {
		t.Fatal("pending switch emitted submission command")
	}
	if m.input.Value() != "keep this draft" {
		t.Fatalf("draft changed: %q", m.input.Value())
	}
	for _, call := range fc.calls {
		if call.method == "task.submit" {
			t.Fatal("task submitted to old session")
		}
	}
}
