// Package outcome owns Carina's process exit classification independently of
// any interactive renderer. Both CLI and UI surfaces use this contract.
package outcome

// Outcome classifies how an interactive session or one-shot command ended.
// The numeric values are a public process contract.
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

// ExitCode returns the stable process exit code for the outcome.
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
