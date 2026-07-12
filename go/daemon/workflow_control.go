package daemon

import (
	"context"
	"fmt"
	"sync"
)

// workflowRunControl is the live execution control plane for one RPC-started
// workflow run. Pausing only closes admission for new steps; work already in
// flight continues. Stopping cancels the shared run context, which reaches
// local subagents and remote dispatch waits through the normal task context.
type workflowRunControl struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	wake   chan struct{}

	mu     sync.Mutex
	paused bool
}

func newWorkflowRunControl() *workflowRunControl {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &workflowRunControl{ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1)}
}

func (c *workflowRunControl) setPaused(paused bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ctx.Err(); err != nil {
		return fmt.Errorf("workflow execution is no longer active: %w", err)
	}
	c.paused = paused
	if !paused {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

func (c *workflowRunControl) isPaused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

func (d *Daemon) createWorkflowControl(runID string) (*workflowRunControl, error) {
	d.workflowControlMu.Lock()
	defer d.workflowControlMu.Unlock()
	if _, exists := d.workflowControls[runID]; exists {
		return nil, fmt.Errorf("workflow %s already has an active execution", runID)
	}
	control := newWorkflowRunControl()
	d.workflowControls[runID] = control
	return control, nil
}

func (d *Daemon) workflowControl(runID string) *workflowRunControl {
	d.workflowControlMu.Lock()
	defer d.workflowControlMu.Unlock()
	return d.workflowControls[runID]
}

func (d *Daemon) removeWorkflowControl(runID string, control *workflowRunControl) {
	d.workflowControlMu.Lock()
	if d.workflowControls[runID] == control {
		delete(d.workflowControls, runID)
	}
	d.workflowControlMu.Unlock()
	control.cancel(context.Canceled)
}

func (d *Daemon) cancelWorkflowControls() {
	d.workflowControlMu.Lock()
	controls := make([]*workflowRunControl, 0, len(d.workflowControls))
	for _, control := range d.workflowControls {
		controls = append(controls, control)
	}
	d.workflowControlMu.Unlock()
	for _, control := range controls {
		control.cancel(context.Canceled)
	}
}
