package daemon

import (
	"context"
	"testing"
	"time"
)

func TestExecutionKeepalivePublishesOnTheBusWithoutAudit(t *testing.T) {
	d := &Daemon{events: NewBus()}
	sub := newFakeEventSub("keepalive")
	d.events.Subscribe("sess", sub)

	prev := executionKeepaliveAfter
	executionKeepaliveAfter = 15 * time.Millisecond
	defer func() { executionKeepaliveAfter = prev }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := d.startExecutionKeepalive(ctx, "sess", "run_1", "think")
	deadline := time.Now().Add(300 * time.Millisecond)
	var got map[string]any
	for time.Now().Before(deadline) {
		sub.mu.Lock()
		for _, event := range sub.events {
			if event["type"] == "execution.keepalive" {
				got = event
				break
			}
		}
		sub.mu.Unlock()
		if got != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if got == nil {
		t.Fatal("expected execution.keepalive on the bus")
	}
	if _, ok := got["event_id"]; ok {
		t.Fatalf("keepalive must not carry an audit event_id: %+v", got)
	}
	payload, _ := got["payload"].(map[string]any)
	if payload["status"] != "keepalive" || payload["stage"] != "think" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestThinkKeepaliveStartsFromContext(t *testing.T) {
	d := &Daemon{events: NewBus()}
	sub := newFakeEventSub("think-ka")
	d.events.Subscribe("sess", sub)
	prev := executionKeepaliveAfter
	executionKeepaliveAfter = 15 * time.Millisecond
	defer func() { executionKeepaliveAfter = prev }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = withExecutionKeepalive(ctx, d, "sess", "run_2")
	stop := startThinkKeepalive(ctx)
	deadline := time.Now().Add(300 * time.Millisecond)
	var found bool
	for time.Now().Before(deadline) {
		sub.mu.Lock()
		for _, event := range sub.events {
			if event["type"] == "execution.keepalive" {
				found = true
			}
		}
		sub.mu.Unlock()
		if found {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	if !found {
		t.Fatal("expected think keepalive from context")
	}
}
