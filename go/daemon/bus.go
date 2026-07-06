package daemon

import (
	"sync"

	"github.com/Nebutra/carina/go/rpc"
)

// Bus fans session events out to live subscribers (PRD §8.6: event
// streaming to CLI/TUI/IDE). Subscriptions are dropped when the client
// disconnects.
type Bus struct {
	mu   sync.RWMutex
	subs map[string][]*rpc.Subscription // session_id -> subscribers
	taps []func(sessionID string, event map[string]any)
}

func NewBus() *Bus {
	return &Bus{subs: make(map[string][]*rpc.Subscription)}
}

func (b *Bus) Subscribe(sessionID string, sub *rpc.Subscription) {
	b.mu.Lock()
	b.subs[sessionID] = append(b.subs[sessionID], sub)
	b.mu.Unlock()
}

// Tap registers an in-process listener invoked for every published event, across
// all sessions. Used for coordination (a parent awaiting a child's completion
// envelope) and metrics, without an RPC round-trip.
func (b *Bus) Tap(fn func(sessionID string, event map[string]any)) {
	b.mu.Lock()
	b.taps = append(b.taps, fn)
	b.mu.Unlock()
}

// Publish delivers an event to every in-process tap and every live subscriber of
// the session, pruning any subscribers that have disconnected.
func (b *Bus) Publish(sessionID string, event map[string]any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, tap := range b.taps {
		tap(sessionID, event)
	}
	subs := b.subs[sessionID]
	if len(subs) == 0 {
		return
	}
	live := subs[:0]
	for _, sub := range subs {
		select {
		case <-sub.Done():
			continue // disconnected; drop
		default:
		}
		if err := sub.Notify("event", event); err == nil {
			live = append(live, sub)
		}
	}
	b.subs[sessionID] = live
}

func (b *Bus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, subs := range b.subs {
		n += len(subs)
	}
	return n
}
