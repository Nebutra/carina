package daemon

import (
	"testing"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/scheduler"
)

func TestRuntimeIdleAttachmentCancelsAndRestartsDeadline(t *testing.T) {
	spec, d := runtimeIdentityFixture(t)
	spec.IdleGraceMS = 50
	d.runtimeSpec = &spec
	d.runtimeLifecycle = localruntime.LifecycleRunning
	d.initializeRuntimeIdle()
	_, _, deadline := d.runtimeIdleSnapshot()
	if deadline == nil {
		t.Fatal("initial idle deadline missing")
	}
	d.ConnectionOpened(rpc.OriginLocal)
	connections, _, deadline := d.runtimeIdleSnapshot()
	if connections != 1 || deadline != nil {
		t.Fatalf("attached snapshot connections=%d deadline=%v", connections, deadline)
	}
	d.ConnectionClosed(rpc.OriginLocal)
	connections, _, deadline = d.runtimeIdleSnapshot()
	if connections != 0 || deadline == nil {
		t.Fatalf("detached snapshot connections=%d deadline=%v", connections, deadline)
	}
	d.stopRuntimeIdleTimer()
}

func TestRuntimeIdleWaitsForTaskObligationThenStops(t *testing.T) {
	spec, d := runtimeIdentityFixture(t)
	spec.IdleGraceMS = 20
	d.runtimeSpec = &spec
	d.runtimeLifecycle = localruntime.LifecycleRunning
	d.sched = scheduler.New()
	task := d.sched.Submit("session", spec.Workspace.ID, "work")
	stopped := make(chan struct{}, 1)
	d.runtimeIdleStop = func() { stopped <- struct{}{} }
	d.initializeRuntimeIdle()
	select {
	case <-stopped:
		t.Fatal("runtime stopped with queued task")
	case <-time.After(60 * time.Millisecond):
	}
	d.sched.SetStatus(task.TaskID, "completed")
	select {
	case <-stopped:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("runtime did not stop after obligation cleared")
	}
}
