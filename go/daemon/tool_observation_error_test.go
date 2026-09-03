package daemon

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolObservationErrorUsesStableRecoveryEnvelope(t *testing.T) {
	tests := []struct {
		name      string
		outcome   toolExecutionOutcome
		status    ToolObservationStatus
		category  ToolObservationCategory
		retryable bool
		recovery  ToolRecoveryVerb
	}{
		{name: "revise range", outcome: toolFailed("error: start_line and line_count must be provided together", "invalid_read_range"), status: toolObservationFailed, category: toolCategoryInvalidRequest, retryable: true, recovery: toolRecoveryRevise},
		{name: "reread conflict", outcome: toolDenied("DENIED: edit target is outside the ranges you read", "edit_span_rejected"), status: toolObservationDenied, category: toolCategoryConflict, retryable: true, recovery: toolRecoveryReread},
		{name: "approval", outcome: toolDenied("requires approval (not granted): internal decision details", "approval_denied"), status: toolObservationDenied, category: toolCategoryPermission, retryable: true, recovery: toolRecoveryRequestApproval},
		{name: "timeout", outcome: toolTimedOut("approval timed out"), status: toolObservationTimedOut, category: toolCategoryTimeout, retryable: true, recovery: toolRecoveryRetry},
		{name: "cancelled", outcome: toolCancelled("operator cancelled", "operator_cancelled"), status: toolObservationCancelled, category: toolCategoryCancelled, recovery: toolRecoveryNone},
		{name: "fallback", outcome: toolFailed("error: internal stack detail", "future_private_category"), status: toolObservationFailed, category: toolCategoryInternal, recovery: toolRecoveryNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.outcome.observationError
			if err == nil || err.Status != test.status || err.Category != test.category || err.Retryable != test.retryable || err.Recovery != test.recovery {
				t.Fatalf("error = %+v", err)
			}
			var decoded ToolObservationError
			if json.Unmarshal([]byte(err.modelJSON()), &decoded) != nil || decoded != *err {
				t.Fatalf("model JSON = %s decoded=%+v", err.modelJSON(), decoded)
			}
		})
	}
}

func TestToolObservationErrorRedactsSensitiveAndHostDetails(t *testing.T) {
	outcome := toolDenied(`DENIED: stale write in /Users/alice/private/file.txt token=supersecret`, "write_provenance_denied")
	model := outcome.observationError.modelJSON()
	if strings.Contains(model, "/Users/alice") || strings.Contains(model, "supersecret") {
		t.Fatalf("model error leaked host or secret detail: %s", model)
	}
	var public ToolObservationError
	if json.Unmarshal([]byte(model), &public) != nil || !strings.Contains(public.Message, "<path>") || !strings.Contains(public.Message, "<redacted>") {
		t.Fatalf("model error omitted redaction markers: %s", model)
	}
	if outcome.display != `DENIED: stale write in /Users/alice/private/file.txt token=supersecret` {
		t.Fatalf("operator display changed: %q", outcome.display)
	}

	internal := toolFailed("governance error: audit database /private/state failed", "audit_persistence_error")
	if strings.Contains(internal.observationError.modelJSON(), "database") || strings.Contains(internal.observationError.modelJSON(), "/private") {
		t.Fatalf("internal detail leaked: %s", internal.observationError.modelJSON())
	}
}

func TestTranscriptProjectsStructuredErrorWithoutChangingStoredDisplay(t *testing.T) {
	outcome := toolFailed("error: start_line must be positive", "invalid_read_range")
	tr := newTranscript("test")
	tr.addTurn(Turn{Tool: "read", ActionBrief: "read sample.txt", Obs: Observation{
		Content: outcome.display,
		Error:   outcome.observationError,
	}})
	if tr.Turns[0].Obs.Content != outcome.display {
		t.Fatalf("stored display = %q", tr.Turns[0].Obs.Content)
	}
	rendered := tr.render()
	if strings.Contains(rendered, "observation: error:") || !strings.Contains(rendered, `"category":"invalid_request"`) || !strings.Contains(rendered, `"recovery":"revise"`) {
		t.Fatalf("rendered transcript = %s", rendered)
	}
	raw, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	var restored Transcript
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Turns[0].Obs.Error == nil || restored.Turns[0].Obs.Error.Category != toolCategoryInvalidRequest {
		t.Fatalf("checkpoint error did not round trip: %+v", restored.Turns[0].Obs)
	}
}

func TestCompletedOutcomeHasNoErrorEnvelope(t *testing.T) {
	if outcome := toolCompleted("ok"); outcome.observationError != nil {
		t.Fatalf("completed outcome error = %+v", outcome.observationError)
	}
	unknown := newToolObservationError("future_status", "future_category", "private detail")
	if unknown.Status != toolObservationFailed || unknown.Category != toolCategoryInternal || unknown.Recovery != toolRecoveryNone {
		t.Fatalf("unknown status fallback = %+v", unknown)
	}
}
