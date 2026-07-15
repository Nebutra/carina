package daemon

import "sync"

func (d *Daemon) sessionExecutionFence(sessionID string) *sync.RWMutex {
	if fence, ok := d.sessionFences.Load(sessionID); ok {
		return fence.(*sync.RWMutex)
	}
	fence := &sync.RWMutex{}
	actual, _ := d.sessionFences.LoadOrStore(sessionID, fence)
	return actual.(*sync.RWMutex)
}

func (d *Daemon) activeSessionTask(sessionID string) *schedulerTaskSnapshot {
	for _, task := range d.sched.List() {
		if task.SessionID != sessionID {
			continue
		}
		switch task.Status {
		case "queued", "running", "waiting_input", "waiting_approval":
			return &schedulerTaskSnapshot{id: task.TaskID, status: task.Status}
		}
	}
	return nil
}

type schedulerTaskSnapshot struct {
	id     string
	status string
}
