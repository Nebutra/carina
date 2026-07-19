package continuity

// ForTaskStatus derives the user-facing axes from scheduler state. Richer
// projections may refine progress from review evidence, but this mapping stays
// the single compatibility baseline.
func ForTaskStatus(status string, criteriaDeclared bool) State {
	state := EmptyState()
	switch status {
	case "queued":
		state.Progress = ProgressStarted
	case "running":
		state.Activity, state.Progress = ActivityRunning, ProgressInProgress
	case "waiting_input", "needs_input":
		state.Activity, state.Progress = ActivityWaitingInput, ProgressInProgress
		state.Recovery.Disposition = RecoveryContinue
	case "waiting_approval":
		state.Activity, state.Progress = ActivityWaitingApproval, ProgressInProgress
		state.Recovery.Disposition = RecoveryContinue
	case "paused":
		state.Progress = ProgressInProgress
		state.Recovery.Disposition = RecoveryContinue
	case "interrupted":
		state.Outcome, state.Progress = OutcomeInterrupted, ProgressInProgress
		state.Recovery.Disposition = RecoveryReviewRequired
	case "completed":
		state.Outcome = OutcomeCompleted
		if criteriaDeclared {
			state.Progress = ProgressComplete
		} else {
			state.Progress = ProgressPartiallyComplete
		}
	case "degraded":
		state.Outcome, state.Progress = OutcomePartial, ProgressPartiallyComplete
		state.Recovery.Disposition = RecoveryContinue
	case "failed":
		state.Outcome, state.Progress = OutcomeFailed, ProgressPartiallyComplete
		state.Recovery.Disposition = RecoveryReviewRequired
	case "cancelled":
		state.Outcome, state.Progress = OutcomeCancelled, ProgressPartiallyComplete
	}
	return state
}

func MergeTaskStatus(current State, status string, criteriaDeclared bool) State {
	derived := ForTaskStatus(status, criteriaDeclared)
	derived.Interruption = current.Interruption
	derived.Execution = current.Execution
	derived.WorkspaceAnchor = current.WorkspaceAnchor
	derived.RecoveryGeneration = current.RecoveryGeneration
	derived.AutoRecoveryAttempts = current.AutoRecoveryAttempts
	if current.Recovery.Disposition != "" && current.Recovery.Disposition != RecoveryNone {
		derived.Recovery = current.Recovery
	}
	return derived
}
