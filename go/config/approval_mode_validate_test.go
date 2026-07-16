package config

import (
	"strings"
	"testing"
)

func TestApprovalModeValidation(t *testing.T) {
	cfg := Defaults("/tmp/home")
	cfg.ApprovalMode = "bogus"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected invalid approval_mode")
	}
	cfg.ApprovalMode = "dont-ask"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.ApprovalMode = "always-approve"
	cfg.DisableAlwaysApprove = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected conflict with disable_always_approve")
	}
	cfg.DisableAlwaysApprove = false
	cfg.ApprovalMode = "ask"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	// Session/kernel axis tokens must not be accepted as product approval_mode.
	for _, axis := range []string{"never", "untrusted", "on_request"} {
		cfg.ApprovalMode = axis
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("expected rejection for session-axis %q", axis)
		}
		if !strings.Contains(err.Error(), "session/kernel") {
			t.Fatalf("%q: want session/kernel in error, got %v", axis, err)
		}
	}
}
