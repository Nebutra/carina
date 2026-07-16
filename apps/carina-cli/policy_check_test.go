package main

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui"
)

// TestPolicyCheckAbsentWhenNotConfigured pins the common open-source/local
// case: no PolicyDir at all must not add a policy row (never a false
// staleness warning for a deployment that never opted into an enterprise
// bundle), and must not affect the overall PASS tier.
func TestPolicyCheckAbsentWhenNotConfigured(t *testing.T) {
	_, present := policyCheck(map[string]any{"configured": false, "stale": false, "reason": ""})
	if present {
		t.Fatal("policyCheck should not render a row when policy.configured is false")
	}
}

// TestPolicyCheckPassWhenFresh pins the healthy case.
func TestPolicyCheckPassWhenFresh(t *testing.T) {
	chk, present := policyCheck(map[string]any{"configured": true, "stale": false, "reason": ""})
	if !present {
		t.Fatal("policyCheck should render a row when policy.configured is true")
	}
	if chk.state != "PASS" {
		t.Fatalf("state = %q, want PASS", chk.state)
	}
}

// TestPolicyCheckWarnWhenStale pins the P1.6 governance-gap fix: a stale
// on-disk policy bundle must render WARN with a non-empty remediation, and
// must escalate the whole doctor report to WARN tier / exit 6
// (OutcomeDegradedPartial) — never a silent PASS.
func TestPolicyCheckWarnWhenStale(t *testing.T) {
	chk, present := policyCheck(map[string]any{
		"configured": true, "stale": true,
		"reason": "policy bundle at /etc/carina/policy has changed on disk since the daemon last loaded it",
	})
	if !present {
		t.Fatal("policyCheck should render a row when policy.configured is true")
	}
	if chk.state != "WARN" {
		t.Fatalf("state = %q, want WARN", chk.state)
	}
	if chk.remediation == "" {
		t.Fatal("a stale policy bundle must carry a copy-paste remediation")
	}
	if !strings.Contains(chk.detail, "changed on disk") {
		t.Fatalf("detail = %q, want it to carry the daemon's staleness reason", chk.detail)
	}
}

// TestDoctorTierEscalatesToWarnOnStalePolicy proves the end-to-end wiring:
// doctorChecks (which policyCheck now feeds into) must cause doctorTier to
// report WARN and doctorOutcome to report OutcomeDegradedPartial (exit 6)
// for a report that is all-PASS except a stale policy bundle — this is
// exactly the false-PASS scenario the finding described: an operator
// tightens policy on disk, doctor must not report all-clear.
func TestDoctorTierEscalatesToWarnOnStalePolicy(t *testing.T) {
	report := map[string]any{
		"version":            "0.6.4",
		"disabled":           false,
		"kernel":             map[string]any{"ok": true},
		"state_dir_writable": map[string]any{"ok": true},
		"tools":              map[string]any{"available": true, "dir": "/zig/zig-out/bin"},
		"reasoner":           true,
		"auth":               map[string]any{"source": "env:ANTHROPIC_API_KEY"},
		"context_engine":     map[string]any{"ok": true},
		"lsp":                map[string]any{"servers": []any{}},
		"byok": map[string]any{
			"any_resolved": true,
			"providers":    []any{},
		},
		"policy": map[string]any{
			"configured": true,
			"stale":      true,
			"reason":     "policy bundle at /etc/carina/policy has changed on disk since the daemon last loaded it",
		},
	}
	if tier := doctorTier(report); tier != "WARN" {
		t.Fatalf("doctorTier = %q, want WARN for an otherwise-healthy report with a stale policy bundle", tier)
	}
	if outcome := doctorOutcome(report); outcome != tui.OutcomeDegradedPartial {
		t.Fatalf("doctorOutcome = %v, want OutcomeDegradedPartial", outcome)
	}
	out := renderDoctorReport(report, false)
	if !strings.Contains(out, "WARN") || !strings.Contains(out, "policy") {
		t.Fatalf("rendered report must surface the stale policy WARN:\n%s", out)
	}
}
