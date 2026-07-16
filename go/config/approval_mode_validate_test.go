package config

import "testing"

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
	cfg.ApprovalMode = "ask"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
