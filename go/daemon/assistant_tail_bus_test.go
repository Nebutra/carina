package daemon

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/rpc"
)

func TestEventStreamCatchUpEmitsRevisionOrderedSnapshots(t *testing.T) {
	b := NewBus()
	for _, runID := range []string{"z", "a", "m"} {
		var owner assistantTailOwner
		if err := b.PublishAssistantTail(&owner, "s", runID, "reset", 1, 1, "", false); err != nil {
			t.Fatal(err)
		}
		if err := b.PublishAssistantTail(&owner, "s", runID, "delta", 1, 2, runID, false); err != nil {
			t.Fatal(err)
		}
	}
	inner := newFakeEventSub("snaps")
	result, err := b.subscribeCatchUp("s", inner, replayOnly(nil, 3), catchUpOptions{emitSnapshots: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.transientSnapshots != 3 || result.transientTailRevision != 6 || result.bufferedLive != 0 {
		t.Fatalf("catch-up counts = %+v", result)
	}
	if len(inner.events) != 3 {
		t.Fatalf("frames = %+v", inner.events)
	}
	want := []string{"z", "a", "m"}
	var lastRevision int
	for index, event := range inner.events {
		if event["type"] != assistantMessageSnapshotType || event["run_id"] != want[index] {
			t.Fatalf("snapshot[%d] = %+v", index, event)
		}
		payload := event["payload"].(map[string]any)
		revision := payload["tail_revision"].(int)
		if revision <= lastRevision || revision > result.transientTailRevision {
			t.Fatalf("revision order/cut = %d after %d cut %d", revision, lastRevision, result.transientTailRevision)
		}
		lastRevision = revision
		if payload["content"] != want[index] || payload["state"] != assistantTailStateOpen {
			t.Fatalf("snapshot payload = %+v", payload)
		}
	}
}

func TestCompletionRaceReplaySealDropsCapturedTail(t *testing.T) {
	b := NewBus()
	var owner assistantTailOwner
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "delta", 1, 2, "stale", false); err != nil {
		t.Fatal(err)
	}
	inner := newFakeEventSub("sealed-snap")
	ready := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan catchUpResult, 1)
	go func() {
		result, err := b.subscribeCatchUp("s", inner, func() (catchUpReplay, error) {
			close(ready)
			<-resume
			return catchUpReplay{
				events: []any{map[string]any{
					"type": "ModelResponded", "task_id": "run_1",
					"payload":              map[string]any{"text": "final"},
					internalRawAuditCursor: 4,
				}},
				durableCursor: 4,
				sealed:        []sealedRunPhase{{RunID: "run_1", Phase: assistantPhaseFinalAnswer}},
			}, nil
		}, catchUpOptions{emitSnapshots: true})
		if err != nil {
			t.Errorf("catch-up: %v", err)
		}
		done <- result
	}()
	<-ready
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "delta", 1, 3, "queued-before-final", false); err != nil {
		t.Fatal(err)
	}
	b.Publish("s", map[string]any{
		"type": "assistant.message.delta", "task_id": "run_1",
		"payload": map[string]any{"delta": "pending-stale", "phase": assistantPhaseFinalAnswer},
	})
	close(resume)
	result := <-done
	if result.transientSnapshots != 0 {
		t.Fatalf("replayed seal leaked snapshot: %+v frames=%+v", result, inner.events)
	}
	for _, event := range inner.events {
		if event["type"] == assistantMessageSnapshotType || isTransientAssistantEvent(event) {
			t.Fatalf("tail followed a replayed seal: %+v", inner.events)
		}
	}
}

func TestCompletionRaceSnapshotThenCanonicalFinalAfterWatermark(t *testing.T) {
	b := NewBus()
	var owner assistantTailOwner
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "delta", 1, 2, "hel", false); err != nil {
		t.Fatal(err)
	}
	inner := newFakeEventSub("open-tail")
	ready := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := b.subscribeCatchUp("s", inner, func() (catchUpReplay, error) {
			close(ready)
			<-resume
			return catchUpReplay{durableCursor: 1}, nil
		}, catchUpOptions{emitSnapshots: true})
		done <- err
	}()
	<-ready
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "delta", 1, 3, "lo", false); err != nil {
		t.Fatal(err)
	}
	b.Publish("s", map[string]any{
		"type":                 "ModelResponded",
		"task_id":              "run_1",
		"payload":              map[string]any{"text": "hello"},
		internalRawAuditCursor: 2,
	})
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(inner.events) < 3 {
		t.Fatalf("frames = %+v", inner.events)
	}
	if inner.events[0]["type"] != assistantMessageSnapshotType {
		t.Fatalf("first frame = %+v", inner.events[0])
	}
	if inner.events[1]["type"] != "assistant.message.delta" {
		t.Fatalf("pending tail = %+v", inner.events[1])
	}
	if inner.events[2]["type"] != "ModelResponded" {
		t.Fatalf("canonical final = %+v", inner.events[2])
	}
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "reset", 2, 1, "", false); !errors.Is(err, errAssistantTailSealed) {
		t.Fatalf("resurrection after final = %v", err)
	}
}

func TestCompletionRaceDelayedFinalPublishAfterReplay(t *testing.T) {
	b := NewBus()
	var owner assistantTailOwner
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	inner := newFakeEventSub("delayed-final")
	result, err := b.subscribeCatchUp("s", inner, replayOnly([]any{
		map[string]any{
			"type": "ModelResponded", "task_id": "run_1",
			"payload":              map[string]any{"text": "done"},
			internalRawAuditCursor: 4,
		},
	}, 4, sealedRunPhase{RunID: "run_1", Phase: assistantPhaseFinalAnswer}), catchUpOptions{emitSnapshots: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.transientSnapshots != 0 {
		t.Fatalf("sealed snapshot escaped: %+v", result)
	}
	b.Publish("s", map[string]any{
		"type":                 "ModelResponded",
		"task_id":              "run_1",
		"payload":              map[string]any{"text": "done"},
		internalRawAuditCursor: 4,
	})
	if len(inner.events) != 1 || inner.events[0]["type"] != "ModelResponded" {
		t.Fatalf("delayed final duplicated or tail leaked: %+v", inner.events)
	}
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "delta", 1, 2, "late", false); !errors.Is(err, errAssistantTailSealed) {
		t.Fatalf("delayed final did not seal tail: %v", err)
	}
}

func TestReplayTailDisconnectDuringDeltaReconnectSealsFinalOnce(t *testing.T) {
	b := NewBus()
	var owner assistantTailOwner
	first := newFakeEventSub("first-live")
	if _, err := b.subscribeCatchUp("s", first, replayOnly(nil, 1), catchUpOptions{emitSnapshots: true}); err != nil {
		t.Fatal(err)
	}
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "delta", 1, 2, "Hel", false); err != nil {
		t.Fatal(err)
	}
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "delta", 1, 3, "lo", false); err != nil {
		t.Fatal(err)
	}
	if got := assistantBodiesFromFrames(first.events); len(got) == 0 {
		t.Fatalf("first subscriber saw no public tail: %+v", first.events)
	}
	close(first.done)
	_ = b.Unsubscribe(first.id)

	second := newGatedEventSub("reconnect", 1)
	ready := make(chan struct{})
	done := make(chan catchUpResult, 1)
	go func() {
		result, err := b.subscribeCatchUp("s", second, func() (catchUpReplay, error) {
			close(ready)
			return catchUpReplay{durableCursor: 1}, nil
		}, catchUpOptions{emitSnapshots: true})
		if err != nil {
			t.Errorf("reconnect catch-up: %v", err)
		}
		done <- result
	}()
	<-ready
	<-second.barriers[1].entered
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "delta", 1, 4, ", world", false); err != nil {
		t.Fatal(err)
	}
	b.Publish("s", map[string]any{
		"type":                 "ModelResponded",
		"task_id":              "run_1",
		"run_id":               "run_1",
		"payload":              map[string]any{"text": "Hello, world"},
		internalRawAuditCursor: 2,
	})
	close(second.barriers[1].release)
	result := <-done
	if result.transientSnapshots != 1 {
		t.Fatalf("reconnect snapshots = %+v frames=%+v", result, second.events)
	}
	frames := second.events
	var finals []string
	var snapshots []string
	for _, event := range frames {
		switch event["type"] {
		case assistantMessageSnapshotType:
			payload, _ := event["payload"].(map[string]any)
			content, _ := payload["content"].(string)
			if content != "" {
				snapshots = append(snapshots, content)
			}
		case "ModelResponded":
			payload, _ := event["payload"].(map[string]any)
			text, _ := payload["text"].(string)
			if text != "" {
				finals = append(finals, text)
			}
		}
	}
	if len(finals) != 1 || finals[0] != "Hello, world" {
		t.Fatalf("canonical final = %v frames=%+v", finals, frames)
	}
	if len(snapshots) != 1 || snapshots[0] != "Hello" {
		t.Fatalf("captured tail at subscribe cut = %v, want [Hello]; frames=%+v", snapshots, frames)
	}
	if err := b.PublishAssistantTail(&owner, "s", "run_1", "delta", 1, 5, "resurrect", false); !errors.Is(err, errAssistantTailSealed) {
		t.Fatalf("final did not seal the tail: %v", err)
	}
}

func assistantBodiesFromFrames(events []map[string]any) []string {
	var bodies []string
	for _, event := range events {
		payload, _ := event["payload"].(map[string]any)
		switch event["type"] {
		case "assistant.message.delta":
			if delta, _ := payload["delta"].(string); delta != "" {
				bodies = append(bodies, delta)
			}
		case assistantMessageSnapshotType, "assistant.message.completed":
			if content, _ := payload["content"].(string); content != "" {
				bodies = append(bodies, content)
			}
		}
	}
	return bodies
}

func TestAssistantTailOverflowDisconnectsWithoutTruncation(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("overflow")
	b.Subscribe("s", sub)
	var owner assistantTailOwner
	if err := b.PublishAssistantTail(&owner, "s", "run", "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	tooLarge := strings.Repeat("x", maxProviderResponseBytes+1)
	if err := b.PublishAssistantTail(&owner, "s", "run", "delta", 1, 2, tooLarge, false); !errors.Is(err, errAssistantTailOverflow) {
		t.Fatalf("overflow = %v", err)
	}
	if b.SubscriberCount() != 0 {
		t.Fatalf("overflow left subscribers: %d", b.SubscriberCount())
	}
	if !sub.disconnected {
		t.Fatal("overflow did not force reconnect")
	}
	snapshots, _ := b.captureTails("s")
	if len(snapshots) != 0 {
		t.Fatalf("overflow kept a truncated tail: %+v", snapshots)
	}
	if b.Stats().TailOverflows != 1 {
		t.Fatalf("overflow was not observable: %+v", b.Stats())
	}
}

func TestAssistantTailDoesNotSurviveNewBus(t *testing.T) {
	b := NewBus()
	var owner assistantTailOwner
	if err := b.PublishAssistantTail(&owner, "s", "run", "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	if snaps, _ := b.captureTails("s"); len(snaps) != 1 {
		t.Fatalf("seed tail missing: %+v", snaps)
	}
	replaced := NewBus()
	if snaps, rev := replaced.captureTails("s"); len(snaps) != 0 || rev != 0 {
		t.Fatalf("restarted bus retained tail: snaps=%+v rev=%d", snaps, rev)
	}
}

func TestAssistantTailSnapshotsExcludePrivateActionJSON(t *testing.T) {
	var decoder actionEnvelopeStreamDecoder
	d := &Daemon{events: NewBus()}
	publisher := &assistantStreamPublisher{d: d, sessionID: "s", taskID: "run"}
	leaked := decoder.Push(`{"thought":"secret","tool":"patch","content":"SECRET_PATCH"}`)
	if leaked != "" {
		t.Fatalf("tool JSON leaked from decoder: %q", leaked)
	}
	publisher.publish(ReasonerStreamUpdate{Generation: 1, Reset: true})
	publisher.publish(ReasonerStreamUpdate{Generation: 1, Text: leaked})
	decoder.Reset()
	public := decoder.Push(`{"thought":"secret","tool":"done","summary":"hello"}`)
	if public != "hello" {
		t.Fatalf("public summary = %q", public)
	}
	publisher.publish(ReasonerStreamUpdate{Generation: 1, Text: public})
	snapshots, _ := d.events.captureTails("s")
	if len(snapshots) != 1 || snapshots[0].Content != "hello" {
		t.Fatalf("snapshot = %+v", snapshots)
	}
	if strings.Contains(snapshots[0].Content, "SECRET") || strings.Contains(snapshots[0].Content, "thought") {
		t.Fatalf("private text entered snapshot: %+v", snapshots[0])
	}
}

func TestAssistantTailPublishRejectsSealedRun(t *testing.T) {
	b := NewBus()
	var owner assistantTailOwner
	if err := b.PublishAssistantTail(&owner, "s", "run", "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	b.Publish("s", map[string]any{
		"type":    "ExecutionCompleted",
		"task_id": "run",
		"payload": map[string]any{"summary": "done"},
	})
	if err := b.PublishAssistantTail(&owner, "s", "run", "delta", 1, 2, "late", false); !errors.Is(err, errAssistantTailSealed) {
		t.Fatalf("terminal event did not seal tail: %v", err)
	}
}

func TestAssistantTailOverflowDoesNotUseSlowConsumerPath(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("x")
	b.Subscribe("s", sub)
	var owner assistantTailOwner
	if err := b.PublishAssistantTail(&owner, "s", "run", "reset", 1, 1, "", false); err != nil {
		t.Fatal(err)
	}
	_ = b.PublishAssistantTail(&owner, "s", "run", "delta", 1, 2, strings.Repeat("x", maxProviderResponseBytes+1), false)
	stats := b.Stats()
	if stats.SlowDrops != 0 || stats.SlowDisconnects != 0 || stats.TailOverflows != 1 {
		t.Fatalf("overflow used the wrong metric: %+v", stats)
	}
	if errors.Is(rpc.ErrSlowConsumer, errAssistantTailOverflow) {
		t.Fatal("overflow aliased as slow consumer")
	}
}
