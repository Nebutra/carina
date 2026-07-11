package runtimecontract

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestToolCallEnvelopeValidationAndJSON(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	e := ToolCallEnvelope{CallID: "c1", SessionID: "s1", TaskID: "t1", Tool: "shell", Status: ToolCallRunning, Arguments: json.RawMessage(`{"cmd":"go test"}`), CreatedAt: now, UpdatedAt: now}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"status":"running"`) {
		t.Fatalf("json = %s", raw)
	}
	e.Status = ToolCallFailed
	if err := e.Validate(); err == nil {
		t.Fatal("failed call without error accepted")
	}
}

func TestToolCallTransitions(t *testing.T) {
	valid := [][2]ToolCallStatus{{ToolCallPending, ToolCallAwaitingApproval}, {ToolCallAwaitingApproval, ToolCallRunning}, {ToolCallRunning, ToolCallCompleted}, {ToolCallRunning, ToolCallDenied}}
	for _, pair := range valid {
		if err := ValidateTransition(pair[0], pair[1]); err != nil {
			t.Errorf("%s -> %s: %v", pair[0], pair[1], err)
		}
	}
	invalid := [][2]ToolCallStatus{{ToolCallCompleted, ToolCallRunning}, {ToolCallPending, ToolCallCompleted}, {ToolCallRunning, ToolCallAwaitingApproval}}
	for _, pair := range invalid {
		if err := ValidateTransition(pair[0], pair[1]); err == nil {
			t.Errorf("accepted %s -> %s", pair[0], pair[1])
		}
	}
}

func TestActionableErrorAndRetryMetadata(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	e := ErrorEnvelope{Code: "provider_busy", Category: ErrorUnavailable, Message: "provider unavailable", UserAction: "wait or choose another provider", CorrelationID: "corr-1", Retry: RetryAfter(1500*time.Millisecond, 2, 5, now)}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	if e.Retry.RetryAfterMs != 1500 || !e.Retry.RetryAt.Equal(now.Add(1500*time.Millisecond)) {
		t.Fatalf("retry = %+v", e.Retry)
	}
	e.Category = ErrorPermission
	if err := e.Validate(); err == nil {
		t.Fatal("retryable permission error accepted")
	}
	e.Retry = NoRetry()
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryValidation(t *testing.T) {
	when := time.Now()
	bad := []RetryMetadata{{Retryable: false, RetryAt: &when}, {Retryable: true, RetryAfterMs: -1}, {Retryable: true, Attempt: 4, MaxAttempts: 3}}
	for _, retry := range bad {
		if err := retry.Validate(); err == nil {
			t.Fatalf("accepted %+v", retry)
		}
	}
}
