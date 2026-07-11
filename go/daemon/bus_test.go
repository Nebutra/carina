package daemon

import (
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

func TestBusCatchUpIsGapFreeAndDeduplicatesOverlap(t *testing.T) {
	b := NewBus()
	sub := newFakeEventSub("catchup")
	ready := make(chan struct{})
	resume := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, _, err := b.SubscribeCatchUp("s", sub, func() ([]any, int, map[string]int, error) {
			close(ready)
			<-resume
			replayed := map[string]any{"type": "TaskCreated", "task_id": "t", "payload": map[string]any{"status": "running"}}
			return []any{replayed}, 1, map[string]int{eventKey(replayed): 1}, nil
		})
		done <- err
	}()
	<-ready
	overlap := map[string]any{"type": "TaskCreated", "task_id": "t", "payload": map[string]any{"status": "running"}}
	b.Publish("s", overlap)
	live := map[string]any{"type": "TaskCreated", "task_id": "t", "payload": map[string]any{"status": "completed"}}
	b.Publish("s", live)
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(sub.events) != 2 {
		t.Fatalf("want replay+live exactly once, got %+v", sub.events)
	}
	if sub.events[0]["payload"].(map[string]any)["status"] != "running" || sub.events[1]["payload"].(map[string]any)["status"] != "completed" {
		t.Fatalf("event order/gap: %+v", sub.events)
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
