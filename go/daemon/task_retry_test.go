package daemon

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/artifact"
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
	mediaRef, err := ingestImageMedia(d.artifacts, artifact.Scope{SessionID: session.SessionID}, "retry input", tinyPNG())
	if err != nil {
		t.Fatal(err)
	}
	d.sched.SetInputMediaRefs(original.RunID, []scheduler.InputMediaRef{{
		ArtifactID: mediaRef.ArtifactID,
		MediaType:  mediaRef.MediaType,
		Bytes:      mediaRef.Bytes,
		Origin:     mediaRef.Origin,
	}})
	original, err = d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.runs.saveChecked(original); err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.SetNextModelPreference(session.SessionID, "openai/gpt-5-current", "low"); err != nil {
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
	if len(first.InputMediaRefs) != 1 || first.InputMediaRefs[0].ArtifactID != mediaRef.ArtifactID {
		t.Fatalf("retry media drifted: %+v", first.InputMediaRefs)
	}
	unchanged, ok := d.sched.Get(original.RunID)
	if !ok || unchanged.Status != "cancelled" || unchanged.RetryOfRunID != "" {
		t.Fatalf("retry mutated original run: %+v ok=%v", unchanged, ok)
	}
	payload := executionQueuedPayloadForRun(t, d, session.SessionID, first.RunID)
	if payload["retry_routing"] != "original" || payload["retry_model_source"] != "original" ||
		payload["retry_reasoning_effort_source"] != "original" || payload["retry_original_model"] != "openai/gpt-5" ||
		payload["retry_original_reasoning_effort"] != "high" {
		t.Fatalf("original retry audit evidence = %#v", payload)
	}

	explicitAny, err := d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id": original.RunID, "client_submission_id": "retry_failure_cell_explicit", "routing": "original",
	}))
	if err != nil {
		t.Fatal(err)
	}
	explicit := explicitAny.(*scheduler.ExecutionRun)
	if explicit.RequestedModel != "openai/gpt-5" || explicit.RequestedReasoningEffort != "high" {
		t.Fatalf("explicit original retry used current route: %+v", explicit)
	}
}

func TestTaskRetryCurrentRoutingUsesSessionPreferenceAndIsIdempotent(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(session.SessionID, workspace, "safe-edit", nil)

	original := d.sched.SubmitWithGoalModelAgent(session.SessionID, session.WorkspaceID, "retry with what I selected", "openai/gpt-5", "build", nil)
	d.sched.SetModelState(original.RunID, "openai/gpt-5", "openai/gpt-5")
	d.sched.SetReasoningEffortState(original.RunID, "high", "high")
	original, err := d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.SetNextModelPreference(session.SessionID, "openai/gpt-5-current", "low"); err != nil {
		t.Fatal(err)
	}
	params := mustJSON(t, map[string]any{
		"run_id": original.RunID, "client_submission_id": "retry_current_1", "routing": "current",
	})
	firstAny, err := d.handleTaskRetry(params)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(*scheduler.ExecutionRun)
	if first.RequestedModel != "openai/gpt-5-current" || first.RequestedReasoningEffort != "low" {
		t.Fatalf("current retry route = %+v", first)
	}
	if first.RetryOfRunID != original.RunID || first.UserPrompt != original.UserPrompt || first.Agent != original.Agent {
		t.Fatalf("current retry envelope/linkage drifted: original=%+v retry=%+v", original, first)
	}

	// A legacy retry without an explicit request snapshot keeps idempotency tied
	// to the first frozen selection even if the session preference later changes.
	if _, err := d.store.SetNextModelPreference(session.SessionID, "openai/gpt-5-later", "medium"); err != nil {
		t.Fatal(err)
	}
	secondAny, err := d.handleTaskRetry(params)
	if err != nil {
		t.Fatal(err)
	}
	second := secondAny.(*scheduler.ExecutionRun)
	if second.RunID != first.RunID || second.RequestedModel != "openai/gpt-5-current" || second.RequestedReasoningEffort != "low" {
		t.Fatalf("current retry idempotency drifted: first=%+v second=%+v", first, second)
	}
	if _, err := d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id": original.RunID, "client_submission_id": "retry_current_1", "routing": "original",
	})); err == nil || !strings.Contains(err.Error(), "already used for a different request") {
		t.Fatalf("same id accepted conflicting retry routing: %v", err)
	}
	payload := executionQueuedPayloadForRun(t, d, session.SessionID, first.RunID)
	if payload["retry_routing"] != "current" || payload["retry_model_source"] != "session_current" ||
		payload["retry_reasoning_effort_source"] != "session_current" || payload["requested_model"] != "openai/gpt-5-current" ||
		payload["requested_reasoning_effort"] != "low" {
		t.Fatalf("current retry audit evidence = %#v", payload)
	}
	unchanged, ok := d.sched.Get(original.RunID)
	if !ok || unchanged.Status != "cancelled" || unchanged.RetryOfRunID != "" {
		t.Fatalf("current retry mutated original: %+v ok=%v", unchanged, ok)
	}
}

func TestTaskRetryCurrentRoutingUsesRequestSnapshotBeforeSessionPreference(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(session.SessionID, workspace, "safe-edit", nil)

	original := d.sched.SubmitWithGoalModelAgent(session.SessionID, session.WorkspaceID, "retry with visible selection", "openai/gpt-5", "build", nil)
	d.sched.SetModelState(original.RunID, "openai/gpt-5", "openai/gpt-5")
	d.sched.SetReasoningEffortState(original.RunID, "high", "high")
	original, err := d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.SetNextModelPreference(session.SessionID, "openai/session-stale", "low"); err != nil {
		t.Fatal(err)
	}
	result, err := d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id": original.RunID, "client_submission_id": "retry_current_snapshot", "routing": "current",
		"current_model": "openai/gpt-5-visible", "current_reasoning_effort": "medium",
	}))
	if err != nil {
		t.Fatal(err)
	}
	retry := result.(*scheduler.ExecutionRun)
	if retry.RequestedModel != "openai/gpt-5-visible" || retry.RequestedReasoningEffort != "medium" {
		t.Fatalf("request snapshot did not own current retry: %+v", retry)
	}
	payload := executionQueuedPayloadForRun(t, d, session.SessionID, retry.RunID)
	if payload["retry_model_source"] != "request_current" ||
		payload["retry_reasoning_effort_source"] != "request_current" {
		t.Fatalf("request snapshot audit evidence = %#v", payload)
	}
	if _, err := d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id": original.RunID, "client_submission_id": "retry_current_snapshot", "routing": "current",
		"current_model": "openai/gpt-5-other", "current_reasoning_effort": "medium",
	})); err == nil || !strings.Contains(err.Error(), "already used for a different request") {
		t.Fatalf("changed request snapshot reused an incompatible retry: %v", err)
	}
}

func TestTaskRetryCurrentRoutingPreservesExplicitlyClearedEffort(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(session.SessionID, workspace, "safe-edit", nil)

	original := d.sched.SubmitWithGoalModelAgent(session.SessionID, session.WorkspaceID, "retry without effort", "openai/gpt-5", "build", nil)
	d.sched.SetModelState(original.RunID, "openai/gpt-5", "openai/gpt-5")
	d.sched.SetReasoningEffortState(original.RunID, "high", "high")
	original, err := d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.SetNextModelPreference(session.SessionID, "openai/session-stale", "high"); err != nil {
		t.Fatal(err)
	}
	result, err := d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id": original.RunID, "client_submission_id": "retry_current_no_effort", "routing": "current",
		"current_model": "openai/gpt-5-visible", "current_reasoning_effort": "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	retry := result.(*scheduler.ExecutionRun)
	if retry.RequestedReasoningEffort != "" || retry.EffectiveReasoningEffort != "" {
		t.Fatalf("explicit empty effort inherited stale session value: %+v", retry)
	}
	payload := executionQueuedPayloadForRun(t, d, session.SessionID, retry.RunID)
	if payload["retry_reasoning_effort_source"] != "request_current" || payload["requested_reasoning_effort"] != "" {
		t.Fatalf("cleared effort audit evidence = %#v", payload)
	}
}

func TestTaskRetryCurrentRoutingRejectsEmptyCurrentModel(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(session.SessionID, workspace, "safe-edit", nil)

	original := d.sched.SubmitWithGoalModelAgent(session.SessionID, session.WorkspaceID, "fallback safely", "openai/gpt-5", "build", nil)
	d.sched.SetModelState(original.RunID, "openai/gpt-5", "openai/gpt-5")
	d.sched.SetReasoningEffortState(original.RunID, "high", "high")
	original, err := d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	before := len(d.sched.List())
	_, err = d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id": original.RunID, "client_submission_id": "retry_current_fallback", "routing": "current",
	}))
	if err == nil || !strings.Contains(err.Error(), "current model is unavailable") {
		t.Fatalf("empty current model error = %v", err)
	}
	if after := len(d.sched.List()); after != before {
		t.Fatalf("empty current model created a task: before=%d after=%d", before, after)
	}
}

func TestTaskRetryRejectsUnknownRoutingWithoutCreatingTask(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(session.SessionID, workspace, "safe-edit", nil)
	original := d.sched.Submit(session.SessionID, session.WorkspaceID, "do not dispatch")
	original, err := d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	before := len(d.sched.List())
	if _, err := d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id": original.RunID, "client_submission_id": "retry_invalid_routing", "routing": "latest",
	})); err == nil || !strings.Contains(err.Error(), "invalid retry routing") {
		t.Fatalf("unknown retry routing error = %v", err)
	}
	if after := len(d.sched.List()); after != before {
		t.Fatalf("unknown routing created a task: before=%d after=%d", before, after)
	}
}

func TestTaskSubmitCannotForgeRetryAuditProvenance(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	session, _ := d.store.CreateSession(workspace, "safe-edit")
	d.kern.InitSessionWithPolicy(session.SessionID, workspace, "safe-edit", nil)
	result, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id":         session.SessionID,
		"prompt":             "ordinary start",
		"retry_of_run_id":    "run_forged",
		"retry_routing":      "current",
		"retry_model_source": "session_current",
	}))
	if err != nil {
		t.Fatal(err)
	}
	task := result.(*scheduler.ExecutionRun)
	if task.RetryOfRunID != "" {
		t.Fatalf("public start forged retry linkage: %+v", task)
	}
	payload := executionQueuedPayloadForRun(t, d, session.SessionID, task.RunID)
	for _, key := range []string{"retry_of_run_id", "retry_routing", "retry_model_source", "retry_reasoning_effort_source"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("public start forged %s in audit payload: %#v", key, payload)
		}
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

func executionQueuedPayloadForRun(t *testing.T, d *Daemon, sessionID, runID string) map[string]any {
	t.Helper()
	raw, err := d.kern.ReadEvents(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	var events []struct {
		TaskID  string         `json:"task_id"`
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "ExecutionQueued" && event.TaskID == runID {
			return event.Payload
		}
	}
	t.Fatalf("ExecutionQueued not found for %s: %s", runID, raw)
	return nil
}
