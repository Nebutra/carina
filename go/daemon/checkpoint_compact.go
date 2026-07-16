package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func cloneTranscriptForCompact(src *Transcript) (*Transcript, error) {
	raw, err := json.Marshal(src)
	if err != nil {
		return nil, err
	}
	var out Transcript
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out.policy = defaultCompactionPolicy()
	return &out, nil
}

func (d *Daemon) compactTaskForSession(sessionID, taskID string) (*scheduler.Task, error) {
	if strings.TrimSpace(taskID) != "" {
		task, ok := d.sched.Get(taskID)
		if !ok || task.SessionID != sessionID {
			return nil, fmt.Errorf("task %s does not belong to session", taskID)
		}
		return task, nil
	}
	var candidates []*scheduler.Task
	for _, task := range d.sched.List() {
		if task.SessionID == sessionID && task.Status == "paused" {
			candidates = append(candidates, task)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].UpdatedAt.After(candidates[j].UpdatedAt) })
	if len(candidates) == 0 {
		return nil, fmt.Errorf("compact requires a paused task checkpoint")
	}
	return candidates[0], nil
}

func (d *Daemon) handleCheckpointCompact(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		TaskID    string `json:"task_id"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.store.Get(p.SessionID); !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	d.checkpointMu.Lock()
	defer d.checkpointMu.Unlock()
	task, err := d.compactTaskForSession(p.SessionID, p.TaskID)
	if err != nil {
		return nil, err
	}
	if task.Status != "paused" {
		return nil, fmt.Errorf("compact refused while task is %s; pause it first", task.Status)
	}
	if task.ReconciliationRequired {
		return nil, fmt.Errorf("compact refused: checkpoint reconciliation required")
	}
	if active := d.activeSessionTask(p.SessionID); active != nil {
		return nil, fmt.Errorf("compact refused: session task %s is %s", active.id, active.status)
	}
	fence := d.sessionExecutionFence(p.SessionID)
	if !fence.TryLock() {
		return nil, fmt.Errorf("compact refused: session has active execution or patch mutation")
	}
	defer fence.Unlock()
	if existing, loadErr := d.runs.loadCompactJournal(task.TaskID); loadErr != nil {
		return nil, loadErr
	} else if existing != nil {
		if existing.State == "prepared" {
			if err = d.ensureCompactAudit(task, existing, "requested"); err != nil {
				return nil, err
			}
			existing.State = "audited"
			if err = d.runs.writeCompactJournal(task.TaskID, existing); err != nil {
				return nil, err
			}
		}
		if existing.State != "committed" {
			if err = d.runs.commitCompact(task.TaskID, existing); err != nil {
				return nil, fmt.Errorf("compact recovery: %w", err)
			}
		}
		if err = d.ensureCompactAudit(task, existing, "completion"); err != nil {
			return nil, err
		}
		cleanup := d.runs.clearCompactJournal(task.TaskID) != nil
		return compactResult(task, existing, true, cleanup), nil
	}
	source := d.runs.loadCheckpoint(task.TaskID)
	if source == nil || source.Transcript == nil {
		return nil, fmt.Errorf("compact requires a persisted checkpoint")
	}
	clone, err := cloneTranscriptForCompact(source.Transcript)
	if err != nil {
		return nil, err
	}
	clone.policy.MaxChars = max(1, clone.size()-1)
	clone.policy.MaxTokens = 1
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	receipt := clone.compact(func(head string) (string, error) {
		return thinkWithRetry(ctx, d.summarizeReasoner(), "Summarize this checkpoint for loss-aware continuation. Preserve decisions, constraints, failures, pending work, and file paths.\n\n"+head)
	})
	if receipt == nil {
		return map[string]any{"compacted": false, "task_id": task.TaskID, "checkpoint_id": checkpointID(task, source), "reason": "checkpoint has no safely compactable head"}, nil
	}
	target := &runCheckpoint{Turn: source.Turn, Transcript: clone, MemorySnapshot: source.MemorySnapshot, AppliedPatches: append([]string(nil), source.AppliedPatches...)}
	operationID := sessionstore.NewID("compact")
	j, err := d.runs.prepareCompact(task.TaskID, operationID, checkpointID(task, source), target)
	if err != nil {
		return nil, fmt.Errorf("compact WAL prepare: %w", err)
	}
	if err = d.recordChecked(task.SessionID, "ContextCompacted", task.TaskID, "operator", map[string]any{"status": "checkpoint_compact_requested", "operation_id": operationID, "source_checkpoint_id": j.SourceCheckpointID, "target_checkpoint_id": j.Target.CheckpointID, "phase": "requested"}, ""); err != nil {
		return nil, fmt.Errorf("compact write-ahead audit: %w", err)
	}
	j.State = "audited"
	if err = d.runs.writeCompactJournal(task.TaskID, j); err != nil {
		return nil, fmt.Errorf("persist compact audit boundary: %w", err)
	}
	if err = d.runs.commitCompact(task.TaskID, j); err != nil {
		return nil, fmt.Errorf("compact commit (retry is idempotent): %w", err)
	}
	if err = d.recordChecked(task.SessionID, "ContextCompacted", task.TaskID, "operator", map[string]any{"status": "checkpoint_compacted", "operation_id": operationID, "source_checkpoint_id": j.SourceCheckpointID, "target_checkpoint_id": j.Target.CheckpointID, "receipt": receipt, "phase": "completion"}, ""); err != nil {
		return nil, fmt.Errorf("compact committed but completion audit failed: %w", err)
	}
	cleanup := d.runs.clearCompactJournal(task.TaskID) != nil
	return compactResult(task, j, false, cleanup), nil
}

func (d *Daemon) ensureCompactAudit(task *scheduler.Task, j *compactJournal, phase string) error {
	count, err := d.restoreAuditPhaseCount(task.SessionID, task.TaskID, j.OperationID, phase)
	if err != nil {
		return fmt.Errorf("compact %s audit check: %w", phase, err)
	}
	if count > 1 {
		return fmt.Errorf("compact %s audit duplicated for operation %s", phase, j.OperationID)
	}
	if count == 1 {
		return nil
	}
	status := "checkpoint_compact_requested"
	if phase == "completion" {
		status = "checkpoint_compacted"
	}
	payload := map[string]any{"status": status, "operation_id": j.OperationID, "source_checkpoint_id": j.SourceCheckpointID, "target_checkpoint_id": j.Target.CheckpointID, "phase": phase}
	if phase == "completion" && len(j.Target.Transcript.CompactionReceipts) > 0 {
		payload["receipt"] = j.Target.Transcript.CompactionReceipts[len(j.Target.Transcript.CompactionReceipts)-1]
	}
	return d.recordChecked(task.SessionID, "ContextCompacted", task.TaskID, "operator", payload, "")
}

func compactResult(task *scheduler.Task, j *compactJournal, idempotent, cleanup bool) map[string]any {
	return map[string]any{"compacted": true, "task_id": task.TaskID, "checkpoint_id": j.Target.CheckpointID, "source_checkpoint_id": j.SourceCheckpointID, "turn": j.Target.Turn, "status": "paused", "idempotent": idempotent, "journal_cleanup_pending": cleanup, "receipt": j.Target.Transcript.CompactionReceipts[len(j.Target.Transcript.CompactionReceipts)-1]}
}
