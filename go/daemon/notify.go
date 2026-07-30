package daemon

import (
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	carinatelemetry "github.com/Nebutra/carina/go/telemetry"
)

// emitCompletion publishes the structured execution envelope on the event bus
// when a run reaches a terminal state. It is a single notification a client or
// parent agent can await, instead of scraping the raw event log for a status
// transition. Safe to call for any terminal path (finish, degrade, cancel,
// remote report).
func (d *Daemon) emitCompletion(sessionID string, run *scheduler.ExecutionRun) {
	if run == nil {
		return
	}
	// Prefer the latest scheduler snapshot so the envelope carries the final
	// status/summary/patches/tokens even if the caller holds a stale copy.
	t := run
	if latest, ok := d.sched.Get(run.RunID); ok {
		t = latest
	}
	var durationMs int64
	if !t.CreatedAt.IsZero() {
		durationMs = time.Now().UTC().Sub(t.CreatedAt).Milliseconds()
	}
	d.events.Publish(sessionID, map[string]any{
		"type":            "execution.completed",
		"session_id":      sessionID,
		"run_id":          t.RunID,
		"agent":           t.Agent,
		"status":          t.Status,
		"summary":         t.Summary,
		"applied_patches": t.AppliedPatches,
		"tokens_used":     t.TokensUsed,
		"mode":            t.Mode,
		"duration_ms":     durationMs,
		"timestamp":       time.Now().UTC().Format(time.RFC3339),
	})
	_ = d.telemetry.Metric("carina.execution.completed", carinatelemetry.Attribution{
		WorkspaceID: t.WorkspaceID,
		SessionID:   sessionID,
		RunID:       t.RunID,
	}, carinatelemetry.Cost{Requests: 1, InputTokens: int64(t.TokensUsed)})
	_ = d.telemetry.Log("carina.execution.outcome", carinatelemetry.Attribution{
		WorkspaceID: t.WorkspaceID,
		SessionID:   sessionID,
		RunID:       t.RunID,
	}, map[string]any{"status": t.Status, "duration_ms": durationMs, "mode": t.Mode})
}

func (d *Daemon) emitTaskCompletion(sessionID string, task *scheduler.Task) {
	if task == nil {
		return
	}
	d.events.Publish(sessionID, map[string]any{
		"type": "task.completed", "session_id": sessionID, "task_id": task.TaskID,
		"status": task.Status, "summary": task.Summary, "applied_patches": task.AppliedPatches,
		"tokens_used": task.TokensUsed, "attempts": task.Attempts,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}
