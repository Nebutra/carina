package tui

import "testing"

func TestSessionAttentionRankPrioritizesRecovery(t *testing.T) {
	var blocked, recovering, running, done sessionListItem
	blocked.Continuity.Recovery.Disposition = "review_required"
	recovering.Continuity.Recovery.Disposition = "resume_checkpoint"
	running.TaskStatus = "running"
	done.TaskStatus = "completed"
	if !(sessionAttentionRank(blocked) < sessionAttentionRank(recovering) &&
		sessionAttentionRank(recovering) < sessionAttentionRank(running) &&
		sessionAttentionRank(running) < sessionAttentionRank(done)) {
		t.Fatal("session continuation priority is not attention-first")
	}
}
