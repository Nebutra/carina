package daemon

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
)

// leaseReapInterval is how often the reaper checks for abandoned dispatch leases.
const leaseReapInterval = 5 * time.Second

// reapLeases re-queues dispatch tasks whose lease expired (worker crashed or
// stalled) so another worker can pick them up — the at-least-once guarantee.
func (d *Daemon) reapLeases() {
	ticker := time.NewTicker(leaseReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopCh:
			return
		case now := <-ticker.C:
			for _, id := range d.sched.ReapExpiredLeases(now.UTC()) {
				if t, ok := d.sched.Get(id); ok {
					d.record(t.SessionID, "TaskCreated", id, "go",
						map[string]any{"status": "lease_expired_requeued", "attempts": t.Attempts}, "")
					d.persistRun(id)
				}
			}
		}
	}
}

// handleWorkSubmit enqueues a task for remote execution (control-plane only —
// not on the remote allowlist). The task waits until a worker leases it.
func (d *Daemon) handleWorkSubmit(params json.RawMessage) (any, error) {
	var p struct {
		SessionID                  string                   `json:"session_id"`
		Prompt                     string                   `json:"prompt"`
		SuccessCriteria            []scheduler.SuccessCheck `json:"success_criteria"`
		RequiredWorkerCapabilities []string                 `json:"required_worker_capabilities"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	if sess.Status != "active" {
		return nil, fmt.Errorf("session %s is %s, not active", p.SessionID, sess.Status)
	}
	for _, capability := range p.RequiredWorkerCapabilities {
		if capability != "process_tree_containment" && capability != "process_tree_containment:unix_pgrp_v1" && capability != "process_tree_containment:windows_job_v1" {
			return nil, fmt.Errorf("unsupported required worker capability %q", capability)
		}
	}
	task := d.sched.SubmitForDispatchWithCapabilities(sess.SessionID, sess.WorkspaceID, p.Prompt, p.SuccessCriteria, p.RequiredWorkerCapabilities)
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
		map[string]any{"task_id": task.TaskID, "user_prompt": task.UserPrompt, "mode": "dispatch"}, "")
	d.persistRun(task.TaskID)
	d.emitDebug("scheduler", "work_submitted", task.TaskID, map[string]string{
		"session_id": sess.SessionID,
		"task_id":    task.TaskID,
	})
	return task, nil
}

// handleWorkPoll leases the next queued dispatch task to a registered worker.
// Polling also counts as a heartbeat (the worker is demonstrably alive).
func (d *Daemon) handleWorkPoll(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID         string `json:"worker_id"`
		WorkerCredential string `json:"worker_credential"`
		TTLMs            int    `json:"ttl_ms"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := d.authenticateWorker(p.WorkerID, p.WorkerCredential); err != nil {
		return nil, err
	}
	_ = d.pool.Heartbeat(p.WorkerID)
	directive := d.backpressure.directive(p.WorkerID, time.Now().UTC())
	if directive.MaxInflight == 0 {
		d.emitDebug("backpressure", "poll_throttled", p.WorkerID, map[string]string{
			"worker_id": p.WorkerID,
			"level":     directive.Level,
			"reason":    directive.Reason,
		})
		return map[string]any{"empty": true, "backpressure": directive}, nil
	}
	w, ok := d.pool.Get(p.WorkerID)
	if !ok {
		return nil, fmt.Errorf("worker authentication failed")
	}
	task, ok := d.sched.LeaseMatching(p.WorkerID, time.Duration(p.TTLMs)*time.Millisecond, w.Supports)
	if !ok {
		return map[string]any{"empty": true, "backpressure": directive}, nil
	}
	d.record(task.SessionID, "TaskCreated", task.TaskID, "worker",
		map[string]any{"status": "leased", "worker_id": p.WorkerID, "attempts": task.Attempts}, "")
	d.persistRun(task.TaskID)
	d.emitDebug("scheduler", "work_leased", task.TaskID, map[string]string{
		"worker_id": p.WorkerID,
		"task_id":   task.TaskID,
		"level":     directive.Level,
	})
	return map[string]any{"task": task, "backpressure": directive}, nil
}

// handleWorkRenew extends a held lease (the worker's mid-execution heartbeat).
func (d *Daemon) handleWorkRenew(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID         string `json:"worker_id"`
		WorkerCredential string `json:"worker_credential"`
		TaskID           string `json:"task_id"`
		LeaseGeneration  int    `json:"lease_generation"`
		TTLMs            int    `json:"ttl_ms"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := d.authenticateWorker(p.WorkerID, p.WorkerCredential); err != nil {
		return nil, err
	}
	_ = d.pool.Heartbeat(p.WorkerID)
	if task, ok := d.sched.Get(p.TaskID); ok && task.Status == "cancelled" {
		return map[string]any{"ok": false, "cancelled": true}, nil
	}
	if err := d.sched.RenewLease(p.TaskID, p.WorkerID, p.LeaseGeneration, time.Duration(p.TTLMs)*time.Millisecond); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// maxRemoteChannelMessagesPerReport bounds work.report's optional
// "channel_messages" array — an RPC-boundary sanity limit against a buggy or
// malicious worker/executor reporting an unbounded batch in one call. This
// is deliberately independent of maxMessagesPerChannel (the broker's own
// per-channel retention cap), which still applies on top per channel.
const maxRemoteChannelMessagesPerReport = 64

// handleWorkReport records a worker's terminal result for a leased task. It is
// idempotent (safe redelivery) and rejects reports from a non-owning worker.
func (d *Daemon) handleWorkReport(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID         string                 `json:"worker_id"`
		WorkerCredential string                 `json:"worker_credential"`
		TaskID           string                 `json:"task_id"`
		LeaseGeneration  int                    `json:"lease_generation"`
		Status           string                 `json:"status"`
		Summary          string                 `json:"summary"`
		Patches          []string               `json:"patches"`
		ChannelMessages  []remoteChannelMessage `json:"channel_messages,omitempty"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if err := d.authenticateWorker(p.WorkerID, p.WorkerCredential); err != nil {
		return nil, err
	}
	if len(p.ChannelMessages) > maxRemoteChannelMessagesPerReport {
		return nil, fmt.Errorf("at most %d channel_messages may be reported at once", maxRemoteChannelMessagesPerReport)
	}
	if task, ok := d.sched.Get(p.TaskID); ok && task.Status == "cancelled" {
		return map[string]any{"ok": false, "cancelled": true}, nil
	}
	if err := d.sched.Report(p.TaskID, p.WorkerID, p.LeaseGeneration, p.Status, p.Summary, p.Patches); err != nil {
		return nil, err
	}
	var task *scheduler.Task
	sessionID := ""
	if t, ok := d.sched.Get(p.TaskID); ok {
		task, sessionID = t, t.SessionID
	}
	// A dispatch task that a streaming workflow step routed here (see
	// runStreamingStepRemote) has a swarm channel binding registered for its
	// entire lease lifetime; anything else (a plain work.submit task with no
	// workflow behind it) has none, and channel_messages — if a worker sent
	// any anyway — are simply not publishable, not a hard error, since the
	// worker's terminal report itself must still be recorded either way.
	if len(p.ChannelMessages) > 0 && task != nil {
		if raw, ok := d.dispatchSwarmBindings.Load(p.TaskID); ok {
			if sess, ok := d.store.Get(sessionID); ok {
				d.publishRemoteChannelMessagesBestEffort(sess, task, raw.(*swarmChannelBinding), p.ChannelMessages)
			}
		}
	}
	d.record(sessionID, "TaskCreated", p.TaskID, "worker",
		map[string]any{"status": p.Status, "worker_id": p.WorkerID, "reported": true}, "")
	d.persistRun(p.TaskID)
	d.emitDebug("worker", "work_reported", p.TaskID, map[string]string{
		"worker_id": p.WorkerID,
		"task_id":   p.TaskID,
		"status":    p.Status,
	})
	d.emitCompletion(sessionID, task)
	return map[string]any{"ok": true}, nil
}
