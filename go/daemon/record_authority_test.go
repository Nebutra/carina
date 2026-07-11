package daemon

import "testing"

func TestRecordDoesNotPublishWhenAuditAppendFails(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()

	sess, err := d.store.CreateSession(ws, "safe-edit")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil); err != nil {
		t.Fatal(err)
	}
	sub := newFakeEventSub("record-authority")
	d.events.Subscribe(sess.SessionID, sub)

	_ = d.Kernel().Close()
	d.record(sess.SessionID, "TaskCreated", "task-unrecorded", "go", map[string]any{"status": "running"}, "")

	if len(sub.events) != 0 {
		t.Fatalf("live stream published an event rejected by the audit writer: %+v", sub.events)
	}
	if got := d.events.Stats().Published; got != 0 {
		t.Fatalf("bus publish count = %d, want 0 after failed audit append", got)
	}
}
