package daemon

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/Nebutra/carina/go/rpc"
)

const busPendingLimit = 256

type busSubscriber struct {
	id        string
	sub       eventSubscriber
	active    bool
	pending   []map[string]any
	deliverMu sync.Mutex
	closed    bool
}

type eventSubscriber interface {
	ID() string
	Done() <-chan struct{}
	TryNotify(string, any) error
	Disconnect() error
}

type BusStats struct {
	Published       uint64 `json:"published"`
	SlowDrops       uint64 `json:"slow_consumer_drops"`
	SlowDisconnects uint64 `json:"slow_consumer_disconnects"`
}

// Bus fans events out without ever waiting on network consumers. A consumer
// that fills its bounded connection queue is disconnected and must catch up by
// cursor. This preserves global publisher health and makes overload explicit.
type Bus struct {
	mu              sync.RWMutex
	subs            map[string]map[string]*busSubscriber
	taps            []func(sessionID string, event map[string]any)
	published       atomic.Uint64
	slowDrops       atomic.Uint64
	slowDisconnects atomic.Uint64
}

func NewBus() *Bus { return &Bus{subs: make(map[string]map[string]*busSubscriber)} }

func (b *Bus) Subscribe(sessionID string, sub eventSubscriber) string {
	return b.add(sessionID, sub, true)
}

// SubscribeCatchUp registers inactive before replay starts. Events published
// during replay are buffered and flushed after replay, so no event can fall in
// the read-then-subscribe gap.
func (b *Bus) SubscribeCatchUp(sessionID string, sub eventSubscriber, replay func() ([]any, int, map[string]int, error)) (string, int, int, error) {
	id := b.add(sessionID, sub, false)
	events, cursor, overlap, err := replay()
	if err != nil {
		b.Unsubscribe(id)
		return "", 0, 0, err
	}
	for _, event := range events {
		if err := sub.TryNotify("event", event); err != nil {
			b.dropSlow(id, sub, err)
			return "", 0, 0, err
		}
	}
	b.mu.Lock()
	entry := b.lookupLocked(id)
	if entry == nil {
		b.mu.Unlock()
		return "", 0, 0, errors.New("subscription closed during catch-up")
	}
	pending := append([]map[string]any(nil), entry.pending...)
	entry.pending = nil
	entry.active = true
	b.mu.Unlock()
	for _, event := range pending {
		if key := eventKey(event); overlap[key] > 0 {
			overlap[key]--
			continue
		}
		if err := sub.TryNotify("event", event); err != nil {
			b.dropSlow(id, sub, err)
			return "", 0, 0, err
		}
	}
	return id, cursor, len(events), nil
}

func eventKey(event any) string {
	raw, _ := json.Marshal(event)
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	key, _ := json.Marshal(map[string]any{"type": value["type"], "task_id": value["task_id"], "payload": value["payload"]})
	return string(key)
}

func (b *Bus) add(sessionID string, sub eventSubscriber, active bool) string {
	id := sub.ID()
	if id == "" {
		id = sessionID + ":legacy"
	}
	b.mu.Lock()
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = make(map[string]*busSubscriber)
	}
	b.subs[sessionID][id] = &busSubscriber{id: id, sub: sub, active: active}
	b.mu.Unlock()
	go func() { <-sub.Done(); b.Unsubscribe(id) }()
	return id
}

func (b *Bus) Unsubscribe(id string) bool {
	b.mu.Lock()
	for sessionID, entries := range b.subs {
		if entry, ok := entries[id]; ok {
			delete(entries, id)
			if len(entries) == 0 {
				delete(b.subs, sessionID)
			}
			b.mu.Unlock()
			entry.deliverMu.Lock()
			entry.closed = true
			entry.deliverMu.Unlock()
			return true
		}
	}
	b.mu.Unlock()
	return false
}

func (b *Bus) lookupLocked(id string) *busSubscriber {
	for _, entries := range b.subs {
		if entry := entries[id]; entry != nil {
			return entry
		}
	}
	return nil
}

func (b *Bus) Tap(fn func(sessionID string, event map[string]any)) {
	b.mu.Lock()
	b.taps = append(b.taps, fn)
	b.mu.Unlock()
}

func (b *Bus) Publish(sessionID string, event map[string]any) {
	b.published.Add(1)
	b.mu.RLock()
	taps := append([]func(string, map[string]any){}, b.taps...)
	b.mu.RUnlock()
	for _, tap := range taps {
		tap(sessionID, event)
	}
	b.mu.Lock()
	entries := b.subs[sessionID]
	active := make([]*busSubscriber, 0, len(entries))
	overflow := make([]*busSubscriber, 0)
	for _, entry := range entries {
		if entry.active {
			active = append(active, entry)
			continue
		}
		if len(entry.pending) >= busPendingLimit {
			overflow = append(overflow, entry)
			delete(entries, entry.id)
			continue
		}
		entry.pending = append(entry.pending, event)
	}
	if len(entries) == 0 {
		delete(b.subs, sessionID)
	}
	b.mu.Unlock()
	for _, entry := range overflow {
		b.slowDrops.Add(1)
		b.slowDisconnects.Add(1)
		_ = entry.sub.Disconnect()
	}
	for _, entry := range active {
		entry.deliverMu.Lock()
		if entry.closed {
			entry.deliverMu.Unlock()
			continue
		}
		err := entry.sub.TryNotify("event", event)
		entry.deliverMu.Unlock()
		if err != nil {
			b.dropSlow(entry.id, entry.sub, err)
		}
	}
}

func (b *Bus) dropSlow(id string, sub eventSubscriber, err error) {
	b.Unsubscribe(id)
	if errors.Is(err, rpc.ErrSlowConsumer) {
		b.slowDrops.Add(1)
		b.slowDisconnects.Add(1)
		_ = sub.Disconnect()
	}
}

func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, entries := range b.subs {
		n += len(entries)
	}
	return n
}
func (b *Bus) Stats() BusStats {
	return BusStats{Published: b.published.Load(), SlowDrops: b.slowDrops.Load(), SlowDisconnects: b.slowDisconnects.Load()}
}
