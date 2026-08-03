package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/continuity"
	"github.com/Nebutra/carina/go/scheduler"
)

func TestTaskRetryClonesEnvelopeLinksNewRunAndIsIdempotent(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(session.SessionID, workspace, "safe-edit", nil)

	original := d.sched.SubmitWithGoalModelAgent(
		session.SessionID,
		session.WorkspaceID,
		"repair the failing lifecycle",
		"openai/gpt-5",
		"build",
		[]scheduler.SuccessCheck{{Kind: "file_exists", Path: "fixed.txt"}},
	)
	d.sched.SetLocale(original.RunID, "zh")
	d.sched.SetModelState(original.RunID, "openai/gpt-5", "openai/gpt-5")
	d.sched.SetReasoningEffortState(original.RunID, "high", "high")
	d.sched.SetTokenBudget(original.RunID, 4096)
	d.sched.SetMode(original.RunID, "background")
	d.sched.SetOutputSchema(original.RunID, json.RawMessage(`{"type":"object","required":["summary"]}`))
	original, err := d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.runs.saveChecked(original); err != nil {
		t.Fatal(err)
	}

	params := mustJSON(t, map[string]any{
		"run_id":               original.RunID,
		"client_submission_id": "retry_failure_cell_1",
	})
	firstAny, err := d.handleTaskRetry(params)
	if err != nil {
		t.Fatal(err)
	}
	secondAny, err := d.handleTaskRetry(params)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(*scheduler.ExecutionRun)
	second := secondAny.(*scheduler.ExecutionRun)
	if first.RunID == original.RunID || first.RunID != second.RunID {
		t.Fatalf("retry identity original=%s first=%s second=%s", original.RunID, first.RunID, second.RunID)
	}
	if first.RetryOfRunID != original.RunID {
		t.Fatalf("retry linkage = %q, want %q", first.RetryOfRunID, original.RunID)
	}
	if first.UserPrompt != original.UserPrompt || first.Agent != original.Agent || first.Locale != "zh" ||
		first.RequestedModel != "openai/gpt-5" || first.RequestedReasoningEffort != "high" ||
		first.TokenBudget != 4096 || first.Mode != "background" {
		t.Fatalf("retry envelope drifted: original=%+v retry=%+v", original, first)
	}
	if len(first.SuccessCriteria) != 1 || first.SuccessCriteria[0].Path != "fixed.txt" ||
		!strings.Contains(string(first.OutputSchema), `"summary"`) {
		t.Fatalf("retry goal/schema drifted: %+v schema=%s", first.SuccessCriteria, first.OutputSchema)
	}
	unchanged, ok := d.sched.Get(original.RunID)
	if !ok || unchanged.Status != "cancelled" || unchanged.RetryOfRunID != "" {
		t.Fatalf("retry mutated original run: %+v ok=%v", unchanged, ok)
	}
}

func TestTaskRetryRejectsUnknownActiveAndAutoRecoveringRuns(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(session.SessionID, workspace, "safe-edit", nil)

	call := func(runID, submissionID string) error {
		_, err := d.handleTaskRetry(mustJSON(t, map[string]any{
			"run_id": runID, "client_submission_id": submissionID,
		}))
		return err
	}
	if err := call("run_missing", "retry_missing"); err == nil || !strings.Contains(err.Error(), "unknown execution") {
		t.Fatalf("unknown retry error = %v", err)
	}
	active := d.sched.Submit(session.SessionID, session.WorkspaceID, "still running")
	if err := call(active.RunID, "retry_active"); err == nil || !strings.Contains(err.Error(), "not retryable") {
		t.Fatalf("active retry error = %v", err)
	}

	recovering := d.sched.Submit(session.SessionID, session.WorkspaceID, "resume checkpoint")
	record := continuity.InterruptionRecord{
		Kind:       continuity.InterruptionRuntimeLost,
		Actor:      "runtime",
		TaskID:     recovering.RunID,
		ObservedAt: time.Now().UTC(),
		Certainty:  continuity.CertaintyInferred,
	}
	decision := continuity.RecoveryDecision{
		Disposition:  continuity.RecoveryResumeCheckpoint,
		CheckpointID: "checkpoint_1",
	}
	recovering, err := d.sched.Interrupt(recovering.RunID, record, decision)
	if err != nil {
		t.Fatal(err)
	}
	if err := call(recovering.RunID, "retry_recovering"); err == nil || !strings.Contains(err.Error(), "automatic checkpoint recovery") {
		t.Fatalf("auto-recovery retry error = %v", err)
	}
	if err := call(recovering.RunID, "bad/key"); err == nil || !strings.Contains(err.Error(), "client_submission_id") {
		t.Fatalf("invalid id retry error = %v", err)
	}
}
