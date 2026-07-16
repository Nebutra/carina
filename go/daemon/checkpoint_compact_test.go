package daemon

import (
	"strings"
	"testing"
)

func compactFixtureTranscript() *Transcript {
	tr := newTranscript("finish the migration")
	for i := 1; i <= 9; i++ {
		tr.addTurn(Turn{Tool: "read", ActionBrief: "inspect file", Obs: Observation{Content: strings.Repeat("context ", 400)}})
	}
	return tr
}

func TestCompactWALRecoveryRollsForwardOnlyAfterAuditBoundary(t *testing.T) {
	state := t.TempDir()
	runs := newRunStore(state)
	source := &runCheckpoint{Turn: 9, Transcript: compactFixtureTranscript()}
	if err := runs.saveCheckpointChecked("task", source); err != nil {
		t.Fatal(err)
	}
	clone, _ := cloneTranscriptForCompact(source.Transcript)
	clone.Summary = "summary"
	clone.Turns = clone.Turns[len(clone.Turns)-3:]
	clone.CompactionReceipts = []CompactionReceipt{{Version: 2, RemovedTurns: 6}}
	j, err := runs.prepareCompact("task", "compact_1", runCheckpointID("task", source), &runCheckpoint{Turn: 9, Transcript: clone})
	if err != nil {
		t.Fatal(err)
	}
	// A crash before the audit boundary must leave latest untouched.
	restarted := newRunStore(state)
	if got := restarted.loadCheckpoint("task"); runCheckpointID("task", got) != runCheckpointID("task", source) {
		t.Fatalf("prepared WAL published early: %s", runCheckpointID("task", got))
	}
	j.State = "audited"
	if err = restarted.writeCompactJournal("task", j); err != nil {
		t.Fatal(err)
	}
	// A crash after the audit boundary rolls forward the immutable target.
	recovered := newRunStore(state)
	got := recovered.loadCheckpoint("task")
	if got == nil || runCheckpointID("task", got) != j.Target.CheckpointID {
		t.Fatalf("recovery latest=%#v want %s", got, j.Target.CheckpointID)
	}
	if recovered.loadCheckpointID("task", runCheckpointID("task", source)) == nil {
		t.Fatal("source checkpoint was not preserved")
	}
	journal, err := recovered.loadCompactJournal("task")
	if err != nil || journal == nil || journal.State != "committed" {
		t.Fatalf("journal=%+v err=%v", journal, err)
	}
}

func TestCompactCommitIsIdempotent(t *testing.T) {
	state := t.TempDir()
	runs := newRunStore(state)
	source := &runCheckpoint{Turn: 9, Transcript: compactFixtureTranscript()}
	if err := runs.saveCheckpointChecked("task", source); err != nil {
		t.Fatal(err)
	}
	clone, _ := cloneTranscriptForCompact(source.Transcript)
	clone.CompactionReceipts = []CompactionReceipt{{Version: 2, RemovedTurns: 1}}
	j, err := runs.prepareCompact("task", "compact_2", runCheckpointID("task", source), &runCheckpoint{Turn: 9, Transcript: clone})
	if err != nil {
		t.Fatal(err)
	}
	j.State = "audited"
	if err = runs.writeCompactJournal("task", j); err != nil {
		t.Fatal(err)
	}
	if err = runs.commitCompact("task", j); err != nil {
		t.Fatal(err)
	}
	if err = runs.commitCompact("task", j); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if got := len(runs.listCheckpoints("task")); got != 2 {
		t.Fatalf("history rows=%d want 2", got)
	}
}

func TestCheckpointCompactRequiresIdleTaskAndPreservesSource(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "compact me")
	source := &runCheckpoint{Turn: 9, Transcript: compactFixtureTranscript()}
	if err := d.runs.saveCheckpointChecked(task.TaskID, source); err != nil {
		t.Fatal(err)
	}
	params := mustJSON(t, map[string]any{"session_id": sess.SessionID, "task_id": task.TaskID})
	// Queued/running is not idle — refuse mid-execution.
	if _, err := d.handleCheckpointCompact(params); err == nil || !strings.Contains(err.Error(), "idle") {
		// Submit leaves queued/running; message may say "idle" or active task.
		if err == nil || (!strings.Contains(err.Error(), "idle") && !strings.Contains(err.Error(), "queued") && !strings.Contains(err.Error(), "running") && !strings.Contains(err.Error(), "pause")) {
			t.Fatalf("non-idle compact err=%v", err)
		}
	}
	if _, err := d.sched.RestoreCheckpoint(task.TaskID, nil); err != nil {
		t.Fatal(err)
	}
	d.SetSummarizer(&scriptedReasoner{steps: []string{"decisions preserved; continue migration"}})
	result, err := d.handleCheckpointCompact(params)
	if err != nil {
		t.Fatal(err)
	}
	row := result.(map[string]any)
	if row["compacted"] != true {
		t.Fatalf("result=%#v", row)
	}
	if d.runs.loadCheckpointID(task.TaskID, runCheckpointID(task.TaskID, source)) == nil {
		t.Fatal("source checkpoint missing after compact")
	}
	latest := d.runs.loadCheckpoint(task.TaskID)
	if latest == nil || latest.ParentCheckpointID != runCheckpointID(task.TaskID, source) || len(latest.Transcript.CompactionReceipts) == 0 {
		t.Fatalf("latest=%#v", latest)
	}
}

func TestCheckpointCompactAllowsCompletedTask(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "full-workspace")
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "full-workspace", nil); err != nil {
		t.Fatal(err)
	}
	task := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "done work")
	source := &runCheckpoint{Turn: 4, Transcript: compactFixtureTranscript()}
	if err := d.runs.saveCheckpointChecked(task.TaskID, source); err != nil {
		t.Fatal(err)
	}
	d.sched.SetStatus(task.TaskID, "completed")
	d.SetSummarizer(&scriptedReasoner{steps: []string{"completed work summarized"}})
	result, err := d.handleCheckpointCompact(mustJSON(t, map[string]any{"session_id": sess.SessionID, "task_id": task.TaskID}))
	if err != nil {
		t.Fatal(err)
	}
	row := result.(map[string]any)
	if row["compacted"] != true || row["status"] != "completed" {
		t.Fatalf("completed compact result=%#v", row)
	}
	// context.summary should advertise compact for completed+checkpoint.
	summary, err := d.handleContextSummary(mustJSON(t, map[string]any{"session_id": sess.SessionID}))
	if err != nil {
		t.Fatal(err)
	}
	compact := summary.(map[string]any)["compact"].(map[string]any)
	if compact["available"] != true {
		t.Fatalf("context.summary compact after completed: %#v", compact)
	}
}
