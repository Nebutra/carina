package daemon

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
)

func (d *Daemon) resolveForkBoundary(sessionID, requestedID string, throughTurn int) (*scheduler.ExecutionRun, *runCheckpoint, error) {
	runs := d.sessionRunsChronological(sessionID)
	for _, task := range runs {
		switch task.Status {
		case "running", "queued", "waiting_approval", "paused":
			return nil, nil, fmt.Errorf("cannot fork session %s while task %s is %s", sessionID, task.RunID, task.Status)
		}
	}
	if requestedID != "" {
		var found *scheduler.ExecutionRun
		for _, task := range runs {
			if task.RunID == requestedID {
				found = task
				break
			}
		}
		if found == nil {
			return nil, nil, fmt.Errorf("cannot fork session %s without a completed task checkpoint", sessionID)
		}
		if cp := d.checkpointForFork(found, throughTurn); cp != nil {
			return found, cp, nil
		}
		for i := len(runs) - 1; i >= 0; i-- {
			earlier := runs[i]
			if earlier.RunID == found.RunID || !runCreatedBefore(earlier, found) {
				continue
			}
			if cp := d.checkpointForFork(earlier, 0); cp != nil {
				return earlier, cp, nil
			}
		}
		return nil, nil, fmt.Errorf("fork boundary not found for task %s", found.RunID)
	}
	for i := len(runs) - 1; i >= 0; i-- {
		if cp := d.checkpointForFork(runs[i], throughTurn); cp != nil {
			return runs[i], cp, nil
		}
	}
	return nil, nil, fmt.Errorf("cannot fork session %s without a completed task checkpoint", sessionID)
}

func (d *Daemon) checkpointForFork(run *scheduler.ExecutionRun, throughTurn int) *runCheckpoint {
	if d == nil || d.runs == nil || run == nil {
		return nil
	}
	if throughTurn > 0 {
		return d.runs.loadCheckpointTurn(run.RunID, throughTurn)
	}
	return d.runs.loadCheckpoint(run.RunID)
}

func (d *Daemon) sessionRunsChronological(sessionID string) []*scheduler.ExecutionRun {
	if d == nil || d.sched == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	var runs []*scheduler.ExecutionRun
	for _, task := range d.sched.List() {
		if task != nil && task.SessionID == sessionID {
			runs = append(runs, task)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].RunID < runs[j].RunID
		}
		return runs[i].CreatedAt.Before(runs[j].CreatedAt)
	})
	return runs
}

func (d *Daemon) forkInheritedTaskIDs(parentSessionID, throughTaskID string) map[string]bool {
	allowed := map[string]bool{}
	if strings.TrimSpace(throughTaskID) != "" {
		allowed[throughTaskID] = true
	}
	runs := d.sessionRunsChronological(parentSessionID)
	var through *scheduler.ExecutionRun
	for _, run := range runs {
		if run.RunID == throughTaskID {
			through = run
			break
		}
	}
	if through == nil {
		return allowed
	}
	for _, run := range runs {
		if run.RunID == through.RunID || runCreatedBefore(run, through) {
			allowed[run.RunID] = true
		}
	}
	return allowed
}

func (d *Daemon) inheritedForkEvents(parentID, throughTaskID string) ([]itemAuditEvent, error) {
	if d == nil || d.kern == nil || strings.TrimSpace(parentID) == "" {
		return nil, nil
	}
	raw, err := d.kern.ReadEvents(parentID)
	if err != nil {
		return nil, err
	}
	var events []itemAuditEvent
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, fmt.Errorf("session.items: decode inherited audit events: %w", err)
	}
	allowed := d.forkInheritedTaskIDs(parentID, throughTaskID)
	out := make([]itemAuditEvent, 0, len(events))
	for _, ev := range events {
		if ev.TaskID != "" && allowed[ev.TaskID] {
			out = append(out, ev)
		}
	}
	return out, nil
}

func runCreatedBefore(a, b *scheduler.ExecutionRun) bool {
	if a == nil || b == nil {
		return false
	}
	if a.CreatedAt.Before(b.CreatedAt) {
		return true
	}
	return a.CreatedAt.Equal(b.CreatedAt) && a.RunID < b.RunID
}
