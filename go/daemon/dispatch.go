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
		SessionID       string                   `json:"session_id"`
		Prompt          string                   `json:"prompt"`
		SuccessCriteria []scheduler.SuccessCheck `json:"success_criteria"`
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
	task := d.sched.SubmitForDispatch(sess.SessionID, sess.WorkspaceID, p.Prompt, p.SuccessCriteria)
	d.record(sess.SessionID, "TaskCreated", task.TaskID, "go",
		map[string]any{"task_id": task.TaskID, "user_prompt": task.UserPrompt, "mode": "dispatch"}, "")
	d.persistRun(task.TaskID)
	return task, nil
}

// handleWorkPoll leases the next queued dispatch task to a registered worker.
// Polling also counts as a heartbeat (the worker is demonstrably alive).
func (d *Daemon) handleWorkPoll(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID string `json:"worker_id"`
		TTLMs    int    `json:"ttl_ms"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.pool.Get(p.WorkerID); !ok {
		return nil, fmt.Errorf("unknown worker %s (register first)", p.WorkerID)
	}
	_ = d.pool.Heartbeat(p.WorkerID)
	task, ok := d.sched.Lease(p.WorkerID, time.Duration(p.TTLMs)*time.Millisecond)
	if !ok {
		return map[string]any{"empty": true}, nil
	}
	d.record(task.SessionID, "TaskCreated", task.TaskID, "worker",
		map[string]any{"status": "leased", "worker_id": p.WorkerID, "attempts": task.Attempts}, "")
	d.persistRun(task.TaskID)
	return map[string]any{"task": task}, nil
}

// handleWorkRenew extends a held lease (the worker's mid-execution heartbeat).
func (d *Daemon) handleWorkRenew(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID string `json:"worker_id"`
		TaskID   string `json:"task_id"`
		TTLMs    int    `json:"ttl_ms"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.pool.Get(p.WorkerID); !ok {
		return nil, fmt.Errorf("unknown worker %s", p.WorkerID)
	}
	_ = d.pool.Heartbeat(p.WorkerID)
	if err := d.sched.RenewLease(p.TaskID, p.WorkerID, time.Duration(p.TTLMs)*time.Millisecond); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// handleWorkReport records a worker's terminal result for a leased task. It is
// idempotent (safe redelivery) and rejects reports from a non-owning worker.
func (d *Daemon) handleWorkReport(params json.RawMessage) (any, error) {
	var p struct {
		WorkerID string   `json:"worker_id"`
		TaskID   string   `json:"task_id"`
		Status   string   `json:"status"`
		Summary  string   `json:"summary"`
		Patches  []string `json:"patches"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if _, ok := d.pool.Get(p.WorkerID); !ok {
		return nil, fmt.Errorf("unknown worker %s", p.WorkerID)
	}
	if err := d.sched.Report(p.TaskID, p.WorkerID, p.Status, p.Summary, p.Patches); err != nil {
		return nil, err
	}
	sessionID := ""
	if t, ok := d.sched.Get(p.TaskID); ok {
		sessionID = t.SessionID
	}
	d.record(sessionID, "TaskCreated", p.TaskID, "worker",
		map[string]any{"status": p.Status, "worker_id": p.WorkerID, "reported": true}, "")
	d.persistRun(p.TaskID)
	return map[string]any{"ok": true}, nil
}
