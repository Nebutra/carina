package tui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func seedBacktrackPrompts(m *Model, pairs ...string) {
	for i := 0; i+1 < len(pairs); i += 2 {
		p := newUserPresentation(pairs[i], promptDraft{Text: pairs[i+1]}, false)
		localizePresentation(&p, newLocalizer(m.locale))
		m.tr.pushPresentation(p, m.th, m.transcriptWidth())
	}
	m.vp.SetContentLines(m.tr.lines)
}

func TestBacktrackMovesInlineAndPrintableInputReturnsToComposer(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	seedBacktrackPrompts(m, "task_1", "first", "task_2", "second", "task_3", "third")

	_, _ = m.handleKey("esc")
	_, _ = m.handleKey("esc")
	if m.backtrack.SelectedKey != "user:task_3" {
		t.Fatalf("initial selection = %q", m.backtrack.SelectedKey)
	}
	_, _ = m.handleKey("up")
	if m.backtrack.SelectedKey != "user:task_2" {
		t.Fatalf("older selection = %q", m.backtrack.SelectedKey)
	}
	_, _ = m.handleKey("right")
	if m.backtrack.SelectedKey != "user:task_3" {
		t.Fatalf("newer selection = %q", m.backtrack.SelectedKey)
	}

	m.Update(tea.KeyPressMsg{Text: "x", Code: 'x'})
	if m.backtrack.Phase != backtrackInactive || m.input.Value() != "x" {
		t.Fatalf("typing did not return to composer: phase=%v input=%q", m.backtrack.Phase, m.input.Value())
	}
}

func TestBacktrackBranchesFromPreviousTaskAndRestoresOnlyAfterReady(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"session.fork": map[string]any{"session_id": "sess_branch", "workspace_root": "/workspace", "status": "active"},
	}}
	m, _ := newTestModel(caller)
	m.workspaceRoot = "/workspace"
	var switched string
	m.switchSession = func(id string) error { switched = id; return nil }
	seedBacktrackPrompts(m, "task_1", "first", "task_2", "edit this")

	_, _ = m.handleKey("esc")
	_, _ = m.handleKey("esc")
	cmd, handled := m.handleKey("enter")
	if !handled || cmd == nil || m.backtrack.Phase != backtrackSwitching || m.input.Value() != "" {
		t.Fatalf("branch did not enter switching: handled=%v cmd=%v phase=%v input=%q", handled, cmd != nil, m.backtrack.Phase, m.input.Value())
	}
	m.Update(cmd())
	call := caller.last()
	if call.method != "session.fork" || call.params["session_id"] != "sess_test" || call.params["last_task_id"] != "task_1" || switched != "sess_branch" {
		t.Fatalf("branch call=%#v switched=%q", call, switched)
	}
	if m.input.Value() != "" || m.backtrack.DestinationSessionID != "sess_branch" {
		t.Fatalf("draft restored before ready: input=%q state=%#v", m.input.Value(), m.backtrack)
	}
	m.Update(SessionReadyMsg{SessionID: "sess_branch", Generation: m.sessionGeneration + 1, Call: caller})
	if m.input.Value() != "edit this" || m.backtrack.Phase != backtrackInactive {
		t.Fatalf("ready did not restore draft: input=%q state=%#v", m.input.Value(), m.backtrack)
	}
}

func TestBacktrackFirstPromptCreatesFreshSessionAndStripsReplayOnlyMedia(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"session.create": map[string]any{"session_id": "sess_fresh", "workspace_root": "/workspace", "status": "active"},
	}}
	m, _ := newTestModel(caller)
	m.workspaceRoot = "/workspace"
	m.switchSession = func(string) error { return nil }
	p := newUserPresentation("task_1", promptDraft{Text: "image prompt", Attachments: []draftAttachment{{
		ID: "replay", MediaType: "image/png", Ref: &mediaReference{ArtifactID: "artifact_1"},
	}}}, false)
	localizePresentation(&p, newLocalizer(m.locale))
	m.tr.pushPresentation(p, m.th, m.transcriptWidth())
	m.vp.SetContentLines(m.tr.lines)

	_, _ = m.handleKey("esc")
	_, _ = m.handleKey("esc")
	cmd, _ := m.handleKey("enter")
	m.Update(cmd())
	call := caller.last()
	if call.method != "session.create" || call.params["workspace_root"] != "/workspace" {
		t.Fatalf("first prompt branch call=%#v", call)
	}
	m.Update(SessionReadyMsg{SessionID: "sess_fresh", Generation: m.sessionGeneration + 1, Call: caller})
	if got := m.currentDraft(); got.Text != "image prompt" || len(got.Attachments) != 0 {
		t.Fatalf("replayed media was presented as restorable: %#v", got)
	}
}

func TestUserCellEditActionUsesBacktrackBranchReducer(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"session.fork": map[string]any{"session_id": "sess_edit", "workspace_root": "/workspace", "status": "active"},
	}}
	m, _ := newTestModel(caller)
	m.workspaceRoot = "/workspace"
	m.switchSession = func(string) error { return nil }
	seedBacktrackPrompts(m, "task_1", "first", "task_2", "edit from cell")

	cmd := m.applyTranscriptComponentAction(transcriptComponentAction{Name: "edit", Key: "user:task_2"})
	if cmd == nil || m.backtrack.Phase != backtrackSwitching || m.backtrack.PendingDraft.Text != "edit from cell" {
		t.Fatalf("edit action bypassed branch reducer: cmd=%v state=%#v", cmd != nil, m.backtrack)
	}
	m.Update(cmd())
	if call := caller.last(); call.method != "session.fork" || call.params["last_task_id"] != "task_1" {
		t.Fatalf("edit action call=%#v", call)
	}
}

func TestBacktrackBranchFailureRestoresSelectedPromptInSourceComposer(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{"session.fork": errors.New("fork failed")}}
	m, _ := newTestModel(caller)
	seedBacktrackPrompts(m, "task_1", "first", "task_2", "recover me")

	_, _ = m.handleKey("esc")
	_, _ = m.handleKey("esc")
	cmd, _ := m.handleKey("enter")
	if cmd == nil {
		t.Fatal("branch command missing")
	}
	m.Update(cmd())
	if m.sessionID != "sess_test" || m.input.Value() != "recover me" || m.backtrack.Phase != backtrackInactive {
		t.Fatalf("failed branch lost source prompt: session=%q input=%q state=%#v", m.sessionID, m.input.Value(), m.backtrack)
	}
}
