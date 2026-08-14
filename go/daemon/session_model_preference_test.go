package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/scheduler"
)

type preferenceBlockingReasoner struct {
	started chan struct{}
	release chan struct{}
}

func (r *preferenceBlockingReasoner) Name() string { return "preference-blocking" }

func (r *preferenceBlockingReasoner) Think(ctx context.Context, _ string) (string, error) {
	select {
	case r.started <- struct{}{}:
	default:
	}
	select {
	case <-r.release:
		return `{"thought":"done","action":{"tool":"done","summary":"ok"}}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func TestSessionModelSetPublishesVersionedPreferenceSnapshot(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	sub := newFakeEventSub("model-preference")
	d.events.Subscribe(sess.SessionID, sub)

	result, err := d.handleSessionModelSet(mustJSON(t, map[string]any{
		"session_id":       sess.SessionID,
		"model":            "openai/gpt-5",
		"reasoning_effort": "high",
	}))
	if err != nil {
		t.Fatal(err)
	}
	preference := result.(map[string]any)
	if preference["next_model"] != "openai/gpt-5" || preference["next_reasoning_effort"] != "high" || preference["model_preference_revision"] != uint64(1) {
		t.Fatalf("model preference response = %#v", preference)
	}
	if len(sub.events) != 1 {
		t.Fatalf("preference events = %#v", sub.events)
	}
	event := sub.events[0]
	if event["type"] != "session.model.preference.changed" || event["session_id"] != sess.SessionID {
		t.Fatalf("preference event envelope = %#v", event)
	}
	payload, ok := event["payload"].(map[string]any)
	if !ok || payload["next_model"] != preference["next_model"] || payload["next_reasoning_effort"] != preference["next_reasoning_effort"] || payload["model_preference_revision"] != preference["model_preference_revision"] {
		t.Fatalf("preference event payload = %#v, response = %#v", event["payload"], preference)
	}

	get, err := d.handleSessionModelGet(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	if got := get.(map[string]any); got["model_preference_revision"] != preference["model_preference_revision"] {
		t.Fatalf("model preference get = %#v, want revision %#v", got, preference["model_preference_revision"])
	}

	if _, err := d.handleSessionModelSet(mustJSON(t, map[string]any{
		"session_id":                         sess.SessionID,
		"model":                              "openai/gpt-5",
		"reasoning_effort":                   "high",
		"expected_model_preference_revision": uint64(1),
	})); err != nil {
		t.Fatal(err)
	}
	if len(sub.events) != 1 {
		t.Fatalf("idempotent preference set published another event: %#v", sub.events)
	}
}

func TestSessionModelSetRejectsStaleRevisionWithoutMutationOrEvent(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.SetNextModelPreference(sess.SessionID, "openai/gpt-5", "high"); err != nil {
		t.Fatal(err)
	}
	sub := newFakeEventSub("stale-model-preference")
	d.events.Subscribe(sess.SessionID, sub)

	_, err = d.handleSessionModelSet(mustJSON(t, map[string]any{
		"session_id":                         sess.SessionID,
		"model":                              "openai/gpt-5.1",
		"reasoning_effort":                   "medium",
		"expected_model_preference_revision": uint64(0),
	}))
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32011 || rpcErr.Message != "model_preference_conflict" {
		t.Fatalf("stale preference error = %#v", err)
	}
	if len(sub.events) != 0 {
		t.Fatalf("stale preference published events: %#v", sub.events)
	}
	current, _ := d.store.Get(sess.SessionID)
	if current.NextModel != "openai/gpt-5" || current.NextReasoningEffort != "high" || current.ModelPreferenceRevision != 1 {
		t.Fatalf("stale preference mutated authoritative row: %+v", current)
	}
}

func TestTaskSubmitModelPreferenceRevisionFreezesAuthoritativeTuple(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	preference, err := d.store.SetNextModelPreference(sess.SessionID, "openai/gpt-5", "high")
	if err != nil {
		t.Fatal(err)
	}

	result, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id":                sess.SessionID,
		"prompt":                    "freeze the visible route",
		"model":                     preference.NextModel,
		"reasoning_effort":          preference.NextReasoningEffort,
		"model_preference_revision": preference.ModelPreferenceRevision,
	}))
	if err != nil {
		t.Fatal(err)
	}
	run := result.(*scheduler.ExecutionRun)
	if run.RequestedModel != "openai/gpt-5" || run.RequestedReasoningEffort != "high" {
		t.Fatalf("versioned submission route = %+v", run)
	}

	if _, err := d.store.SetNextModelPreference(sess.SessionID, "openai/gpt-5.1", "medium"); err != nil {
		t.Fatal(err)
	}
	before := len(d.sched.List())
	_, err = d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id":                sess.SessionID,
		"prompt":                    "do not create a mixed route",
		"model":                     preference.NextModel,
		"reasoning_effort":          preference.NextReasoningEffort,
		"model_preference_revision": preference.ModelPreferenceRevision,
	}))
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32011 {
		t.Fatalf("stale submission error = %#v", err)
	}
	if after := len(d.sched.List()); after != before {
		t.Fatalf("stale submission created a run: before=%d after=%d", before, after)
	}
}

func TestTaskRetryCurrentRevisionIsAtomicAndAmbiguousDeliverySafe(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	original := d.sched.SubmitWithGoalModelAgent(
		sess.SessionID,
		sess.WorkspaceID,
		"retry against one preference snapshot",
		"openai/gpt-5-original",
		"build",
		nil,
	)
	d.sched.SetModelState(original.RunID, "openai/gpt-5-original", "openai/gpt-5-original")
	d.sched.SetReasoningEffortState(original.RunID, "low", "low")
	original, err = d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	preference, err := d.store.SetNextModelPreference(sess.SessionID, "openai/gpt-5", "high")
	if err != nil {
		t.Fatal(err)
	}
	params := mustJSON(t, map[string]any{
		"run_id":                    original.RunID,
		"client_submission_id":      "retry_versioned_current",
		"routing":                   "current",
		"model_preference_revision": preference.ModelPreferenceRevision,
	})
	firstAny, err := d.handleTaskRetry(params)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(*scheduler.ExecutionRun)
	if first.RequestedModel != "openai/gpt-5" || first.RequestedReasoningEffort != "high" {
		t.Fatalf("versioned current retry route = %+v", first)
	}

	if _, err := d.store.SetNextModelPreference(sess.SessionID, "openai/gpt-5.1", "medium"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.SetStatus(sess.SessionID, "paused"); err != nil {
		t.Fatal(err)
	}
	d.disabledProviders["openai"] = true
	secondAny, err := d.handleTaskRetry(params)
	if err != nil {
		t.Fatalf("ambiguous-delivery retry after preference/session/provider changes: %v", err)
	}
	second := secondAny.(*scheduler.ExecutionRun)
	if second.RunID != first.RunID {
		t.Fatalf("ambiguous-delivery retry created another run: first=%s second=%s", first.RunID, second.RunID)
	}
	if _, err := d.store.SetStatus(sess.SessionID, "active"); err != nil {
		t.Fatal(err)
	}
	delete(d.disabledProviders, "openai")

	before := len(d.sched.List())
	_, err = d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id":                    original.RunID,
		"client_submission_id":      "retry_versioned_stale_new",
		"routing":                   "current",
		"model_preference_revision": preference.ModelPreferenceRevision,
	}))
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32011 {
		t.Fatalf("new stale retry error = %#v", err)
	}
	if after := len(d.sched.List()); after != before {
		t.Fatalf("new stale retry created a run: before=%d after=%d", before, after)
	}
}

func TestTaskSubmitIdempotencyPrecedesMutableSessionAndProviderValidation(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	preference, err := d.store.SetNextModelPreference(sess.SessionID, "openai/gpt-5", "high")
	if err != nil {
		t.Fatal(err)
	}
	params := mustJSON(t, map[string]any{
		"session_id":                sess.SessionID,
		"client_submission_id":      "start_ambiguous_delivery",
		"prompt":                    "return the first accepted run",
		"model":                     preference.NextModel,
		"reasoning_effort":          preference.NextReasoningEffort,
		"model_preference_revision": preference.ModelPreferenceRevision,
	})
	firstAny, err := d.handleTaskSubmit(params)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(*scheduler.ExecutionRun)

	if _, err := d.store.SetNextModelPreference(sess.SessionID, "openai/gpt-5.1", "medium"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.store.SetStatus(sess.SessionID, "paused"); err != nil {
		t.Fatal(err)
	}
	d.disabledProviders["openai"] = true

	secondAny, err := d.handleTaskSubmit(params)
	if err != nil {
		t.Fatalf("same submission after mutable runtime changes: %v", err)
	}
	second := secondAny.(*scheduler.ExecutionRun)
	if second.RunID != first.RunID {
		t.Fatalf("same submission created another run: first=%s second=%s", first.RunID, second.RunID)
	}
}

func TestTaskRetryOriginalRejectsCurrentPreferenceRevisionWithoutRun(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	original := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "retry the frozen route", "openai/gpt-5", "build", nil)
	d.sched.SetModelState(original.RunID, "openai/gpt-5", "openai/gpt-5")
	original, err = d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	preference, err := d.store.SetNextModelPreference(sess.SessionID, "openai/gpt-5.1", "medium")
	if err != nil {
		t.Fatal(err)
	}
	before := len(d.sched.List())

	_, err = d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id":                    original.RunID,
		"client_submission_id":      "retry_original_with_revision",
		"routing":                   "original",
		"model_preference_revision": preference.ModelPreferenceRevision,
	}))
	if err == nil || err.Error() != "model_preference_revision is only valid with routing=current" {
		t.Fatalf("original routing revision error = %v", err)
	}
	if after := len(d.sched.List()); after != before {
		t.Fatalf("invalid original retry created a run: before=%d after=%d", before, after)
	}
}

func TestTaskRetryLegacyCurrentIdempotencyPrecedesProviderValidation(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	original := d.sched.SubmitWithGoalModelAgent(sess.SessionID, sess.WorkspaceID, "retry the visible route", "openai/gpt-5-original", "build", nil)
	d.sched.SetModelState(original.RunID, "openai/gpt-5-original", "openai/gpt-5-original")
	original, err = d.sched.Cancel(original.RunID)
	if err != nil {
		t.Fatal(err)
	}
	params := mustJSON(t, map[string]any{
		"run_id":                   original.RunID,
		"client_submission_id":     "retry_legacy_provider_change",
		"routing":                  "current",
		"current_model":            "openai/gpt-5-visible",
		"current_reasoning_effort": "medium",
	})
	firstAny, err := d.handleTaskRetry(params)
	if err != nil {
		t.Fatal(err)
	}
	first := firstAny.(*scheduler.ExecutionRun)
	d.disabledProviders["openai"] = true

	secondAny, err := d.handleTaskRetry(params)
	if err != nil {
		t.Fatalf("same legacy current retry after provider change: %v", err)
	}
	second := secondAny.(*scheduler.ExecutionRun)
	if second.RunID != first.RunID {
		t.Fatalf("same legacy current retry created another run: first=%s second=%s", first.RunID, second.RunID)
	}
	before := len(d.sched.List())
	_, err = d.handleTaskRetry(mustJSON(t, map[string]any{
		"run_id":                   original.RunID,
		"client_submission_id":     "retry_legacy_provider_change_new",
		"routing":                  "current",
		"current_model":            "openai/gpt-5-visible",
		"current_reasoning_effort": "medium",
	}))
	if err == nil || !strings.Contains(err.Error(), `model provider "openai" is disabled`) {
		t.Fatalf("new retry bypassed provider validation: %v", err)
	}
	if after := len(d.sched.List()); after != before {
		t.Fatalf("invalid new retry created a run: before=%d after=%d", before, after)
	}
}

func TestSessionModelSetDoesNotWaitForActiveExecution(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	reasoner := &preferenceBlockingReasoner{started: make(chan struct{}, 1), release: make(chan struct{})}
	d.SetReasoner(reasoner)
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)

	if _, err := d.handleTaskSubmit(mustJSON(t, map[string]any{
		"session_id": sess.SessionID,
		"prompt":     "remain active while the next-turn preference changes",
	})); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reasoner.started:
	case <-time.After(5 * time.Second):
		close(reasoner.release)
		t.Fatal("active execution did not reach the reasoner")
	}

	result := make(chan error, 1)
	go func() {
		_, setErr := d.handleSessionModelSet(mustJSON(t, map[string]any{
			"session_id":       sess.SessionID,
			"model":            "openai/gpt-5",
			"reasoning_effort": "high",
		}))
		result <- setErr
	}()
	select {
	case err := <-result:
		if err != nil {
			close(reasoner.release)
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(reasoner.release)
		t.Fatal("next-turn model preference waited for the active run to finish")
	}
	close(reasoner.release)
}

func TestSessionModelPreferenceEventsAreOrderedLiveSnapshotsNotDurableReplay(t *testing.T) {
	d, workspace := newLoopDaemon(t)
	defer d.Close()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	d.kern.InitSessionWithPolicy(sess.SessionID, workspace, "safe-edit", nil)
	live := newFakeEventSub("preference-live-order")
	d.events.Subscribe(sess.SessionID, live)

	const updates = 12
	var wg sync.WaitGroup
	for i := 0; i < updates; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := d.handleSessionModelSet(mustJSON(t, map[string]any{
				"session_id":       sess.SessionID,
				"model":            fmt.Sprintf("openai/gpt-5-%02d", i),
				"reasoning_effort": "high",
			})); err != nil {
				t.Errorf("preference update %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if len(live.events) != updates {
		t.Fatalf("live preference events = %d, want %d", len(live.events), updates)
	}
	for i, event := range live.events {
		payload := event["payload"].(map[string]any)
		if got, want := payload["model_preference_revision"], uint64(i+1); got != want {
			t.Fatalf("live event %d revision = %#v, want %d", i, got, want)
		}
	}

	// Preference notifications intentionally live only on the in-memory bus.
	// A reconnect gets the authoritative tuple from session.model.get rather
	// than receiving an invented durable preference event from audit replay.
	reconnected := newFakeEventSub("preference-reconnected")
	d.events.Subscribe(sess.SessionID, reconnected)
	if len(reconnected.events) != 0 {
		t.Fatalf("new subscription replayed live-only preference events: %#v", reconnected.events)
	}
	raw, err := d.kern.ReadEvents(sess.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "session.model.preference.changed") {
		t.Fatalf("preference event was presented as durable audit history: %s", raw)
	}
	currentAny, err := d.handleSessionModelGet(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	if got := currentAny.(map[string]any)["model_preference_revision"]; got != uint64(updates) {
		t.Fatalf("reconnect preference revision = %#v, want %d", got, updates)
	}
}
