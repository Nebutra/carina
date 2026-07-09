package tui

import "testing"

// The governance exit-code table from the productization plan (P1.5). These
// values are a compatibility contract quoted in third-party CI configs;
// changing one is a breaking change, not a refactor.
func TestGovernanceExitCodeTable(t *testing.T) {
	want := map[Outcome]int{
		OutcomeOK:                0,
		OutcomeRuntimeError:      1,
		OutcomeUsage:             2,
		OutcomePolicyDenied:      3,
		OutcomeApprovalTimeout:   4,
		OutcomeDaemonUnreachable: 5,
		OutcomeDegradedPartial:   6,
		OutcomeUserDenied:        7,
	}
	for outcome, code := range want {
		if got := outcome.ExitCode(); got != code {
			t.Errorf("%v.ExitCode() = %d, want %d", outcome, got, code)
		}
	}
}
