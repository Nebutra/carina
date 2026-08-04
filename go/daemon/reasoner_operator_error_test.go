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
