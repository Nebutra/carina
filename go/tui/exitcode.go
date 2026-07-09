package tui

// Outcome classifies how a TUI session ended, following the governance
// exit-code table in docs/plans/agent-cli-productization.md §P1.5. The codes
// are a compatibility contract: 0 ok, 1 runtime error, 2 usage, 3
// policy-denied, 4 approval-timeout, 5 daemon-unreachable, 6
// degraded-partial, 7 user-denied (the operator's explicit deny — distinct
// from a policy verdict, because "who said no" is a governance fact).
//
// The interactive contract: the exit code reflects the most recent
// governance outcome when the TUI exits — a denial superseded by a later
// approval exits 0; quitting while the daemon was never reached exits 5.
type Outcome int

const (
	OutcomeOK Outcome = iota
	OutcomeRuntimeError
	OutcomeUsage
	OutcomePolicyDenied
	OutcomeApprovalTimeout
	OutcomeDaemonUnreachable
	OutcomeDegradedPartial
	OutcomeUserDenied
)

// ExitCode returns the process exit code for the outcome.
func (o Outcome) ExitCode() int {
	switch o {
	case OutcomeOK:
		return 0
	case OutcomeRuntimeError:
		return 1
	case OutcomeUsage:
		return 2
	case OutcomePolicyDenied:
		return 3
	case OutcomeApprovalTimeout:
		return 4
	case OutcomeDaemonUnreachable:
		return 5
	case OutcomeDegradedPartial:
		return 6
	case OutcomeUserDenied:
		return 7
	default:
		return 1
	}
}
