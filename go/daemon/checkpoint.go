package daemon

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
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
	out := make([]any, 0)
	for _, task := range d.sched.List() {
		if task.SessionID != p.SessionID {
			continue
		}
		for _, cp := range d.runs.listCheckpoints(task.TaskID) {
			out = append(out, checkpointInfo(task, cp))
		}
	}
	return out, nil
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
	return map[string]any{"checkpoint_id": checkpointID(task, cp), "task_id": task.TaskID, "turn": cp.Turn, "summary": cp.Transcript.Summary, "recent": cp.Transcript.Turns}, nil
}
func (d *Daemon) handleCheckpointRestore(params json.RawMessage) (any, error) {
	task, cp, p, err := d.checkpoint(params)
	if err != nil {
		return nil, err
	}
	if !p.Confirmed {
		return nil, fmt.Errorf("checkpoint restore requires confirmed=true after preview")
	}
	switch task.Status {
	case "running", "queued", "waiting_input", "waiting_approval":
		return nil, fmt.Errorf("checkpoint restore refused while task is %s; stop or pause it first", task.Status)
	}
	current := d.appliedPatchIDsForSession(task.SessionID)
	rollback, err := patchSuffix(current, cp.AppliedPatches)
	if err != nil {
		return nil, err
	}
	for i := len(rollback) - 1; i >= 0; i-- {
		if _, err := d.kern.PatchRollback(task.SessionID, rollback[i]); err != nil {
			return nil, fmt.Errorf("checkpoint restore rollback %s: %w", rollback[i], err)
		}
	}
	d.sched.SetAppliedPatches(task.TaskID, cp.AppliedPatches)
	d.sched.SetStatus(task.TaskID, "paused")
	d.runs.saveCheckpoint(task.TaskID, cp)
	d.record(task.SessionID, "TaskCreated", task.TaskID, "operator", map[string]any{"status": "checkpoint_restored", "turn": cp.Turn, "rolled_back": rollback}, "")
	return map[string]any{"restored": true, "checkpoint_id": checkpointID(task, cp), "task_id": task.TaskID, "turn": cp.Turn, "rolled_back": rollback, "status": "paused"}, nil
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
	_, turnText, _ := strings.Cut(p.CheckpointID, ":")
	var turn int
	if _, err := fmt.Sscanf(turnText, "%d", &turn); err != nil || turn < 1 {
		return nil, nil, p, fmt.Errorf("invalid checkpoint_id %q", p.CheckpointID)
	}
	cp := d.runs.loadCheckpointTurn(taskID, turn)
	if cp == nil || checkpointID(task, cp) != p.CheckpointID {
		return nil, nil, p, fmt.Errorf("checkpoint %s is no longer available", p.CheckpointID)
	}
	return task, cp, p, nil
}
func checkpointID(task *scheduler.Task, cp *runCheckpoint) string {
	return fmt.Sprintf("%s:%d", task.TaskID, cp.Turn)
}
func checkpointInfo(task *scheduler.Task, cp *runCheckpoint) map[string]any {
	return map[string]any{"checkpoint_id": checkpointID(task, cp), "task_id": task.TaskID, "session_id": task.SessionID, "turn": cp.Turn, "summary": cp.Transcript.Summary, "applied_patches": cp.AppliedPatches}
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
	return append([]string(nil), current[len(target):]...), nil
}
