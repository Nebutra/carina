package outcome

import "testing"

func TestExitCodeContract(t *testing.T) {
	want := map[Outcome]int{
		OutcomeOK: 0, OutcomeRuntimeError: 1, OutcomeUsage: 2,
		OutcomePolicyDenied: 3, OutcomeApprovalTimeout: 4,
		OutcomeDaemonUnreachable: 5, OutcomeDegradedPartial: 6,
		OutcomeUserDenied: 7,
	}
	for value, code := range want {
		if got := value.ExitCode(); got != code {
			t.Fatalf("Outcome(%d).ExitCode() = %d, want %d", value, got, code)
		}
	}
	if got := Outcome(99).ExitCode(); got != 1 {
		t.Fatalf("unknown outcome exit = %d, want 1", got)
	}
}
