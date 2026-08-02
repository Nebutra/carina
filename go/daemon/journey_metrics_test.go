package daemon

import (
	"testing"
	"time"

	"github.com/Nebutra/carina/go/kernel"
)

func TestJourneyMetricsExposePercentilesAndVerifiedEditRate(t *testing.T) {
	current := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	metrics := newJourneyMetrics(func() time.Time { return current })
	metrics.observeReady(false)
	current = current.Add(2 * time.Second)
	metrics.observeReady(true)
	metrics.accepted("run_1", "sess_1")
	current = current.Add(120 * time.Millisecond)
	metrics.observeEvent("assistant.message.delta", "run_1")
	current = current.Add(880 * time.Millisecond)
	metrics.observeEvent("PatchApplied", "run_1")
	metrics.observeEvent("ExecutionCompleted", "run_1")

	snapshot := metrics.snapshot()
	if got := snapshot["accepted_submissions"]; got != 1 {
		t.Fatalf("accepted_submissions = %v", got)
	}
	if got := snapshot["verified_edit_submissions"]; got != 1 {
		t.Fatalf("verified_edit_submissions = %v", got)
	}
	if got := snapshot["first_verified_edit_success_rate"]; got != float64(1) {
		t.Fatalf("success rate = %v", got)
	}
	ready := snapshot["time_to_ready"].(map[string]any)
	if ready["p50_ms"] != int64(2000) || ready["p95_ms"] != int64(2000) {
		t.Fatalf("ready percentiles = %+v", ready)
	}
	response := snapshot["time_to_first_response"].(map[string]any)
	if response["p95_ms"] != int64(120) {
		t.Fatalf("response percentiles = %+v", response)
	}
	edit := snapshot["time_to_first_verified_edit"].(map[string]any)
	if edit["p95_ms"] != int64(1000) {
		t.Fatalf("edit percentiles = %+v", edit)
	}
}

func TestJourneyMetricsDeduplicateDurableReplay(t *testing.T) {
	now := time.Now()
	metrics := newJourneyMetrics(func() time.Time { return now })
	metrics.accepted("run_1", "sess_1")
	metrics.accepted("run_1", "sess_1")
	metrics.observeEvent("ModelResponded", "run_1")
	metrics.observeEvent("ModelResponded", "run_1")
	metrics.observeEvent("PatchApplied", "run_1")
	metrics.observeEvent("PatchApplied", "run_1")

	snapshot := metrics.snapshot()
	if snapshot["accepted_submissions"] != 1 || snapshot["verified_edit_submissions"] != 1 {
		t.Fatalf("duplicate replay changed counters: %+v", snapshot)
	}
	if snapshot["time_to_first_response"].(map[string]any)["count"] != 1 {
		t.Fatalf("duplicate response changed samples: %+v", snapshot)
	}
}

func TestJourneyMetricsAssociateKernelPatchEventWithPatchTask(t *testing.T) {
	now := time.Now()
	metrics := newJourneyMetrics(func() time.Time { return now })
	metrics.accepted("run_1", "sess_1")
	d := &Daemon{events: NewBus(), journey: metrics}
	d.publishKernelPatchEvents(&kernel.Patch{
		SessionID: "sess_1",
		TaskID:    "run_1",
		AuditEvents: []kernel.PersistedEvent{{
			Cursor: 1,
			Event: map[string]any{
				"session_id": "sess_1",
				"type":       "PatchApplied",
			},
		}},
	})

	snapshot := metrics.snapshot()
	if snapshot["verified_edit_submissions"] != 1 {
		t.Fatalf("patch task fallback did not record verified edit: %+v", snapshot)
	}
}

func TestJourneyMetricsObserveFirstPublicAssistantDelta(t *testing.T) {
	now := time.Now()
	metrics := newJourneyMetrics(func() time.Time { return now })
	metrics.accepted("run_1", "sess_1")
	now = now.Add(45 * time.Millisecond)
	d := &Daemon{events: NewBus(), journey: metrics}
	d.publishAssistantStreamEvent("sess_1", "run_1", "assistant.message.delta", map[string]any{
		"generation": 1,
		"sequence":   1,
		"phase":      assistantPhaseFinalAnswer,
		"delta":      "ready",
	})

	response := metrics.snapshot()["time_to_first_response"].(map[string]any)
	if response["count"] != 1 || response["p95_ms"] != int64(45) {
		t.Fatalf("first public assistant delta metrics = %+v", response)
	}
}

func TestJourneyMetricsCarryFirstEditAcrossLaterSessionRun(t *testing.T) {
	now := time.Now()
	metrics := newJourneyMetrics(func() time.Time { return now })
	metrics.accepted("run_1", "sess_1")
	now = now.Add(time.Second)
	metrics.observeEvent("ExecutionCompleted", "run_1")
	metrics.accepted("run_2", "sess_1")
	now = now.Add(2 * time.Second)
	metrics.observeEvent("PatchApplied", "run_2")

	snapshot := metrics.snapshot()
	if snapshot["accepted_submissions"] != 1 || snapshot["verified_edit_submissions"] != 1 {
		t.Fatalf("session journey was counted per run: %+v", snapshot)
	}
	edit := snapshot["time_to_first_verified_edit"].(map[string]any)
	if edit["count"] != 1 || edit["p95_ms"] != int64(3000) {
		t.Fatalf("later-run first edit did not use first acceptance clock: %+v", edit)
	}
}
