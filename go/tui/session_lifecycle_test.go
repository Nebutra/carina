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

func TestClearCreatesFreshSessionWithoutDeletingHistory(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.create": map[string]any{"session_id": "sess_clear", "status": "active", "workspace_root": "/repo"}}}
	m, _ := newTestModel(fc)
	m.workspaceRoot = "/repo"
	m.switchSession = func(string) error { return nil }
	drain(m, m.slashCommand("/clear"))
	if len(fc.calls) != 1 || fc.calls[0].method != "session.create" {
		t.Fatalf("clear calls=%#v", fc.calls)
	}
	if m.pendingSessionID != "sess_clear" {
		t.Fatalf("clear pending session=%q", m.pendingSessionID)
	}
}

func TestRenameCurrentSessionAndIgnoreStaleCompletion(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.rename": map[string]any{"session_id": "sess_test", "name": "Release review"}}}
	m, _ := newTestModel(fc)
	cmd := m.slashCommand("/rename Release review")
	if cmd == nil {
		t.Fatal("rename command missing")
	}
	msg := cmd()
	m.Update(msg)
	if last := fc.last(); last.method != "session.rename" || last.params["session_id"] != "sess_test" || last.params["name"] != "Release review" {
		t.Fatalf("rename call=%#v", last)
	}
	before := transcriptText(m)
	m.sessionID = "sess_other"
	m.Update(msg)
	if transcriptText(m) != before {
		t.Fatal("stale rename completion affected new session")
	}
}

func TestSessionPickerShowsNameWorkspaceAgeLocalizedStatusAndLineage(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.locale = string(LocaleChinese)
	m.width = 100
	m.sessionPicker = &sessionPickerState{items: []sessionListItem{{
		SessionID: "sess_child", Name: "发布检查", WorkspaceRoot: "/work/carina", Status: "paused",
		ParentID: "sess_parent", ForkedFromTaskID: "task_boundary", CreatedAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano),
	}}}
	view := m.sessionPickerView()
	for _, want := range []string{"发布检查", "carina", "已暂停", "2 小时前", "分叉自 sess_parent 于 task_boundary"} {
		if !strings.Contains(view, want) {
			t.Fatalf("picker missing %q: %s", want, view)
		}
	}
}

func TestSessionPickerConsumesRecoveryEvidence(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.width, m.height = 120, 30
	item := sessionListItem{SessionID: "sess_interrupted", Status: "active", TaskStatus: "interrupted", TaskRevision: 9}
	item.Continuity.Outcome = "interrupted"
	item.Continuity.Progress = "in_progress"
	item.Continuity.Recovery.Disposition = "review_required"
	item.Continuity.Recovery.Reason = "workspace anchor changed"
	item.Continuity.Recovery.CheckpointID = "task_1:3:7"
	item.Continuity.Recovery.Proofs = map[string]bool{"checkpoint": true, "workspace_anchor": false, "effect_replay": true, "external_effects": true}
	item.Continuity.RecoveryGeneration = 2
	item.Continuity.Interruption = &struct {
		Kind             string `json:"kind"`
		Certainty        string `json:"certainty"`
		BillingUncertain bool   `json:"billing_uncertain"`
	}{Kind: "runtime_lost", Certainty: "inferred", BillingUncertain: true}
	m.sessionPicker = &sessionPickerState{items: []sessionListItem{item}, status: m.text(MsgSessionPickerHelp, nil)}
	view := m.sessionPickerView()
	for _, want := range []string{"review_required", "runtime_lost", "billing uncertain", "+ checkpoint", "x workspace_anchor", "task_1:3:7", "task rev 9", "do not replay automatically"} {
		if !strings.Contains(view, want) {
			t.Fatalf("recovery evidence missing %q:\n%s", want, view)
		}
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

func TestResumePickerDefaultsToCurrentWorkspace(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"session.list": []map[string]any{
		{"session_id": "sess_current", "workspace_root": "/work/current", "status": "paused"},
		{"session_id": "sess_other", "workspace_root": "/work/other", "status": "paused"},
	}}}
	m, _ := newTestModel(fc)
	m.workspaceRoot = "/work/current"
	drain(m, m.openSessionPicker())
	if m.sessionPicker == nil || m.sessionPicker.scope != sessionScopeCurrent || len(m.sessionPicker.items) != 1 {
		t.Fatalf("current picker=%#v", m.sessionPicker)
	}
	if got := m.sessionPicker.items[0].SessionID; got != "sess_current" {
		t.Fatalf("current picker selected %q", got)
	}
}

func TestResumePickerTabBrowsesWorkspacesThenDestinationSessions(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.workspaceRoot = "/work/current"
	m.sessionPicker = &sessionPickerState{scope: sessionScopeCurrent, stage: sessionStageSessions}
	m.listWorkspaces = func() ([]WorkspaceListItem, error) {
		return []WorkspaceListItem{{Root: "/work/current", Current: true}, {Root: "/work/other", Name: "other"}}, nil
	}
	m.loadWorkspace = func(root string) (WorkspaceDestination, error) {
		if root != "/work/other" {
			t.Fatalf("loaded root=%q", root)
		}
		return WorkspaceDestination{
			Target:   ConnectionTarget{Socket: "/other.sock", WorkspaceRoot: root, StateDir: "/other-state"},
			Sessions: []SessionListItem{{SessionID: "sess_other", WorkspaceRoot: root, Status: "paused"}},
		}, nil
	}
	cmd, handled := m.sessionPickerKey("tab")
	if !handled || cmd == nil {
		t.Fatal("Tab did not open workspace browser")
	}
	drain(m, cmd)
	if m.sessionPicker.stage != sessionStageWorkspaces || len(m.sessionPicker.workspaces) != 2 {
		t.Fatalf("workspace stage=%#v", m.sessionPicker)
	}
	m.sessionPicker.selected = 1
	cmd, handled = m.sessionPickerKey("enter")
	if !handled || cmd == nil {
		t.Fatal("Enter did not load destination sessions")
	}
	drain(m, cmd)
	if m.sessionPicker.stage != sessionStageSessions || len(m.sessionPicker.items) != 1 || m.sessionPicker.items[0].SessionID != "sess_other" {
		t.Fatalf("destination sessions=%#v", m.sessionPicker)
	}
}

func TestWorkspaceResumeLeaseFailureLeavesSourceUntouched(t *testing.T) {
	destinationDir := t.TempDir()
	occupied := newSubmissionJournal(destinationDir, "/work/other")
	if err := occupied.acquire("sess_other"); err != nil {
		t.Fatal(err)
	}
	defer occupied.close()
	m, _ := newTestModel(&fakeCaller{})
	m.sessionID, m.workspaceRoot, m.stateDir = "sess_source", "/work/source", t.TempDir()
	m.sessionPicker = &sessionPickerState{
		scope: sessionScopeAll, stage: sessionStageSessions,
		destination: ConnectionTarget{Socket: "/other.sock", WorkspaceRoot: "/work/other", StateDir: destinationDir},
		items:       []sessionListItem{{SessionID: "sess_other", WorkspaceRoot: "/work/other", Status: "paused"}},
	}
	m.resumeWorkspace = func(target ConnectionTarget, sessionID string) (ConnectionTarget, error) {
		target.SessionID = sessionID
		return target, nil
	}
	prepared := false
	m.prepareTarget = func(ConnectionTarget) (uint64, error) { prepared = true; return 1, nil }
	m.commitTarget = func(uint64) error { return nil }
	cmd := m.resumeWorkspaceSession(m.sessionPicker.items[0])
	if cmd == nil {
		t.Fatal("workspace resume command missing")
	}
	drain(m, cmd)
	if prepared || m.pendingSessionID != "" || m.sessionID != "sess_source" || m.workspaceRoot != "/work/source" {
		t.Fatalf("source changed after lease failure: prepared=%v pending=%q session=%q root=%q", prepared, m.pendingSessionID, m.sessionID, m.workspaceRoot)
	}
}

func TestWorkspaceReadyAtomicallySwapsTargetAndJournalEvenWhenSessionIDsMatch(t *testing.T) {
	sourceDir, destinationDir := t.TempDir(), t.TempDir()
	m, _ := newTestModel(&fakeCaller{})
	m.sessionID, m.workspaceRoot, m.stateDir = "sess_same", "/work/source", sourceDir
	m.submissions = newSubmissionJournal(sourceDir, m.workspaceRoot)
	if err := m.submissions.acquire(m.sessionID); err != nil {
		t.Fatal(err)
	}
	destination := newSubmissionJournal(destinationDir, "/work/destination")
	if err := destination.acquire("sess_same"); err != nil {
		t.Fatal(err)
	}
	target := ConnectionTarget{Socket: "/destination.sock", SessionID: "sess_same", WorkspaceRoot: "/work/destination", StateDir: destinationDir}
	m.pendingTarget = &target
	m.pendingSubmissions = &destination
	m.pendingSessionID = target.SessionID
	m.pendingWorkspaceRoot = target.WorkspaceRoot
	m.Update(SessionReadyMsg{SessionID: "sess_same", Generation: 2, Target: target})
	if m.workspaceRoot != target.WorkspaceRoot || m.stateDir != destinationDir || m.socket != target.Socket || m.submissions.leaseSession != "sess_same" {
		t.Fatalf("target was not swapped: root=%q state=%q socket=%q lease=%q", m.workspaceRoot, m.stateDir, m.socket, m.submissions.leaseSession)
	}
	sourceContender := newSubmissionJournal(sourceDir, "/work/source")
	defer sourceContender.close()
	if err := sourceContender.acquire("sess_same"); err != nil {
		t.Fatalf("source lease was not released: %v", err)
	}
	destinationContender := newSubmissionJournal(destinationDir, "/work/destination")
	defer destinationContender.close()
	if err := destinationContender.acquire("sess_same"); err == nil {
		t.Fatal("destination lease was not retained")
	}
	m.Close()
}

func TestWorkspaceReadyRejectsSourceRuntimeWithCollidingSessionID(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	sourceCall := &fakeCaller{}
	m.call = sourceCall
	m.sessionID, m.workspaceRoot, m.stateDir = "sess_same", "/work/source", "/state/source"
	m.sessionGeneration = 5
	destination := ConnectionTarget{
		Socket: "/destination.sock", SessionID: "sess_same",
		WorkspaceRoot: "/work/destination", StateDir: "/state/destination",
	}
	m.pendingTarget = &destination
	m.pendingSubmissions = &submissionJournal{}
	m.pendingSessionID = destination.SessionID
	m.pendingWorkspaceRoot = destination.WorkspaceRoot

	source := ConnectionTarget{
		Socket: "/source.sock", SessionID: "sess_same",
		WorkspaceRoot: "/work/source", StateDir: "/state/source",
	}
	staleCall := &fakeCaller{}
	m.Update(SessionReadyMsg{SessionID: "sess_same", Generation: 5, Call: staleCall, Target: source})

	if m.call != sourceCall || m.workspaceRoot != "/work/source" || m.stateDir != "/state/source" {
		t.Fatalf("stale source readiness partially rebound model: call=%p root=%q state=%q", m.call, m.workspaceRoot, m.stateDir)
	}
	if m.pendingTarget == nil || m.pendingSubmissions == nil || m.pendingSessionID != "sess_same" {
		t.Fatalf("stale source readiness consumed destination transaction: target=%+v journal=%p session=%q", m.pendingTarget, m.pendingSubmissions, m.pendingSessionID)
	}
}

func TestPendingWorkspaceSwitchFencesCollidingConnectionLifecycle(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.sessionID = "sess_same"
	m.sessionGeneration = 5
	m.conn = ConnReconnecting
	m.pendingSessionID = "sess_same"
	m.sessionPicker = &sessionPickerState{loading: true}

	m.Update(ConnLostMsg{SessionID: "sess_same", Generation: 5, Err: errors.New("stale source loss")})
	if m.sessionPicker.loadError || !m.sessionPicker.loading {
		t.Fatalf("stale source loss polluted pending switch: picker=%+v", m.sessionPicker)
	}
	m.Update(ConnRestoredMsg{SessionID: "sess_same", Generation: 5})
	if m.conn != ConnReconnecting {
		t.Fatalf("stale source restore changed connection state: %v", m.conn)
	}

	m.Update(ConnLostMsg{SessionID: "sess_same", Generation: 6, Err: errors.New("destination loss")})
	if !m.sessionPicker.loadError || m.sessionPicker.loading || !strings.Contains(m.sessionPicker.status, "destination loss") {
		t.Fatalf("destination loss was not surfaced: picker=%+v", m.sessionPicker)
	}
}

func TestWorkspaceResumeCancelRejectsLatePreparedResult(t *testing.T) {
	destinationDir := t.TempDir()
	m, _ := newTestModel(&fakeCaller{})
	m.sessionID, m.workspaceRoot, m.stateDir = "sess_source", "/work/source", t.TempDir()
	m.sessionGeneration = 2
	m.sessionOpGen = 10
	m.sessionPicker = &sessionPickerState{
		generation: 10, loading: true, scope: sessionScopeAll, stage: sessionStageSessions,
	}
	aborted := uint64(0)
	m.abortTarget = func(token uint64) uint64 {
		aborted = token
		return 12
	}
	journal := newSubmissionJournal(destinationDir, "/work/destination")
	if err := journal.acquire("sess_target"); err != nil {
		t.Fatal(err)
	}
	_, handled := m.sessionPickerKey("esc")
	if !handled || m.sessionPicker == nil || m.sessionPicker.stage != sessionStageWorkspaces {
		t.Fatalf("cancel did not return to workspace list: picker=%#v", m.sessionPicker)
	}
	m.handleWorkspaceResume(workspaceResumeMsg{
		generation: 10,
		target:     ConnectionTarget{Socket: "/destination.sock", SessionID: "sess_target", WorkspaceRoot: "/work/destination", StateDir: destinationDir},
		journal:    journal,
		token:      7,
	})
	if aborted != 7 || m.pendingTarget != nil || m.pendingSessionID != "" {
		t.Fatalf("late result was not rejected: aborted=%d pending=%#v session=%q", aborted, m.pendingTarget, m.pendingSessionID)
	}
	if m.sessionGeneration != 12 {
		t.Fatalf("rollback generation=%d want=12", m.sessionGeneration)
	}
	contender := newSubmissionJournal(destinationDir, "/work/destination")
	defer contender.close()
	if err := contender.acquire("sess_target"); err != nil {
		t.Fatalf("late destination lease was not released: %v", err)
	}
}

func TestWorkspacePendingBackRestoresSourceAndReleasesDestination(t *testing.T) {
	sourceDir, destinationDir := t.TempDir(), t.TempDir()
	m, _ := newTestModel(&fakeCaller{})
	m.sessionID, m.workspaceRoot, m.stateDir = "sess_same", "/work/source", sourceDir
	m.sessionGeneration = 2
	m.sessionPicker = &sessionPickerState{loading: true}
	destination := newSubmissionJournal(destinationDir, "/work/destination")
	if err := destination.acquire("sess_same"); err != nil {
		t.Fatal(err)
	}
	target := ConnectionTarget{Socket: "/destination.sock", SessionID: "sess_same", WorkspaceRoot: "/work/destination", StateDir: destinationDir}
	m.pendingTarget = &target
	m.pendingSubmissions = &destination
	m.pendingPreparedToken = 9
	m.pendingSessionID = "sess_same"
	m.pendingWorkspaceRoot = "/work/destination"
	m.abortTarget = func(token uint64) uint64 {
		if token != 9 {
			t.Fatalf("abort token=%d want=9", token)
		}
		return 5
	}
	_, handled := m.sessionPickerKey("b")
	if !handled || m.pendingTarget != nil || m.pendingSubmissions != nil || m.pendingSessionID != "" {
		t.Fatalf("pending target survived rollback: target=%#v journal=%#v session=%q", m.pendingTarget, m.pendingSubmissions, m.pendingSessionID)
	}
	if m.sessionID != "sess_same" || m.workspaceRoot != "/work/source" || m.stateDir != sourceDir || m.sessionGeneration != 5 {
		t.Fatalf("source binding changed: session=%q root=%q state=%q generation=%d", m.sessionID, m.workspaceRoot, m.stateDir, m.sessionGeneration)
	}
	contender := newSubmissionJournal(destinationDir, "/work/destination")
	defer contender.close()
	if err := contender.acquire("sess_same"); err != nil {
		t.Fatalf("destination lease was not released: %v", err)
	}
}

func TestSessionSwitchRejectsDraftAndStaleEvents(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("unsent")
	if cmd := m.newSession(); cmd != nil {
		t.Fatal("switch allowed unsent draft")
	}
	if !strings.Contains(m.statusActivityText(), "current draft") {
		t.Fatalf("missing blocker: %s", m.statusActivityText())
	}
	if strings.Contains(m.tr.plainText(), "current draft") {
		t.Fatalf("session blocker polluted transcript: %s", m.tr.plainText())
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

func TestSubmissionLeaseTransferBackRestoresOldOwnership(t *testing.T) {
	dir := t.TempDir()
	owner := newSubmissionJournal(dir, "/workspace")
	defer owner.close()
	if err := owner.acquire("sess_old"); err != nil {
		t.Fatal(err)
	}
	if err := owner.transfer("sess_new"); err != nil {
		t.Fatal(err)
	}
	if err := owner.transfer("sess_old"); err != nil {
		t.Fatal(err)
	}
	contender := newSubmissionJournal(dir, "/workspace")
	defer contender.close()
	if err := contender.acquire("sess_new"); err != nil {
		t.Fatalf("rollback did not release target lease: %v", err)
	}
	blocked := newSubmissionJournal(dir, "/workspace")
	defer blocked.close()
	if err := blocked.acquire("sess_old"); err == nil {
		t.Fatal("rollback did not restore old-session lease ownership")
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

func TestSessionActionIsSingleFlightAndReadyAdoptsTargetWorkspace(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.workspaceRoot = "/workspace/old"
	m.treeCacheRoot = m.workspaceRoot
	m.treeCache = []treeEntry{{}}
	first := m.beginSessionAction("resume", "session.resume", map[string]any{"session_id": "sess_new"})
	if first == nil || m.sessionActionPending != "resume" {
		t.Fatal("first session action did not synchronously enter pending state")
	}
	if second := m.beginSessionAction("resume", "session.resume", map[string]any{"session_id": "sess_other"}); second != nil {
		t.Fatal("concurrent session action was not suppressed")
	}
	m.switchSession = func(string) error { return nil }
	m.handleSessionAction(sessionActionMsg{generation: m.sessionOpGen, action: "resume", session: sessionListItem{SessionID: "sess_new", WorkspaceRoot: "/workspace/new"}})
	if m.pendingSessionID != "sess_new" || m.pendingWorkspaceRoot != "/workspace/new" {
		t.Fatalf("pending target=%q workspace=%q", m.pendingSessionID, m.pendingWorkspaceRoot)
	}
	m.Update(SessionReadyMsg{SessionID: "sess_new", Generation: m.sessionGeneration + 1})
	if m.workspaceRoot != "/workspace/new" || m.treeCacheRoot != "" || m.treeCache != nil {
		t.Fatalf("ready did not adopt target workspace or clear tree cache: root=%q cacheRoot=%q cache=%#v", m.workspaceRoot, m.treeCacheRoot, m.treeCache)
	}
}
