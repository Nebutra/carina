package daemon

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/rpc"
)

type fakeEventSub struct {
	id           string
	done         chan struct{}
	mu           sync.Mutex
	events       []map[string]any
	failAfter    int
	disconnected bool
	block        chan struct{}
	entered      chan struct{}
}

type gatedEventSub struct {
	*fakeEventSub
	mu       sync.Mutex
	call     int
	barriers map[int]notifyBarrier
}

type notifyBarrier struct {
	entered chan struct{}
	release chan struct{}
}

type frameTraceSubscriber struct {
	*fakeEventSub
	mu     sync.Mutex
	frames []string
}

func (s *frameTraceSubscriber) TryNotify(method string, value any) error {
	event, _ := value.(map[string]any)
	s.mu.Lock()
	s.frames = append(s.frames, "event:"+event["n"].(string))
	s.mu.Unlock()
	return s.fakeEventSub.TryNotify(method, value)
}

func (s *frameTraceSubscriber) record(frame string) {
	s.mu.Lock()
	s.frames = append(s.frames, frame)
	s.mu.Unlock()
}

func newGatedEventSub(id string, calls ...int) *gatedEventSub {
	barriers := make(map[int]notifyBarrier, len(calls))
	for _, call := range calls {
		barriers[call] = notifyBarrier{entered: make(chan struct{}), release: make(chan struct{})}
	}
	return &gatedEventSub{fakeEventSub: newFakeEventSub(id), barriers: barriers}
}

func (s *gatedEventSub) TryNotify(method string, value any) error {
	s.mu.Lock()
	s.call++
	barrier, blocked := s.barriers[s.call]
	s.mu.Unlock()
	if blocked {
		close(barrier.entered)
		<-barrier.release
	}
	return s.fakeEventSub.TryNotify(method, value)
}

func newFakeEventSub(id string) *fakeEventSub {
	return &fakeEventSub{id: id, done: make(chan struct{}), failAfter: -1}
}
func (s *fakeEventSub) ID() string            { return s.id }
func (s *fakeEventSub) Done() <-chan struct{} { return s.done }
func (s *fakeEventSub) TryNotify(_ string, value any) error {
	if s.entered != nil {
		select {
		case s.entered <- struct{}{}:
		default:
			{
			}
		}
	}
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failAfter >= 0 && len(s.events) >= s.failAfter {
		return rpc.ErrSlowConsumer
	}
	if event, ok := value.(map[string]any); ok {
		s.events = append(s.events, event)
	}
	return nil
}
func (s *fakeEventSub) Disconnect() error {
	s.mu.Lock()
	s.disconnected = true
	s.mu.Unlock()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
	return nil
}

func TestBusSlowConsumerDoesNotBlockOrAffectOtherSession(t *testing.T) {
	b := NewBus()
	slow := newFakeEventSub("slow")
	slow.failAfter = 0
	fast := newFakeEventSub("fast")
	b.Subscribe("a", slow)
	b.Subscribe("b", fast)
	started := time.Now()
	for i := 0; i < 1000; i++ {
		b.Publish("a", map[string]any{"n": i})
	}
	if time.Since(started) > time.Second {
		t.Fatal("slow consumer blocked producer")
	}
	b.Publish("b", map[string]any{"n": 1})
	if len(fast.events) != 1 {
		t.Fatal("slow session affected other session")
	}
	stats := b.Stats()
	if stats.SlowDrops != 1 || stats.SlowDisconnects != 1 {
		t.Fatalf("overload not observable: %+v", stats)
	}
	if b.SubscriberCount() != 1 {
		t.Fatalf("slow subscriber not removed: %d", b.SubscriberCount())
	}
}

func TestBusCatchUpUsesRawCursorForOverlap(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("catchup")
	ready := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, _, err := b.SubscribeCatchUp("s", sub, func() ([]any, int, error) {
			close(ready)
			<-resume
			replayed := map[string]any{"type": "TaskCreated", "task_id": "t", "payload": map[string]any{"status": "running"}}
			return []any{replayed}, 1, nil
		})
		done <- err
	}()
	<-ready
	overlap := map[string]any{"type": "TaskCreated", "task_id": "t", "payload": map[string]any{"status": "running"}, internalRawAuditCursor: 1}
	b.Publish("s", overlap)
	// Equal payload at a distinct durable cursor is a distinct event. Payload
	// hashing used to drop this incorrectly.
	live := map[string]any{"type": "TaskCreated", "task_id": "t", "payload": map[string]any{"status": "running"}, internalRawAuditCursor: 2}
	b.Publish("s", live)
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(sub.events) != 2 {
		t.Fatalf("want replay+live exactly once, got %+v", sub.events)
	}
	if sub.events[0]["payload"].(map[string]any)["status"] != "running" || rawAuditCursor(sub.events[1]) != 2 {
		t.Fatalf("event order/gap: %+v", sub.events)
	}
}

func TestRawAuditCursorRejectsInvalidNumericRepresentations(t *testing.T) {
	valid := []any{int(3), int64(3), uint64(3), float64(3)}
	for _, value := range valid {
		if cursor := rawAuditCursor(map[string]any{internalRawAuditCursor: value}); cursor != 3 {
			t.Fatalf("valid cursor %T(%v) = %d", value, value, cursor)
		}
	}
	invalid := []any{
		int(0), int64(-1), ^uint64(0), 3.5,
		math.NaN(), math.Inf(1), math.Ldexp(1, 53), math.Ldexp(1, 63),
	}
	for _, value := range invalid {
		if cursor := rawAuditCursor(map[string]any{internalRawAuditCursor: value}); cursor != 0 {
			t.Fatalf("invalid cursor %T(%v) = %d", value, value, cursor)
		}
	}
}

func TestBusCatchUpKeepsCursorlessTransientAfterReplay(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("cursorless-tail")
	if _, _, _, err := b.SubscribeCatchUp("s", sub, func() ([]any, int, error) {
		return nil, 8, nil
	}); err != nil {
		t.Fatal(err)
	}

	b.Publish("s", map[string]any{"type": "assistant.message.delta", "payload": map[string]any{"delta": "tail"}})
	if len(sub.events) != 1 || sub.events[0]["type"] != "assistant.message.delta" {
		t.Fatalf("cursorless transient was filtered: %+v", sub.events)
	}
}

func TestBusConcurrentActivePublishesQueueInAdmissionOrder(t *testing.T) {
	b := NewBus()
	sub := newGatedEventSub("ordered-live", 1)
	b.Subscribe("s", sub)

	firstDone := make(chan struct{})
	go func() {
		b.Publish("s", map[string]any{"n": 1})
		close(firstDone)
	}()
	first := sub.barriers[1]
	<-first.entered

	secondDone := make(chan struct{})
	go func() {
		b.Publish("s", map[string]any{"n": 2})
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("later publisher blocked behind the active delivery owner")
	}

	close(first.release)
	<-firstDone
	sub.fakeEventSub.mu.Lock()
	defer sub.fakeEventSub.mu.Unlock()
	if len(sub.events) != 2 || sub.events[0]["n"] != 1 || sub.events[1]["n"] != 2 {
		t.Fatalf("active publish order = %+v", sub.events)
	}
}

func TestBusCatchUpDropsDelayedPublishAlreadyOwnedByReplay(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("delayed-publish")
	replayed := map[string]any{"type": "TaskCreated", "payload": map[string]any{"status": "running"}}
	if _, _, _, err := b.SubscribeCatchUp("s", sub, func() ([]any, int, error) {
		return []any{replayed}, 4, nil
	}); err != nil {
		t.Fatal(err)
	}

	// This append was included in the replay snapshot but its fan-out was
	// delayed until after activation. Cursor ownership, not payload equality,
	// prevents the duplicate.
	b.Publish("s", map[string]any{
		"type":                 "TaskCreated",
		"payload":              map[string]any{"status": "running"},
		internalRawAuditCursor: 4,
	})
	b.Publish("s", map[string]any{
		"type":                 "TaskCreated",
		"payload":              map[string]any{"status": "running"},
		internalRawAuditCursor: 5,
	})
	if len(sub.events) != 2 || rawAuditCursor(sub.events[1]) != 5 {
		t.Fatalf("replay-owned delayed publish was duplicated or live event lost: %+v", sub.events)
	}
}

func TestBusCatchUpDrainsPublishesDuringReplayAndPendingDeliveryInOrder(t *testing.T) {
	b := NewBus()
	sub := newGatedEventSub("ordered-catchup", 1, 2)
	done := make(chan error, 1)
	go func() {
		_, _, _, err := b.SubscribeCatchUp("s", sub, func() ([]any, int, error) {
			return []any{map[string]any{"n": 1}}, 1, nil
		})
		done <- err
	}()

	first := sub.barriers[1]
	<-first.entered
	b.Publish("s", map[string]any{"n": 2, internalRawAuditCursor: 2})
	close(first.release)

	second := sub.barriers[2]
	<-second.entered
	b.Publish("s", map[string]any{"n": 3, internalRawAuditCursor: 3})
	close(second.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	b.Publish("s", map[string]any{"n": 4, internalRawAuditCursor: 4})
	sub.fakeEventSub.mu.Lock()
	defer sub.fakeEventSub.mu.Unlock()
	if len(sub.events) != 4 {
		t.Fatalf("want replay+pending+pending-during-drain+live, got %+v", sub.events)
	}
	for index, event := range sub.events {
		if event["n"] != index+1 {
			t.Fatalf("event order at %d: %+v", index, sub.events)
		}
	}
}

func TestBusCatchUpCommitsAckBeforeConcurrentLiveDelivery(t *testing.T) {
	b := NewBus()
	sub := &frameTraceSubscriber{fakeEventSub: newFakeEventSub("commit-order")}
	commitEntered := make(chan struct{})
	publishStarted := make(chan struct{})
	publishDone := make(chan struct{})
	go func() {
		<-commitEntered
		close(publishStarted)
		b.Publish("s", map[string]any{"n": "live"})
		close(publishDone)
	}()

	_, _, _, err := b.SubscribeCatchUp("s", sub, func() ([]any, int, error) {
		return []any{map[string]any{"n": "replay"}}, 1, nil
	}, func(_ string, _, _ int) error {
		sub.record("ack")
		close(commitEntered)
		<-publishStarted
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-publishDone
	sub.mu.Lock()
	defer sub.mu.Unlock()
	want := []string{"event:replay", "ack", "event:live"}
	if len(sub.frames) != len(want) {
		t.Fatalf("frames = %v, want %v", sub.frames, want)
	}
	for index := range want {
		if sub.frames[index] != want[index] {
			t.Fatalf("frames = %v, want %v", sub.frames, want)
		}
	}
}

func TestBusCatchUpCommitFailureNeverActivatesSubscriber(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("commit-failure")
	_, _, _, err := b.SubscribeCatchUp("s", sub, func() ([]any, int, error) {
		return nil, 1, nil
	}, func(_ string, _, _ int) error {
		return rpc.ErrSlowConsumer
	})
	if !errors.Is(err, rpc.ErrSlowConsumer) {
		t.Fatalf("commit failure = %v, want ErrSlowConsumer", err)
	}
	if b.SubscriberCount() != 0 {
		t.Fatalf("failed commit activated subscriber: %d", b.SubscriberCount())
	}
	b.Publish("s", map[string]any{"n": 2})
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if !sub.disconnected || len(sub.events) != 0 {
		t.Fatalf("failed commit subscriber = disconnected:%v events:%+v", sub.disconnected, sub.events)
	}
}

func TestBusCatchUpPendingOverflowDisconnectsWithoutPartialReplay(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("overflow-catchup")
	replayStarted := make(chan struct{})
	releaseReplay := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, _, err := b.SubscribeCatchUp("s", sub, func() ([]any, int, error) {
			close(replayStarted)
			<-releaseReplay
			return []any{map[string]any{"n": "replayed"}}, 1, nil
		})
		done <- err
	}()

	<-replayStarted
	for index := 0; index <= busPendingLimit; index++ {
		b.Publish("s", map[string]any{"n": index})
	}
	if b.SubscriberCount() != 0 {
		t.Fatalf("overflowed catch-up subscriber still registered: %d", b.SubscriberCount())
	}
	sub.mu.Lock()
	disconnected := sub.disconnected
	sub.mu.Unlock()
	if !disconnected {
		t.Fatal("overflowed catch-up subscriber was not disconnected")
	}
	stats := b.Stats()
	if stats.SlowDrops != 1 || stats.SlowDisconnects != 1 {
		t.Fatalf("catch-up overflow not observable: %+v", stats)
	}

	close(releaseReplay)
	if err := <-done; err == nil {
		t.Fatal("catch-up succeeded after its pending queue overflowed")
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if len(sub.events) != 0 {
		t.Fatalf("partial replay escaped after overflow: %+v", sub.events)
	}
}

func TestBusUnsubscribeAckStopsDeliveryAndDisconnectCleansImmediately(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("x")
	b.Subscribe("s", sub)
	if !b.Unsubscribe("x") {
		t.Fatal("unsubscribe was not acknowledged")
	}
	b.Publish("s", map[string]any{"n": 1})
	if len(sub.events) != 0 {
		t.Fatal("event delivered after unsubscribe ack")
	}
	sub2 := newFakeEventSub("y")
	b.Subscribe("s", sub2)
	close(sub2.done)
	deadline := time.Now().Add(time.Second)
	for b.SubscriberCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if b.SubscriberCount() != 0 {
		t.Fatal("disconnect leaked subscription until next publish")
	}
}

func TestBusUnsubscribeAckWaitsForInFlightDelivery(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("ordered")
	sub.block = make(chan struct{})
	sub.entered = make(chan struct{}, 1)
	b.Subscribe("s", sub)
	published := make(chan struct{})
	go func() { b.Publish("s", map[string]any{"n": 1}); close(published) }()
	<-sub.entered
	acked := make(chan bool, 1)
	go func() { acked <- b.Unsubscribe("ordered") }()
	select {
	case <-acked:
		t.Fatal("unsubscribe ack overtook in-flight callback")
	case <-time.After(20 * time.Millisecond):
	}
	close(sub.block)
	<-published
	if !<-acked {
		t.Fatal("unsubscribe not acknowledged")
	}
	b.Publish("s", map[string]any{"n": 2})
	if len(sub.events) != 1 {
		t.Fatalf("callback after unsubscribe ACK: %+v", sub.events)
	}
}
