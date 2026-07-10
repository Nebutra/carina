package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/tui"
)

// TestClassifyExitCodeOK pins the trivial case: no error is OutcomeOK (exit
// 0), reusing go/tui's already-shipped Outcome enum rather than a second
// exit-code table for the CLI's one-shot path.
func TestClassifyExitCodeOK(t *testing.T) {
	if got := classifyExitCode(nil); got != tui.OutcomeOK {
		t.Fatalf("classifyExitCode(nil) = %v, want OutcomeOK", got)
	}
}

// TestClassifyExitCodeDaemonUnreachable pins P1.5(b)'s dial-failure
// mapping: an error wrapping rpc.ErrDaemonUnreachable (from a typed dial
// failure) must classify as OutcomeDaemonUnreachable(5), not the generic
// OutcomeRuntimeError(1) every error collapses to today.
func TestClassifyExitCodeDaemonUnreachable(t *testing.T) {
	err := fmt.Errorf("dial failed: %w", rpc.ErrDaemonUnreachable)
	got := classifyExitCode(err)
	if got != tui.OutcomeDaemonUnreachable {
		t.Fatalf("classifyExitCode(dial error) = %v (exit %d), want OutcomeDaemonUnreachable (exit %d)",
			got, got.ExitCode(), tui.OutcomeDaemonUnreachable.ExitCode())
	}
}

// TestClassifyExitCodePolicyDenied pins the policy-denial mapping: a
// terminal error carrying the daemon's own "DENIED by policy: ..." verdict
// text (as produced verbatim by agentRun/dispatchAction) must classify
// distinctly from a plain runtime error.
func TestClassifyExitCodePolicyDenied(t *testing.T) {
	err := errors.New("DENIED by policy: destructive command blocked by rule cmd.rm-rf")
	got := classifyExitCode(err)
	if got != tui.OutcomePolicyDenied {
		t.Fatalf("classifyExitCode(policy denial) = %v (exit %d), want OutcomePolicyDenied (exit %d)",
			got, got.ExitCode(), tui.OutcomePolicyDenied.ExitCode())
	}
}

// TestClassifyExitCodeUserDenied pins the operator-deny mapping: `carina
// deny` resolving a decision must classify distinctly from a policy
// verdict — "who said no" is itself a governance fact worth a distinct
// code (OutcomeUserDenied=7, distinct from OutcomePolicyDenied=3).
func TestClassifyExitCodeUserDenied(t *testing.T) {
	err := errors.New("denied by user: reason=not now")
	got := classifyExitCode(err)
	if got != tui.OutcomeUserDenied {
		t.Fatalf("classifyExitCode(user denial) = %v (exit %d), want OutcomeUserDenied (exit %d)",
			got, got.ExitCode(), tui.OutcomeUserDenied.ExitCode())
	}
}

// TestClassifyExitCodeGenericRuntimeError pins the fallback: an
// unclassifiable error still maps to OutcomeRuntimeError(1), matching
// today's behavior for the residual case.
func TestClassifyExitCodeGenericRuntimeError(t *testing.T) {
	err := errors.New("boom: unexpected condition")
	got := classifyExitCode(err)
	if got != tui.OutcomeRuntimeError {
		t.Fatalf("classifyExitCode(generic error) = %v, want OutcomeRuntimeError", got)
	}
}

// TestClassifyExitCodeDoctorDegraded pins carina doctor's WARN-tier mapping:
// a *doctorOutcomeError carrying OutcomeDegradedPartial must classify as
// exit 6, not collapse into the generic OutcomeRuntimeError(1) — a WARN
// (e.g. no BYOK key yet) is not the same governance fact as a hard FAIL.
func TestClassifyExitCodeDoctorDegraded(t *testing.T) {
	err := &doctorOutcomeError{outcome: tui.OutcomeDegradedPartial}
	got := classifyExitCode(err)
	if got != tui.OutcomeDegradedPartial {
		t.Fatalf("classifyExitCode(doctor WARN) = %v (exit %d), want OutcomeDegradedPartial (exit %d)",
			got, got.ExitCode(), tui.OutcomeDegradedPartial.ExitCode())
	}
}

// TestClassifyExitCodeDoctorFailed pins carina doctor's FAIL-tier mapping.
func TestClassifyExitCodeDoctorFailed(t *testing.T) {
	err := &doctorOutcomeError{outcome: tui.OutcomeRuntimeError}
	got := classifyExitCode(err)
	if got != tui.OutcomeRuntimeError {
		t.Fatalf("classifyExitCode(doctor FAIL) = %v (exit %d), want OutcomeRuntimeError (exit %d)",
			got, got.ExitCode(), tui.OutcomeRuntimeError.ExitCode())
	}
}

// TestClassifyExitCodeUsageError pins the exit-2 usage/runtime-error
// distinction the plan documents as a frozen compatibility contract
// (docs/plans/agent-cli-productization.md §6 item 8): every one of run()'s
// ~44 "usage: carina ..." errors must classify as OutcomeUsage (exit 2),
// not fall through to the generic OutcomeRuntimeError (exit 1) every other
// unclassified error maps to.
func TestClassifyExitCodeUsageError(t *testing.T) {
	err := errors.New("usage: carina watch <session_id> [--json]")
	got := classifyExitCode(err)
	if got != tui.OutcomeUsage {
		t.Fatalf("classifyExitCode(usage error) = %v (exit %d), want OutcomeUsage (exit %d)",
			got, got.ExitCode(), tui.OutcomeUsage.ExitCode())
	}
}

// TestClassifyExitCodeUsageErrorFromBacktickFormat pins the backtick-quoted
// usage strings (e.g. parseRunArgs's `usage: carina %s ...`) classify
// identically to the plain-quoted ones.
func TestClassifyExitCodeUsageErrorFromBacktickFormat(t *testing.T) {
	err := fmt.Errorf(`usage: carina %s [--agent name] [--model provider/model] "<prompt>" [--background]`, "run")
	got := classifyExitCode(err)
	if got != tui.OutcomeUsage {
		t.Fatalf("classifyExitCode(backtick usage error) = %v (exit %d), want OutcomeUsage (exit %d)",
			got, got.ExitCode(), tui.OutcomeUsage.ExitCode())
	}
}
