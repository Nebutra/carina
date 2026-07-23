package daemon

import (
	"sort"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/workflowui"
)

func (d *Daemon) initializeRuntimeIdle() {
	if d.runtimeSpec == nil || d.runtimeSpec.Mode != localruntime.ModeWorkspace || d.runtimeSpec.IdleGraceMS <= 0 {
		return
	}
	d.runtimeIdleMu.Lock()
	d.runtimeIdleGrace = time.Duration(d.runtimeSpec.IdleGraceMS) * time.Millisecond
	d.scheduleRuntimeIdleLocked()
	d.runtimeIdleMu.Unlock()
	lifecycle, socketPath := d.runtimePublishState()
	_ = d.publishRuntimeDescriptor(lifecycle, socketPath)
}

func (d *Daemon) ConnectionOpened(_ rpc.Origin) {
	d.runtimeIdleMu.Lock()
	d.runtimeConnections++
	if d.runtimeIdleTimer != nil {
		d.runtimeIdleTimer.Stop()
		d.runtimeIdleTimer = nil
	}
	d.runtimeIdleDeadline = nil
	d.runtimeIdleMu.Unlock()
	lifecycle, socketPath := d.runtimePublishState()
	_ = d.publishRuntimeDescriptor(lifecycle, socketPath)
}

func (d *Daemon) ConnectionClosed(_ rpc.Origin) {
	d.runtimeIdleMu.Lock()
	if d.runtimeConnections > 0 {
		d.runtimeConnections--
	}
	if d.runtimeConnections == 0 && !d.runtimeIdleStopping {
		d.scheduleRuntimeIdleLocked()
	}
	d.runtimeIdleMu.Unlock()
	lifecycle, socketPath := d.runtimePublishState()
	_ = d.publishRuntimeDescriptor(lifecycle, socketPath)
}

func (d *Daemon) scheduleRuntimeIdleLocked() {
	if d.runtimeIdleGrace <= 0 || d.runtimeIdleStopping || d.runtimeConnections != 0 {
		return
	}
	if d.runtimeIdleTimer != nil {
		d.runtimeIdleTimer.Stop()
	}
	deadline := time.Now().UTC().Add(d.runtimeIdleGrace)
	d.runtimeIdleDeadline = &deadline
	d.runtimeIdleTimer = time.AfterFunc(d.runtimeIdleGrace, d.runtimeIdleExpired)
}

func (d *Daemon) runtimeIdleExpired() {
	obligations := d.runtimeObligationNames()
	d.runtimeIdleMu.Lock()
	if d.runtimeConnections != 0 || d.runtimeIdleStopping {
		d.runtimeIdleMu.Unlock()
		return
	}
	if len(obligations) > 0 {
		d.scheduleRuntimeIdleLocked()
		d.runtimeIdleMu.Unlock()
		lifecycle, socketPath := d.runtimePublishState()
		_ = d.publishRuntimeDescriptor(lifecycle, socketPath)
		return
	}
	d.runtimeIdleStopping = true
	d.runtimeIdleDeadline = nil
	d.runtimeIdleTimer = nil
	d.runtimeIdleMu.Unlock()
	if d.runtimeIdleStop != nil {
		go d.runtimeIdleStop()
		return
	}
	go func() { _ = d.Close() }()
}

func (d *Daemon) stopRuntimeIdleTimer() {
	d.runtimeIdleMu.Lock()
	d.runtimeIdleStopping = true
	if d.runtimeIdleTimer != nil {
		d.runtimeIdleTimer.Stop()
		d.runtimeIdleTimer = nil
	}
	d.runtimeIdleDeadline = nil
	d.runtimeIdleMu.Unlock()
}

func (d *Daemon) runtimeIdleSnapshot() (int, []string, *time.Time) {
	d.runtimeIdleMu.Lock()
	connections := d.runtimeConnections
	var deadline *time.Time
	if d.runtimeIdleDeadline != nil {
		copy := *d.runtimeIdleDeadline
		deadline = &copy
	}
	d.runtimeIdleMu.Unlock()
	return connections, d.runtimeObligationNames(), deadline
}

func (d *Daemon) runtimeObligationNames() []string {
	set := map[string]bool{}
	if d.sched != nil {
		for _, task := range d.sched.List() {
			switch task.Status {
			case "queued", "running", "waiting_input", "waiting_approval", "paused", "interrupted":
				set["task:"+task.Status] = true
			}
		}
	}
	if d.schedules != nil {
		for _, schedule := range d.schedules.List() {
			if schedule.Enabled {
				set["schedule:enabled"] = true
				break
			}
		}
	}
	if d.workflowRuns != nil {
		for _, run := range d.workflowRuns.List() {
			switch run.Status {
			case workflowui.Queued, workflowui.Running, workflowui.Paused:
				set["workflow:"+string(run.Status)] = true
			}
		}
	}
	if len(d.gatewayHTTPServers) > 0 {
		set["gateway:http"] = true
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
