package daemon

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

type checkpointParams struct {
	SessionID    string `json:"session_id"`
	CheckpointID string `json:"checkpoint_id"`
	Confirmed    bool   `json:"confirmed"`
}

func (d *Daemon) handleCheckpointList(params json.RawMessage) (any, error) {
	var p checkpointParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	if _, ok := d.store.Get(p.SessionID); !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	type listedCheckpoint struct {
		task *scheduler.Task
		cp   *runCheckpoint
	}
	listed := make([]listedCheckpoint, 0)
	for _, task := range d.sched.List() {
		if task.SessionID != p.SessionID {
			continue
		}
		for _, cp := range d.runs.listCheckpoints(task.TaskID) {
			listed = append(listed, listedCheckpoint{task: task, cp: cp})
		}
	}
	// Global oldest-to-newest checkpoint chronology. New rows carry a durable
	// creation time and sequence; legacy rows receive their persisted file
	// mtime while loading. Stable ids/turns break the remaining ties.
	sort.Slice(listed, func(i, j int) bool {
		left, right := listed[i], listed[j]
		leftTime := checkpointCreatedAt(left.cp)
		rightTime := checkpointCreatedAt(right.cp)
		if !leftTime.Equal(rightTime) {
			return leftTime.Before(rightTime)
		}
		if left.cp.Sequence != right.cp.Sequence {
			return left.cp.Sequence < right.cp.Sequence
		}
		if left.task.TaskID != right.task.TaskID {
			return left.task.TaskID < right.task.TaskID
		}
		if left.cp.Turn != right.cp.Turn {
			return left.cp.Turn < right.cp.Turn
		}
		return checkpointID(left.task, left.cp) < checkpointID(right.task, right.cp)
	})
	out := make([]any, 0, len(listed))
	for _, entry := range listed {
		out = append(out, checkpointInfo(entry.task, entry.cp))
	}
	return out, nil
}

func checkpointCreatedAt(cp *runCheckpoint) time.Time {
	if cp == nil {
		return time.Time{}
	}
	created, err := time.Parse(time.RFC3339Nano, cp.CreatedAt)
	if err != nil {
		return time.Time{}
	}
	return created
}
func (d *Daemon) handleCheckpointPreview(params json.RawMessage) (any, error) {
	task, cp, _, err := d.checkpoint(params)
	if err != nil {
		return nil, err
	}
	current := d.appliedPatchIDsForSession(task.SessionID)
	rollback, err := patchSuffix(current, cp.AppliedPatches)
	if err != nil {
		return nil, err
	}
	return map[string]any{"checkpoint": checkpointInfo(task, cp), "conversation_turns": len(cp.Transcript.Turns), "summary": cp.Transcript.Summary, "rollback_patches": rollback, "will_resume": "paused"}, nil
}
func (d *Daemon) handleCheckpointSummarize(params json.RawMessage) (any, error) {
	task, cp, _, err := d.checkpoint(params)
	if err != nil {
		return nil, err
	}
	recent := cp.Transcript.Turns
	if recent == nil {
		recent = make([]Turn, 0)
	}
	return map[string]any{"checkpoint_id": checkpointID(task, cp), "task_id": task.TaskID, "turn": cp.Turn, "summary": cp.Transcript.Summary, "recent": recent}, nil
}
func (d *Daemon) handleCheckpointRestore(params json.RawMessage) (any, error) {
	d.checkpointMu.Lock()
	defer d.checkpointMu.Unlock()

	task, cp, p, err := d.checkpoint(params)
	if err != nil {
		return nil, err
	}
	if d.runs.isTombstoned(task.TaskID) {
		return nil, fmt.Errorf("checkpoint restore refused: task %s has a durable removal tombstone", task.TaskID)
	}
	if !p.Confirmed {
		return nil, fmt.Errorf("checkpoint restore requires confirmed=true after preview")
	}
	switch task.Status {
	case "running", "queued", "waiting_input", "waiting_approval":
		return nil, fmt.Errorf("checkpoint restore refused while task is %s; stop or pause it first", task.Status)
	}
	if active := d.activeSessionTask(task.SessionID); active != nil {
		return nil, fmt.Errorf("checkpoint restore refused: session task %s is %s; the session must be quiescent", active.id, active.status)
	}
	fence := d.sessionExecutionFence(task.SessionID)
	if !fence.TryLock() {
		return nil, fmt.Errorf("checkpoint restore refused: session %s has active execution or a patch mutation in flight", task.SessionID)
	}
	defer fence.Unlock()
	if active := d.activeSessionTask(task.SessionID); active != nil {
		return nil, fmt.Errorf("checkpoint restore refused: session task %s became %s; the session must be quiescent", active.id, active.status)
	}
	existing, err := d.runs.loadRestoreJournal(task.TaskID)
	if err != nil {
		return nil, fmt.Errorf("checkpoint restore journal is unreadable: %w", err)
	}
	if existing != nil && existing.CheckpointID != p.CheckpointID {
		return nil, fmt.Errorf("checkpoint_restore_blocked: task %s has an incomplete restore for %s; retry that checkpoint before restoring %s", task.TaskID, existing.CheckpointID, p.CheckpointID)
	}
	if existing != nil && existing.OperationID == "" {
		existing.OperationID = sessionstore.NewID("restore")
	}
	if d.checkpointRestoreCommitted(task, cp, p.CheckpointID) {
		if existing == nil {
			return checkpointRestoreResult(task, cp, nil, true, false), nil
		}
		if existing.State == "committed" {
			return d.finishCommittedCheckpointRestore(task, cp, existing, true)
		}
		count, auditErr := d.restoreAuditPhaseCount(task.SessionID, task.TaskID, existing.OperationID, "completion")
		if auditErr != nil {
			return d.blockCheckpointRestore(task, existing, fmt.Errorf("confirm completion audit: %w", auditErr))
		}
		if count > 1 {
			return d.blockCheckpointRestore(task, existing, fmt.Errorf("completion audit duplicated for operation %s", existing.OperationID))
		}
		if count == 1 {
			return d.finishCommittedCheckpointRestore(task, cp, existing, true)
		}
	}
	current := d.appliedPatchIDsForSession(task.SessionID)
	rollback, err := patchSuffix(current, cp.AppliedPatches)
	if err != nil {
		return nil, err
	}
	// Preflight every rollback pointer before mutating the first file. This
	// prevents a known-invalid later patch from leaving a partially restored tree.
	for _, patchID := range rollback {
		patch, err := d.kern.PatchShow(task.SessionID, patchID)
		if err != nil {
			return nil, fmt.Errorf("checkpoint restore preflight %s: %w", patchID, err)
		}
		if patch.Status != "applied" || strings.TrimSpace(patch.RollbackPointer) == "" {
			return nil, fmt.Errorf("checkpoint restore preflight %s: patch is not rollbackable", patchID)
		}
	}
	journal := &restoreJournal{
		Version:              restoreJournalVersion,
		OperationID:          sessionstore.NewID("restore"),
		CheckpointID:         p.CheckpointID,
		TargetTurn:           cp.Turn,
		TargetAppliedPatches: append([]string(nil), cp.AppliedPatches...),
		Pending:              append([]string(nil), rollback...),
		State:                "prepared",
		UpdatedAt:            time.Now().UTC().Format(time.RFC3339Nano),
	}
	if existing != nil {
		journal.OperationID = existing.OperationID
		journal.Completed = append([]string(nil), existing.Completed...)
	}
	if err := d.runs.writeRestoreJournal(task.TaskID, journal); err != nil {
		return nil, fmt.Errorf("checkpoint restore journal: %w", err)
	}
	if err := d.recordChecked(task.SessionID, "TaskCreated", task.TaskID, "operator", map[string]any{
		"status": "checkpoint_restore_requested", "checkpoint_id": p.CheckpointID, "turn": cp.Turn,
		"operation_id": journal.OperationID, "phase": "requested",
	}, ""); err != nil {
		return d.blockCheckpointRestore(task, journal, fmt.Errorf("write-ahead audit append failed: %w", err))
	}
	journal.State = "rolling_back"
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := d.runs.writeRestoreJournal(task.TaskID, journal); err != nil {
		return d.blockCheckpointRestore(task, journal, fmt.Errorf("persist rollback state: %w", err))
	}
	for i := len(rollback) - 1; i >= 0; i-- {
		if _, err := d.kern.PatchRollback(task.SessionID, rollback[i]); err != nil {
			journal.Pending = append([]string(nil), rollback[:i+1]...)
			return d.blockCheckpointRestore(task, journal, fmt.Errorf("rollback %s: %w", rollback[i], err))
		}
		journal.Completed = append(journal.Completed, rollback[i])
		journal.Pending = append([]string(nil), rollback[:i]...)
		journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := d.runs.writeRestoreJournal(task.TaskID, journal); err != nil {
			return d.blockCheckpointRestore(task, journal, fmt.Errorf("persist rollback progress: %w", err))
		}
	}
	journal.State = "publishing_latest"
	journal.Pending = nil
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := d.runs.writeRestoreJournal(task.TaskID, journal); err != nil {
		return d.blockCheckpointRestore(task, journal, fmt.Errorf("persist latest-publish state: %w", err))
	}
	if err := d.runs.publishCheckpointLatest(task.TaskID, cp); err != nil {
		return d.blockCheckpointRestore(task, journal, fmt.Errorf("publish selected checkpoint as latest: %w", err))
	}
	journal.State = "persisting_task"
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := d.runs.writeRestoreJournal(task.TaskID, journal); err != nil {
		return d.blockCheckpointRestore(task, journal, fmt.Errorf("persist task-commit state: %w", err))
	}
	restored, err := d.sched.RestoreCheckpoint(task.TaskID, cp.AppliedPatches)
	if err != nil {
		return d.blockCheckpointRestore(task, journal, err)
	}
	if err := d.runs.saveChecked(restored); err != nil {
		return d.blockCheckpointRestore(restored, journal, fmt.Errorf("persist restored task: %w", err))
	}
	journal.State = "audit_completion"
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := d.runs.writeRestoreJournal(task.TaskID, journal); err != nil {
		return d.blockCheckpointRestore(restored, journal, fmt.Errorf("persist audit-completion state: %w", err))
	}
	completionCount, err := d.restoreAuditPhaseCount(task.SessionID, task.TaskID, journal.OperationID, "completion")
	if err != nil {
		return d.blockCheckpointRestore(restored, journal, fmt.Errorf("confirm completion audit: %w", err))
	}
	if completionCount > 1 {
		return d.blockCheckpointRestore(restored, journal, fmt.Errorf("completion audit duplicated for operation %s", journal.OperationID))
	}
	if completionCount == 0 {
		if err := d.recordChecked(task.SessionID, "TaskCreated", task.TaskID, "operator", map[string]any{
			"status": "checkpoint_restored", "checkpoint_id": p.CheckpointID, "turn": cp.Turn, "rolled_back": rollback,
			"operation_id": journal.OperationID, "phase": "completion",
		}, ""); err != nil {
			return d.blockCheckpointRestore(restored, journal, fmt.Errorf("completion audit append failed: %w", err))
		}
	}
	journal.State = "committed"
	journal.Failure = ""
	journal.RecoveryAction = ""
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := d.runs.writeRestoreJournal(task.TaskID, journal); err != nil {
		return d.blockCheckpointRestore(restored, journal, fmt.Errorf("persist restore commit marker: %w", err))
	}
	cleanupPending := false
	if err := d.runs.clearRestoreJournal(task.TaskID); err != nil {
		cleanupPending = true
	}
	return checkpointRestoreResult(restored, cp, rollback, false, cleanupPending), nil
}

func (d *Daemon) checkpointRestoreCommitted(task *scheduler.Task, cp *runCheckpoint, id string) bool {
	if task.Status != "paused" || !slices.Equal(task.AppliedPatches, cp.AppliedPatches) {
		return false
	}
	latest := d.runs.loadCheckpoint(task.TaskID)
	if latest == nil || checkpointID(task, latest) != id {
		return false
	}
	return slices.Equal(d.appliedPatchIDsForSession(task.SessionID), cp.AppliedPatches)
}

func (d *Daemon) finishCommittedCheckpointRestore(task *scheduler.Task, cp *runCheckpoint, journal *restoreJournal, idempotent bool) (any, error) {
	restored, err := d.sched.RestoreCheckpoint(task.TaskID, cp.AppliedPatches)
	if err != nil {
		return d.blockCheckpointRestore(task, journal, err)
	}
	if err := d.runs.saveChecked(restored); err != nil {
		return d.blockCheckpointRestore(restored, journal, fmt.Errorf("persist committed restore task: %w", err))
	}
	journal.State = "committed"
	journal.Failure = ""
	journal.RecoveryAction = ""
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := d.runs.writeRestoreJournal(task.TaskID, journal); err != nil {
		return d.blockCheckpointRestore(restored, journal, fmt.Errorf("persist restore commit marker: %w", err))
	}
	cleanupPending := d.runs.clearRestoreJournal(task.TaskID) != nil
	return checkpointRestoreResult(restored, cp, nil, idempotent, cleanupPending), nil
}

func (d *Daemon) restoreAuditPhaseCount(sessionID, taskID, operationID, phase string) (int, error) {
	if strings.TrimSpace(operationID) == "" {
		return 0, fmt.Errorf("restore operation_id is required")
	}
	raw, err := d.kern.ReadEvents(sessionID)
	if err != nil {
		return 0, err
	}
	var events []struct {
		TaskID  string         `json:"task_id"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		return 0, err
	}
	count := 0
	for _, event := range events {
		if event.TaskID == taskID && event.Payload["operation_id"] == operationID && event.Payload["phase"] == phase {
			count++
		}
	}
	return count, nil
}

func checkpointRestoreResult(task *scheduler.Task, cp *runCheckpoint, rollback []string, idempotent, cleanupPending bool) map[string]any {
	return map[string]any{
		"restored": true, "checkpoint_id": checkpointID(task, cp), "task_id": task.TaskID,
		"turn": cp.Turn, "rolled_back": nonNilStrings(rollback), "status": "paused", "idempotent": idempotent,
		"reconciliation_required": false, "journal_cleanup_pending": cleanupPending,
	}
}

func (d *Daemon) blockCheckpointRestore(task *scheduler.Task, journal *restoreJournal, cause error) (any, error) {
	reason := cause.Error()
	journal.State = "blocked_reconciliation_required"
	journal.Failure = reason
	journal.RecoveryAction = "retry session.checkpoint.restore with the same checkpoint_id"
	journal.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	journalErr := d.runs.writeRestoreJournal(task.TaskID, journal)
	blocked, schedulerErr := d.sched.MarkReconciliationRequired(task.TaskID, reason, d.appliedPatchIDsForSession(task.SessionID))
	persistErr := d.runs.saveChecked(blocked)
	if journalErr != nil {
		reason += "; journal update failed: " + journalErr.Error()
	}
	if schedulerErr != nil {
		reason += "; scheduler block failed: " + schedulerErr.Error()
	}
	if persistErr != nil {
		reason += "; blocked task persistence failed: " + persistErr.Error()
	}
	return nil, fmt.Errorf("checkpoint_restore_blocked: %s; reconciliation_required=true; retry checkpoint_id %s", reason, journal.CheckpointID)
}

func (d *Daemon) handleTaskResume(params json.RawMessage) (any, error) {
	d.checkpointMu.Lock()
	defer d.checkpointMu.Unlock()
	var p struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	p.TaskID = strings.TrimSpace(p.TaskID)
	if p.TaskID == "" {
		return nil, fmt.Errorf("task_id is required")
	}
	task, ok := d.sched.Get(p.TaskID)
	if !ok {
		return nil, fmt.Errorf("unknown task %s", p.TaskID)
	}
	if d.runs.isTombstoned(task.TaskID) {
		return nil, fmt.Errorf("task_resume_blocked: task %s has a durable removal tombstone", task.TaskID)
	}
	if task.Status != "paused" {
		return nil, fmt.Errorf("task %s is %s, not paused", p.TaskID, task.Status)
	}
	if task.ReconciliationRequired {
		return nil, fmt.Errorf("task_resume_blocked: checkpoint reconciliation required: %s", task.BlockedReason)
	}
	if journal, err := d.runs.loadRestoreJournal(task.TaskID); err != nil {
		return nil, fmt.Errorf("task_resume_blocked: restore journal is unreadable: %w", err)
	} else if journal != nil && journal.State != "committed" {
		return nil, fmt.Errorf("task_resume_blocked: checkpoint reconciliation required; retry checkpoint_id %s", journal.CheckpointID)
	}
	sess, ok := d.store.Get(task.SessionID)
	if !ok {
		return nil, fmt.Errorf("task_resume_blocked: session %s is unavailable", task.SessionID)
	}
	if sess.Status != "active" {
		return nil, fmt.Errorf("task_resume_blocked: session %s is %s, not active", task.SessionID, sess.Status)
	}
	fence := d.sessionExecutionFence(task.SessionID)
	fence.RLock()
	defer fence.RUnlock()
	if d.reasoner == nil {
		return nil, fmt.Errorf("task_resume_blocked: no reasoner is configured")
	}
	cp := d.runs.loadCheckpoint(task.TaskID)
	if cp == nil {
		return nil, fmt.Errorf("task_resume_blocked: latest checkpoint is unavailable")
	}
	actual := d.appliedPatchIDsForSession(task.SessionID)
	if !slices.Equal(task.AppliedPatches, cp.AppliedPatches) || !slices.Equal(actual, cp.AppliedPatches) {
		reason := "latest checkpoint, durable task, and workspace patch lineages do not match"
		blocked, _ := d.sched.MarkReconciliationRequired(task.TaskID, reason, actual)
		if err := d.runs.saveChecked(blocked); err != nil {
			return nil, fmt.Errorf("task_resume_blocked: %s; persist reconciliation state: %w", reason, err)
		}
		return nil, fmt.Errorf("task_resume_blocked: %s; reconciliation_required=true", reason)
	}
	if err := d.recordChecked(task.SessionID, "TaskCreated", task.TaskID, "operator", map[string]any{
		"status": "resume_requested", "checkpoint_id": checkpointID(task, cp), "turn": cp.Turn,
	}, ""); err != nil {
		return nil, fmt.Errorf("task_resume_blocked: write-ahead audit append failed: %w", err)
	}
	running, err := d.sched.Resume(task.TaskID)
	if err != nil {
		return nil, err
	}
	if err := d.runs.saveChecked(running); err != nil {
		d.sched.SetStatus(task.TaskID, "paused")
		return nil, fmt.Errorf("task_resume_blocked: persist running state before launch: %w", err)
	}
	d.startTask(func() { d.resumeTaskGuarded(sess, running, cp) })
	return running, nil
}
func (d *Daemon) checkpoint(params json.RawMessage) (*scheduler.Task, *runCheckpoint, checkpointParams, error) {
	var p checkpointParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, nil, p, err
	}
	if strings.TrimSpace(p.SessionID) == "" || strings.TrimSpace(p.CheckpointID) == "" {
		return nil, nil, p, fmt.Errorf("session_id and checkpoint_id are required")
	}
	taskID, _, ok := strings.Cut(p.CheckpointID, ":")
	if !ok {
		return nil, nil, p, fmt.Errorf("invalid checkpoint_id %q", p.CheckpointID)
	}
	task, found := d.sched.Get(taskID)
	if !found || task.SessionID != p.SessionID {
		return nil, nil, p, fmt.Errorf("checkpoint does not belong to session %s", p.SessionID)
	}
	cp := d.runs.loadCheckpointID(taskID, p.CheckpointID)
	if cp == nil || checkpointID(task, cp) != p.CheckpointID {
		return nil, nil, p, fmt.Errorf("checkpoint %s is no longer available", p.CheckpointID)
	}
	return task, cp, p, nil
}
func checkpointID(task *scheduler.Task, cp *runCheckpoint) string {
	return runCheckpointID(task.TaskID, cp)
}
func checkpointInfo(task *scheduler.Task, cp *runCheckpoint) map[string]any {
	return map[string]any{"checkpoint_id": checkpointID(task, cp), "parent_checkpoint_id": cp.ParentCheckpointID, "created_at": cp.CreatedAt, "sequence": fmt.Sprintf("%020d", cp.Sequence), "task_id": task.TaskID, "session_id": task.SessionID, "turn": cp.Turn, "summary": cp.Transcript.Summary, "applied_patches": nonNilStrings(cp.AppliedPatches)}
}
func (d *Daemon) appliedPatchIDsForSession(sessionID string) []string {
	sess, ok := d.store.Get(sessionID)
	if !ok {
		return nil
	}
	return d.appliedPatchIDs(sess)
}
func patchSuffix(current, target []string) ([]string, error) {
	if len(target) > len(current) {
		return nil, fmt.Errorf("checkpoint patch lineage is ahead of current state")
	}
	for i := range target {
		if target[i] != current[i] {
			return nil, fmt.Errorf("checkpoint restore refused: patch lineage diverged")
		}
	}
	return nonNilStrings(current[len(target):]), nil
}

func nonNilStrings(values []string) []string {
	out := make([]string, len(values))
	copy(out, values)
	return out
}
