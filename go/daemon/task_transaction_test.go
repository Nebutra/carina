package daemon

import (
	"testing"
	"time"
)

func TestCancelAndRemoveShareCheckpointTaskTransactionLock(t *testing.T) {
	d := newDaemonAt(t, t.TempDir())
	defer d.Close()
	workspace := t.TempDir()
	sess, err := d.store.CreateSession(workspace, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}

	cancelTask := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "cancel")
	d.checkpointMu.Lock()
	cancelDone := make(chan error, 1)
	go func() {
		_, err := d.handleTaskCancel(mustJSON(t, map[string]any{"task_id": cancelTask.TaskID}))
		cancelDone <- err
	}()
	select {
	case err := <-cancelDone:
		d.checkpointMu.Unlock()
		t.Fatalf("cancel crossed task transaction lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	d.checkpointMu.Unlock()
	select {
	case err := <-cancelDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not continue after transaction lock release")
	}

	removeTask := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "remove")
	d.sched.SetStatus(removeTask.TaskID, "completed")
	current, _ := d.sched.Get(removeTask.TaskID)
	if err := d.runs.saveChecked(current); err != nil {
		t.Fatal(err)
	}
	d.checkpointMu.Lock()
	removeDone := make(chan error, 1)
	go func() {
		_, err := d.handleAgentRemove(mustJSON(t, map[string]any{"task_id": removeTask.TaskID}))
		removeDone <- err
	}()
	select {
	case err := <-removeDone:
		d.checkpointMu.Unlock()
		t.Fatalf("remove crossed task transaction lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	d.checkpointMu.Unlock()
	select {
	case err := <-removeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("remove did not continue after transaction lock release")
	}

	resultAny, err := d.handleAgentRemove(mustJSON(t, map[string]any{"task_id": removeTask.TaskID}))
	if err != nil {
		t.Fatalf("idempotent remove retry: %v", err)
	}
	result := resultAny.(map[string]any)
	if result["removed"] != true || result["idempotent"] != true {
		t.Fatalf("idempotent remove result = %+v", result)
	}

	pendingTask := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "remove after tombstone crash")
	d.sched.SetStatus(pendingTask.TaskID, "completed")
	pendingCurrent, _ := d.sched.Get(pendingTask.TaskID)
	if err := d.runs.saveChecked(pendingCurrent); err != nil {
		t.Fatal(err)
	}
	if err := d.runs.tombstone(pendingTask.TaskID); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.sched.Get(pendingTask.TaskID); !ok {
		t.Fatal("crash-window setup must leave scheduler publication pending")
	}
	if _, err := d.handleAgentRemove(mustJSON(t, map[string]any{"task_id": pendingTask.TaskID})); err != nil {
		t.Fatalf("remove retry after durable tombstone: %v", err)
	}
	if _, ok := d.sched.Get(pendingTask.TaskID); ok {
		t.Fatal("remove retry did not publish scheduler removal")
	}
}
