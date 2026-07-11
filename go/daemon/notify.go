package daemon

import (
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	carinatelemetry "github.com/Nebutra/carina/go/telemetry"
)

// emitCompletion publishes the structured "task done" envelope on the event bus
// when a task reaches a terminal state. It is a single notification a client or
// parent agent can await, instead of scraping the raw event log for a status
// transition. Safe to call for any terminal path (finish, degrade, cancel,
// remote report).
func (d *Daemon) emitCompletion(sessionID string, task *scheduler.Task) {
	if task == nil {
		return
	}
	// Prefer the latest scheduler snapshot so the envelope carries the final
	// status/summary/patches/tokens even if the caller holds a stale copy.
	t := task
	if latest, ok := d.sched.Get(task.TaskID); ok {
		t = latest
	}
	var durationMs int64
	if !t.CreatedAt.IsZero() {
		durationMs = time.Now().UTC().Sub(t.CreatedAt).Milliseconds()
	}
	d.events.Publish(sessionID, map[string]any{
		"type":            "task.completed",
		"session_id":      sessionID,
		"task_id":         t.TaskID,
		"status":          t.Status,
		"summary":         t.Summary,
		"applied_patches": t.AppliedPatches,
		"tokens_used":     t.TokensUsed,
		"attempts":        t.Attempts,
		"mode":            t.Mode,
		"duration_ms":     durationMs,
		"timestamp":       time.Now().UTC().Format(time.RFC3339),
	})
	_ = d.telemetry.Metric("carina.task.completed", carinatelemetry.Attribution{
		WorkspaceID: t.WorkspaceID,
		SessionID:   sessionID,
		TaskID:      t.TaskID,
	}, carinatelemetry.Cost{Requests: int64(t.Attempts), InputTokens: int64(t.TokensUsed)})
	_ = d.telemetry.Log("carina.task.outcome", carinatelemetry.Attribution{
		WorkspaceID: t.WorkspaceID,
		SessionID:   sessionID,
		TaskID:      t.TaskID,
	}, map[string]any{"status": t.Status, "duration_ms": durationMs, "mode": t.Mode})
}
