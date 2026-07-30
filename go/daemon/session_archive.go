package daemon

import (
	"encoding/json"
	"fmt"
)

func (d *Daemon) handleSessionArchive(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	current, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	if current.Status == "closed" {
		return current, nil
	}
	for _, task := range d.sched.List() {
		if task.SessionID == id && !archiveTerminalTaskStatus(task.Status) {
			return nil, fmt.Errorf("session %s has task %s in %s; finish or cancel it before archiving", id, task.RunID, task.Status)
		}
	}
	sess, err := d.store.SetStatus(id, "closed")
	if err != nil {
		return nil, err
	}
	d.record(id, "SessionArchived", "", "go", map[string]any{"reason": "client request"}, "")
	d.runLifecycleHooks(sess.WorkspaceRoot, "SessionEnd", map[string]any{"session_id": id, "reason": "archived"})
	return sess, nil
}

func (d *Daemon) handleSessionUnarchive(params json.RawMessage) (any, error) {
	id, err := sessionID(params)
	if err != nil {
		return nil, err
	}
	current, ok := d.store.Get(id)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", id)
	}
	if current.Status != "closed" {
		return current, nil
	}
	if err := d.ensureKernelSession(current); err != nil {
		return nil, err
	}
	sess, err := d.store.SetStatus(id, "active")
	if err != nil {
		return nil, err
	}
	d.record(id, "SessionUnarchived", "", "go", map[string]any{"reason": "client request"}, "")
	return sess, nil
}

func archiveTerminalTaskStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "degraded":
		return true
	default:
		return false
	}
}
