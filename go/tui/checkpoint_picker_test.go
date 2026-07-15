package tui

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

type blockingCheckpointRestoreCaller struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingCheckpointRestoreCaller) Call(method string, _ any, result any) error {
	if method != "session.checkpoint.restore" {
		return errors.New("unexpected RPC method: " + method)
	}
	close(c.started)
	<-c.release
	raw, _ := json.Marshal(map[string]any{"checkpoint_id": "tsk:2", "task_id": "tsk", "turn": 2})
	return json.Unmarshal(raw, result)
}

func TestCheckpointRestoreCannotCloseWhileRPCIsInFlight(t *testing.T) {
	caller := &blockingCheckpointRestoreCaller{started: make(chan struct{}), release: make(chan struct{})}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(SessionReadyMsg{SessionID: "sess_test", Call: caller})
	preview := checkpointPreview{Checkpoint: checkpointInfo{CheckpointID: "tsk:2", TaskID: "tsk", Turn: 2}}
	m.checkpointPicker = &checkpointPickerState{generation: 1, preview: &preview, confirmArmed: true}

	cmd := m.restoreCheckpoint(preview)
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-caller.started:
	case <-time.After(time.Second):
		t.Fatal("restore RPC did not start")
	}

	closeCmd, handled := m.checkpointPickerKey("esc")
	if !handled || closeCmd != nil || m.checkpointPicker == nil || !m.checkpointPicker.restoring {
		t.Fatalf("Esc escaped in-flight restore: handled=%v cmd=%v state=%#v", handled, closeCmd != nil, m.checkpointPicker)
	}
	if !strings.Contains(m.checkpointPicker.status, "wait for success or failure") {
		t.Fatalf("in-flight close feedback = %q", m.checkpointPicker.status)
	}

	close(caller.release)
	select {
	case msg := <-done:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("restore RPC did not complete")
	}
	if m.checkpointPicker == nil || m.checkpointPicker.restored == nil {
		t.Fatalf("confirmed restore result was lost after close attempt: %#v", m.checkpointPicker)
	}
}

func TestCheckpointRestoreFailureRetainsExactRetry(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"history.recent":             map[string]any{"entries": []string{}},
		"session.checkpoint.restore": errors.New("restore journal busy"),
	}}
	m, _ := newTestModel(caller)
	preview := checkpointPreview{Checkpoint: checkpointInfo{CheckpointID: "tsk:4", TaskID: "tsk", Turn: 4}}
	m.checkpointPicker = &checkpointPickerState{generation: 1, preview: &preview, confirmArmed: true}

	drain(m, m.restoreCheckpoint(preview))
	if m.checkpointPicker == nil || m.checkpointPicker.restoreError == "" || m.checkpointPicker.preview == nil {
		t.Fatalf("failed restore did not retain retry state: %#v", m.checkpointPicker)
	}
	caller.handler["session.checkpoint.restore"] = map[string]any{"checkpoint_id": "tsk:4", "task_id": "tsk", "turn": 4}
	retry, handled := m.checkpointPickerKey("r")
	if !handled || retry == nil {
		t.Fatal("restore failure did not expose one-key retry")
	}
	drain(m, retry)
	if m.checkpointPicker == nil || m.checkpointPicker.restored == nil || m.checkpointPicker.restored.CheckpointID != "tsk:4" {
		t.Fatalf("restore retry did not reach paused success: %#v", m.checkpointPicker)
	}
	if last := caller.last(); last.params["checkpoint_id"] != "tsk:4" || last.params["confirmed"] != true {
		t.Fatalf("restore retry changed target or confirmation: %+v", last)
	}
}

func TestCheckpointResumeFailureRemainsRetryable(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []string{}},
		"task.resume":    errors.New("scheduler unavailable"),
	}}
	m, _ := newTestModel(caller)
	restored := &checkpointRestoreResult{CheckpointID: "tsk:3", TaskID: "tsk", Turn: 3}
	m.pausedRestore = restored
	m.checkpointPicker = &checkpointPickerState{generation: 1, restored: restored}
	m.tasks.setTask("tsk", "paused")

	resume, handled := m.checkpointPickerKey("enter")
	if !handled || resume == nil {
		t.Fatal("success page did not expose explicit resume")
	}
	drain(m, resume)
	if m.checkpointPicker == nil || m.checkpointPicker.resumeError == "" || m.pausedRestore == nil {
		t.Fatalf("failed resume did not remain retryable: picker=%#v paused=%#v", m.checkpointPicker, m.pausedRestore)
	}
	caller.handler["task.resume"] = map[string]any{"task_id": "tsk", "status": "running"}
	retry, handled := m.checkpointPickerKey("r")
	if !handled || retry == nil {
		t.Fatal("resume failure did not expose one-key retry")
	}
	drain(m, retry)
	if m.checkpointPicker != nil || m.pausedRestore != nil || m.inFlightTaskID != "tsk" {
		t.Fatalf("resume retry did not reactivate task: picker=%#v paused=%#v active=%q", m.checkpointPicker, m.pausedRestore, m.inFlightTaskID)
	}
}

func TestResumeCommandReopensMostRecentPausedRestore(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []string{}},
		"task.resume":    map[string]any{"task_id": "tsk", "status": "running"},
	}}
	m, _ := newTestModel(caller)
	m.pausedRestore = &checkpointRestoreResult{CheckpointID: "tsk:5", TaskID: "tsk", Turn: 5}
	m.tasks.setTask("tsk", "paused")
	m.checkpointPicker = &checkpointPickerState{generation: 1, restored: m.pausedRestore}
	if _, handled := m.checkpointPickerKey("esc"); !handled || m.checkpointPicker != nil {
		t.Fatalf("completed restore page did not close normally: handled=%v picker=%#v", handled, m.checkpointPicker)
	}

	cmd := m.slashCommand("/resume")
	if cmd == nil || m.checkpointPicker == nil || !m.checkpointPicker.resuming {
		t.Fatalf("/resume did not reopen paused restore flow: cmd=%v picker=%#v", cmd != nil, m.checkpointPicker)
	}
	if closeCmd, handled := m.checkpointPickerKey("esc"); !handled || closeCmd != nil || m.checkpointPicker == nil {
		t.Fatalf("resume RPC allowed silent close: handled=%v cmd=%v picker=%#v", handled, closeCmd != nil, m.checkpointPicker)
	}
	drain(m, cmd)
	if m.checkpointPicker != nil || m.inFlightTaskID != "tsk" {
		t.Fatalf("/resume completion: picker=%#v active=%q", m.checkpointPicker, m.inFlightTaskID)
	}
}

func TestCheckpointSuccessViewStatesContextAndTranscriptSemantics(t *testing.T) {
	m, _ := newTestModel(nil)
	m.checkpointPicker = &checkpointPickerState{
		restored: &checkpointRestoreResult{CheckpointID: "tsk:7", TaskID: "tsk", Turn: 7},
	}
	view := m.checkpointPickerView()
	for _, want := range []string{
		"Model context has been rolled back.",
		"audit transcript remains visible",
		"was not physically trimmed",
		"will not resume automatically",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("success view missing %q:\n%s", want, view)
		}
	}
}

func TestResumeCommandAcceptsExplicitTaskIDAfterRestart(t *testing.T) {
	caller := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []string{}},
		"task.resume":    map[string]any{"task_id": "tsk_persisted", "status": "running"},
	}}
	m, _ := newTestModel(caller)
	if m.pausedRestore != nil {
		t.Fatal("test requires an empty in-memory restore target")
	}

	cmd := m.slashCommand("/resume tsk_persisted")
	if cmd == nil || m.checkpointPicker == nil || !m.checkpointPicker.resuming {
		t.Fatalf("explicit /resume did not start: cmd=%v picker=%#v", cmd != nil, m.checkpointPicker)
	}
	if view := m.checkpointPickerView(); !strings.Contains(view, "supplied explicitly") {
		t.Fatalf("explicit resume view did not explain daemon verification:\n%s", view)
	}
	drain(m, cmd)
	if m.inFlightTaskID != "tsk_persisted" || m.pausedRestore != nil {
		t.Fatalf("explicit resume result: active=%q paused=%#v", m.inFlightTaskID, m.pausedRestore)
	}
	if last := caller.last(); last.method != "task.resume" || last.params["task_id"] != "tsk_persisted" {
		t.Fatalf("explicit resume RPC = %+v", last)
	}
	if !validSlashCommand("/resume tsk_persisted") || validSlashCommand("/resume one two") {
		t.Fatal("/resume validation does not enforce its optional single task ID")
	}
}

func TestResumeCommandWithoutMemoryPointsToExplicitTaskID(t *testing.T) {
	m, _ := newTestModel(nil)
	if cmd := m.slashCommand("/resume"); cmd != nil {
		t.Fatal("missing resume target unexpectedly started an RPC")
	}
	if got := transcriptText(m); !strings.Contains(got, "use /resume <task_id> after restarting") {
		t.Fatalf("missing resume target did not explain restart recovery:\n%s", got)
	}
}

func TestCheckpointPreviewCallsNoPatchRestoreModelContextOnly(t *testing.T) {
	m, _ := newTestModel(nil)
	m.checkpointPicker = &checkpointPickerState{preview: &checkpointPreview{
		Checkpoint: checkpointInfo{CheckpointID: "tsk:8", TaskID: "tsk", Turn: 8},
	}}
	view := m.checkpointPickerView()
	if !strings.Contains(view, "none (model context only)") || strings.Contains(view, "conversation state only") {
		t.Fatalf("no-patch restore semantics are ambiguous:\n%s", view)
	}
}

func TestCheckpointPickerUsesSemanticBindingsAndLabels(t *testing.T) {
	m, err := NewChecked(Options{
		Theme:  theme.New(theme.Mono),
		Locale: "en",
		Keybindings: []KeyBindingOverride{
			{Context: KeyContextCheckpointPreview, Action: ActionCheckpointPreviewArm, Keys: []string{"a"}},
			{Context: KeyContextCheckpointPreview, Action: ActionCheckpointPreviewConfirm, Keys: []string{"c"}},
			{Context: KeyContextCheckpointPreview, Action: ActionCheckpointPreviewBack, Keys: []string{"b"}},
			{Context: KeyContextCheckpointPreview, Action: ActionCheckpointPreviewClose, Keys: []string{"x"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	preview := checkpointPreview{Checkpoint: checkpointInfo{CheckpointID: "tsk:9", TaskID: "tsk", Turn: 9}}
	m.checkpointPicker = &checkpointPickerState{preview: &preview}

	if _, handled := m.checkpointPickerKey("y"); !handled || m.checkpointPicker.confirmArmed {
		t.Fatal("hard-coded y armed a semantically rebound checkpoint restore")
	}
	if _, handled := m.checkpointPickerKey("a"); !handled || !m.checkpointPicker.confirmArmed {
		t.Fatal("semantic arm binding did not arm checkpoint restore")
	}
	view := m.checkpointPickerView()
	for _, want := range []string{"[a] arm", "[c] confirm", "[b] back", "[x] close"} {
		if !strings.Contains(view, want) {
			t.Fatalf("checkpoint footer missing rebound label %q:\n%s", want, view)
		}
	}
	if cmd, handled := m.checkpointPickerKey("c"); !handled || cmd == nil {
		t.Fatal("semantic confirm binding did not start checkpoint restore")
	}
}
