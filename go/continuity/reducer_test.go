package continuity

import "testing"

func TestTaskStatusAxesStayOrthogonal(t *testing.T) {
	completed := ForTaskStatus("completed", false)
	if completed.Activity != ActivityIdle || completed.Outcome != OutcomeCompleted || completed.Progress != ProgressPartiallyComplete {
		t.Fatalf("completed without criteria = %+v", completed)
	}
	if got := ForTaskStatus("completed", true).Progress; got != ProgressComplete {
		t.Fatalf("criteria-backed completion progress = %s", got)
	}
	interrupted := ForTaskStatus("interrupted", true)
	if interrupted.Activity != ActivityIdle || interrupted.Outcome != OutcomeInterrupted || interrupted.Recovery.Disposition != RecoveryReviewRequired {
		t.Fatalf("interrupted = %+v", interrupted)
	}
}
