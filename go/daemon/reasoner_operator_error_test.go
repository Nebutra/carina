package daemon

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestOperatorFacingReasonerErrorStripsInternalStacks(t *testing.T) {
	stream := providerStreamError{
		provider: "TDS",
		phase:    "body",
		err:      errors.New("context deadline exceeded (Client.Timeout or context cancellation while reading body)"),
	}
	wrapped := fmt.Errorf("reasoner failed: %w", fmt.Errorf("modelrouter: all providers failed: %w", stream))
	got := operatorFacingReasonerError(wrapped)
	if strings.Contains(got, "modelrouter") || strings.Contains(got, "reasoner failed") {
		t.Fatalf("internal stack leaked: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "stream") && !strings.Contains(strings.ToLower(got), "not auto-retried") {
		t.Fatalf("expected stream VOICE copy, got %q", got)
	}
	auth := operatorFacingReasonerError(providerCredentialError{provider: "openai"})
	if !strings.Contains(strings.ToLower(auth), "credential") {
		t.Fatalf("auth facing = %q", auth)
	}
}

func TestOperatorFacingReasonerErrorKeepsCLIDetail(t *testing.T) {
	got := operatorFacingReasonerError(grokCLIError{
		message: "authenticate: Grok Build emitted an unsafe settings update",
		kind:    "safety",
	})
	if !strings.Contains(got, "unsafe settings update") || !strings.Contains(got, "update Grok Build") {
		t.Fatalf("grok facing = %q", got)
	}
	claude := operatorFacingReasonerError(claudeCLIError{
		message: "Claude CLI emitted multiple assistant messages",
		kind:    "protocol",
	})
	if !strings.Contains(claude, "multiple assistant") || !strings.Contains(claude, "update Claude Code") {
		t.Fatalf("claude facing = %q", claude)
	}
}

func TestOperatorFacingReasonerErrorNamesUnavailable503(t *testing.T) {
	got := operatorFacingReasonerError(providerStatusError{provider: "Mox LAN", status: 503})
	if !strings.Contains(strings.ToLower(got), "temporarily unavailable") {
		t.Fatalf("503 facing = %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "retry") {
		t.Fatalf("503 missing action: %q", got)
	}
	info := classifyProviderError(providerStatusError{provider: "Mox LAN", status: 503})
	if info.Code != "provider_unavailable" || info.UserAction == "" || !info.Retryable {
		t.Fatalf("503 classification = %+v", info)
	}
	if routingFailureMessage(providerStatusError{provider: "Mox LAN", status: 503}, info) != "provider temporarily unavailable" {
		t.Fatalf("503 routing message = %q", routingFailureMessage(providerStatusError{provider: "Mox LAN", status: 503}, info))
	}
}
