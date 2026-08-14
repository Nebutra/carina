package daemon

import (
	"errors"
	"math"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/Nebutra/carina/go/rpc"
)

const busPendingLimit = 256

type busSubscriber struct {
	id         string
	sub        eventSubscriber
	active     bool
	pending    []map[string]any
	replayCut  int
	delivering bool
	deliverMu  sync.Mutex
	closed     bool
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
	id, _ := b.add(sessionID, sub, true)
	return id
}

// SubscribeCatchUp registers inactive before replay starts. Events published
// during replay or pending delivery remain buffered. The subscriber becomes
// active only after the pending queue is empty under the bus lock, while its
// delivery lock still prevents a newly active publisher from overtaking.
func (b *Bus) SubscribeCatchUp(
	sessionID string,
	sub eventSubscriber,
	replay func() ([]any, int, error),
	commit ...func(subscriptionID string, cursor, replayed int) error,
) (string, int, int, error) {
	id, entry := b.add(sessionID, sub, false)
	entry.deliverMu.Lock()
	if entry.closed {
		entry.deliverMu.Unlock()
		return "", 0, 0, errors.New("subscription closed before catch-up")
	}
	events, cursor, err := replay()
	if err != nil {
		entry.deliverMu.Unlock()
		b.Unsubscribe(id)
		return "", 0, 0, err
	}
	b.mu.Lock()
	if b.lookupLocked(id) != entry {
		b.mu.Unlock()
		entry.deliverMu.Unlock()
		return "", 0, 0, errors.New("subscription closed during replay")
	}
	entry.replayCut = cursor
	b.mu.Unlock()
	for _, event := range events {
		if err := sub.TryNotify("event", event); err != nil {
			entry.deliverMu.Unlock()
			b.dropSlow(id, sub, err)
			return "", 0, 0, err
		}
	}
	for {
		b.mu.Lock()
		if b.lookupLocked(id) != entry {
			b.mu.Unlock()
			entry.deliverMu.Unlock()
			return "", 0, 0, errors.New("subscription closed during catch-up")
		}
		pending := append([]map[string]any(nil), entry.pending...)
		entry.pending = nil
		if len(pending) == 0 {
			if len(commit) > 0 && commit[0] != nil {
				if err := commit[0](id, cursor, len(events)); err != nil {
					delete(b.subs[sessionID], id)
					if len(b.subs[sessionID]) == 0 {
						delete(b.subs, sessionID)
					}
					entry.closed = true
					b.mu.Unlock()
					entry.deliverMu.Unlock()
					if errors.Is(err, rpc.ErrSlowConsumer) {
						b.slowDrops.Add(1)
						b.slowDisconnects.Add(1)
					}
					_ = sub.Disconnect()
					return "", 0, 0, err
				}
			}
			entry.active = true
			b.mu.Unlock()
			entry.deliverMu.Unlock()
			return id, cursor, len(events), nil
		}
		b.mu.Unlock()
		for _, event := range pending {
			if pendingCursor := rawAuditCursor(event); pendingCursor > 0 && pendingCursor <= cursor {
				continue
			}
			if err := sub.TryNotify("event", event); err != nil {
				entry.deliverMu.Unlock()
				b.dropSlow(id, sub, err)
				return "", 0, 0, err
			}
		}
	}
}

func rawAuditCursor(event map[string]any) int {
	maxInt := uint64(^uint(0) >> 1)
	switch value := event[internalRawAuditCursor].(type) {
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 && uint64(value) <= maxInt {
			return int(value)
		}
	case uint64:
		if value > 0 && value <= maxInt {
			return int(value)
		}
	case float64:
		// JSON numbers decode as float64. Reject fractions, non-finite values,
		// values above JSON's exact integer range, and the native int boundary.
		if value >= 1 && value <= 1<<53-1 && value < math.Ldexp(1, strconv.IntSize-1) && math.Trunc(value) == value {
			return int(value)
		}
	}
	return 0
}

func (b *Bus) add(sessionID string, sub eventSubscriber, active bool) (string, *busSubscriber) {
	id := sub.ID()
	if id == "" {
		id = sessionID + ":legacy"
	}
	entry := &busSubscriber{id: id, sub: sub, active: active}
	b.mu.Lock()
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = make(map[string]*busSubscriber)
	}
	b.subs[sessionID][id] = entry
	b.mu.Unlock()
	go func() { <-sub.Done(); b.Unsubscribe(id) }()
	return id, entry
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
	drainers := make([]*busSubscriber, 0, len(entries))
	overflow := make([]*busSubscriber, 0)
	for _, entry := range entries {
		if entry.active {
			if cursor := rawAuditCursor(event); cursor > 0 && cursor <= entry.replayCut {
				continue
			}
		}
		if len(entry.pending) >= busPendingLimit {
			overflow = append(overflow, entry)
			delete(entries, entry.id)
			continue
		}
		entry.pending = append(entry.pending, event)
		if entry.active && !entry.delivering {
			entry.delivering = true
			drainers = append(drainers, entry)
		}
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
	for _, entry := range drainers {
		b.drainActive(entry)
	}
}

// drainActive is the single delivery owner for an active subscriber. Publish
// admits events to pending under Bus.mu, establishing their order without
// holding the bus lock across transport work. Concurrent publishers only
// enqueue and return while an owner is already draining.
func (b *Bus) drainActive(entry *busSubscriber) {
	entry.deliverMu.Lock()
	for {
		b.mu.Lock()
		if entry.closed || b.lookupLocked(entry.id) != entry || !entry.active {
			b.mu.Unlock()
			entry.deliverMu.Unlock()
			return
		}
		pending := append([]map[string]any(nil), entry.pending...)
		entry.pending = nil
		if len(pending) == 0 {
			entry.delivering = false
			b.mu.Unlock()
			entry.deliverMu.Unlock()
			return
		}
		b.mu.Unlock()

		for _, event := range pending {
			if err := entry.sub.TryNotify("event", event); err != nil {
				entry.deliverMu.Unlock()
				b.dropSlow(entry.id, entry.sub, err)
				return
			}
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
