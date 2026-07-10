package daemon

import "testing"

func TestDebugTraceDisabledByDefaultAndRingBounded(t *testing.T) {
	d := &Daemon{debugTrace: newDebugTrace(3)}
	d.emitDebug("scheduler", "disabled_event", "task-0", nil)
	if d.debugTrace.stats(false)["events"].(int) != 0 {
		t.Fatal("debug trace should not collect while debug RPC is disabled")
	}
	if _, err := d.handleDebugSnapshot(nil); err == nil {
		t.Fatal("debug.snapshot should be disabled by default")
	}

	d.debugRPCEnabled.Store(true)
	d.emitDebug("scheduler", "submit", "task-1", map[string]string{"status": "queued"})
	d.emitDebug("worker", "lease", "task-1", map[string]string{"worker_id": "w1"})
	d.emitDebug("worker", "report", "task-1", map[string]string{"status": "completed"})
	d.emitDebug("scheduler", "submit", "task-2", map[string]string{"status": "queued"})

	snap, err := d.handleDebugSnapshot(mustJSON(t, map[string]any{"limit": 10}))
	if err != nil {
		t.Fatalf("debug.snapshot: %v", err)
	}
	m := snap.(map[string]any)
	if m["enabled"] != true {
		t.Fatalf("snapshot should report enabled: %+v", m)
	}
	if m["dropped"].(uint64) != 1 {
		t.Fatalf("bounded ring should report one overwritten event: %+v", m)
	}
	events := m["events"].([]DebugTraceEvent)
	if len(events) != 3 {
		t.Fatalf("snapshot should be capped by ring capacity, got %d: %+v", len(events), events)
	}
	if events[0].CorrelationID != "task-2" || events[0].Seq <= events[1].Seq {
		t.Fatalf("snapshot should be newest-first: %+v", events)
	}

	corr, err := d.handleDebugCorrelation(mustJSON(t, map[string]any{"correlation_id": "task-1", "limit": 10}))
	if err != nil {
		t.Fatalf("debug.correlation.search: %v", err)
	}
	cm := corr.(map[string]any)
	cEvents := cm["events"].([]DebugTraceEvent)
	if len(cEvents) != 2 {
		t.Fatalf("correlation search should only return surviving task-1 events, got %+v", cEvents)
	}
	for _, ev := range cEvents {
		if ev.CorrelationID != "task-1" {
			t.Fatalf("unexpected correlation event: %+v", ev)
		}
	}
}
