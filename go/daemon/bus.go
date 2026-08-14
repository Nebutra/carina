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
	sealed     map[sealedRunPhase]struct{}
	delivering bool
	deliverMu  sync.Mutex
	closed     bool
}

type catchUpStage string

const (
	catchUpRegistered catchUpStage = "registered"
	catchUpReplayed   catchUpStage = "replayed"
	catchUpSnapshots  catchUpStage = "snapshots"
	catchUpPending    catchUpStage = "pending"
	catchUpAck        catchUpStage = "ack"
	catchUpActivated  catchUpStage = "activated"
)

type catchUpReplay struct {
	events        []any
	durableCursor int
	sealed        []sealedRunPhase
}

type catchUpCommit struct {
	subscriptionID        string
	durableCursor         int
	durableReplayed       int
	bufferedLive          int
	transientSnapshots    int
	transientTailRevision int
	sealed                []sealedRunPhase
}

type catchUpResult struct {
	subscriptionID        string
	durableCursor         int
	durableReplayed       int
	bufferedLive          int
	transientSnapshots    int
	transientTailRevision int
	sealed                []sealedRunPhase
}

type catchUpOptions struct {
	emitSnapshots bool
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
	TailOverflows   uint64 `json:"tail_overflows"`
}

// Bus fans events out without ever waiting on network consumers. A consumer
// that fills its bounded connection queue is disconnected and must catch up by
// cursor. This preserves global publisher health and makes overload explicit.
type Bus struct {
	mu              sync.RWMutex
	subs            map[string]map[string]*busSubscriber
	taps            []func(sessionID string, event map[string]any)
	tails           assistantTailRegistry
	published       atomic.Uint64
	slowDrops       atomic.Uint64
	slowDisconnects atomic.Uint64
	tailOverflows   atomic.Uint64
	probe           func(catchUpStage)
}

func NewBus() *Bus { return &Bus{subs: make(map[string]map[string]*busSubscriber)} }

func (b *Bus) Subscribe(sessionID string, sub eventSubscriber) string {
	id, _, _, _ := b.add(sessionID, sub, true)
	return id
}

// SubscribeCatchUp registers inactive before replay starts. Events published
// during replay or pending delivery remain buffered. The subscriber becomes
// active only after the pending queue is empty and the subscribe ACK has been
// enqueued under the bus lock, while its delivery lock still prevents a newly
// active publisher from overtaking the ACK.
func (b *Bus) SubscribeCatchUp(
	sessionID string,
	sub eventSubscriber,
	replay func() (catchUpReplay, error),
	commit ...func(catchUpCommit) error,
) (catchUpResult, error) {
	return b.subscribeCatchUp(sessionID, sub, replay, catchUpOptions{}, commit...)
}

func (b *Bus) subscribeCatchUp(
	sessionID string,
	sub eventSubscriber,
	replay func() (catchUpReplay, error),
	opts catchUpOptions,
	commit ...func(catchUpCommit) error,
) (catchUpResult, error) {
	id, entry, snapshots, tailRevision := b.add(sessionID, sub, false)
	b.catchUpProbe(catchUpRegistered)
	entry.deliverMu.Lock()
	fail := func(err error) (catchUpResult, error) {
		entry.deliverMu.Unlock()
		return catchUpResult{}, err
	}
	if entry.closed {
		return fail(errors.New("subscription closed before catch-up"))
	}
	replayed, err := replay()
	if err != nil {
		entry.deliverMu.Unlock()
		b.Unsubscribe(id)
		return catchUpResult{}, err
	}
	revision, ok := uint64ToInt(tailRevision)
	if !ok {
		entry.deliverMu.Unlock()
		b.Unsubscribe(id)
		return catchUpResult{}, errors.New("transient tail revision exceeds transport bound")
	}
	filtered := filterSealedSnapshots(snapshots, replayed.sealed)
	b.mu.Lock()
	if b.lookupLocked(id) != entry {
		b.mu.Unlock()
		return fail(errors.New("subscription closed during replay"))
	}
	entry.replayCut = replayed.durableCursor
	entry.sealed = sealedSet(replayed.sealed)
	for _, pair := range replayed.sealed {
		b.tails.seal(assistantTailKey{sessionID: sessionID, runID: pair.RunID, phase: pair.Phase})
	}
	b.mu.Unlock()

	durableReplayed := 0
	for _, event := range replayed.events {
		enqueued, notifyErr := tryNotifyEvent(sub, "event", event)
		if notifyErr != nil {
			entry.deliverMu.Unlock()
			b.dropSlow(id, sub, notifyErr)
			return catchUpResult{}, notifyErr
		}
		if enqueued {
			durableReplayed++
		}
	}
	b.catchUpProbe(catchUpReplayed)

	transientSnapshots := 0
	if opts.emitSnapshots {
		for _, snapshot := range filtered {
			frame, frameErr := snapshotWireEvent(snapshot)
			if frameErr != nil {
				entry.deliverMu.Unlock()
				b.Unsubscribe(id)
				return catchUpResult{}, frameErr
			}
			enqueued, notifyErr := tryNotifyEvent(sub, "event", frame)
			if notifyErr != nil {
				entry.deliverMu.Unlock()
				b.dropSlow(id, sub, notifyErr)
				return catchUpResult{}, notifyErr
			}
			if enqueued {
				transientSnapshots++
			}
		}
	}
	b.catchUpProbe(catchUpSnapshots)
	b.catchUpProbe(catchUpPending)

	bufferedLive := 0
	for {
		b.mu.Lock()
		if b.lookupLocked(id) != entry {
			b.mu.Unlock()
			return fail(errors.New("subscription closed during catch-up"))
		}
		pending := append([]map[string]any(nil), entry.pending...)
		entry.pending = nil
		if len(pending) == 0 {
			commitInfo := catchUpCommit{
				subscriptionID:        id,
				durableCursor:         replayed.durableCursor,
				durableReplayed:       durableReplayed,
				bufferedLive:          bufferedLive,
				transientSnapshots:    transientSnapshots,
				transientTailRevision: revision,
				sealed:                append([]sealedRunPhase(nil), replayed.sealed...),
			}
			b.catchUpProbe(catchUpAck)
			if len(commit) > 0 && commit[0] != nil {
				if commitErr := commit[0](commitInfo); commitErr != nil {
					delete(b.subs[sessionID], id)
					if len(b.subs[sessionID]) == 0 {
						delete(b.subs, sessionID)
					}
					entry.closed = true
					b.mu.Unlock()
					entry.deliverMu.Unlock()
					if errors.Is(commitErr, rpc.ErrSlowConsumer) {
						b.slowDrops.Add(1)
						b.slowDisconnects.Add(1)
					}
					_ = sub.Disconnect()
					return catchUpResult{}, commitErr
				}
			}
			entry.active = true
			b.mu.Unlock()
			entry.deliverMu.Unlock()
			b.catchUpProbe(catchUpActivated)
			return catchUpResult{
				subscriptionID:        id,
				durableCursor:         replayed.durableCursor,
				durableReplayed:       durableReplayed,
				bufferedLive:          bufferedLive,
				transientSnapshots:    transientSnapshots,
				transientTailRevision: revision,
				sealed:                commitInfo.sealed,
			}, nil
		}
		b.mu.Unlock()
		for _, event := range pending {
			if skipCatchUpPending(event, replayed.durableCursor, entry.sealed) {
				continue
			}
			enqueued, notifyErr := tryNotifyEvent(sub, "event", event)
			if notifyErr != nil {
				entry.deliverMu.Unlock()
				b.dropSlow(id, sub, notifyErr)
				return catchUpResult{}, notifyErr
			}
			if enqueued {
				bufferedLive++
			}
		}
	}
}

func skipCatchUpPending(event map[string]any, durableCut int, sealed map[sealedRunPhase]struct{}) bool {
	if cursor := rawAuditCursor(event); cursor > 0 && cursor <= durableCut {
		return true
	}
	return sealedTransient(event, sealed)
}

func (b *Bus) catchUpProbe(stage catchUpStage) {
	if b == nil || b.probe == nil {
		return
	}
	b.probe(stage)
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

func (b *Bus) add(sessionID string, sub eventSubscriber, active bool) (string, *busSubscriber, []assistantTailSnapshot, uint64) {
	id := sub.ID()
	if id == "" {
		id = sessionID + ":legacy"
	}
	entry := &busSubscriber{id: id, sub: sub, active: active, sealed: map[sealedRunPhase]struct{}{}}
	b.mu.Lock()
	if b.subs[sessionID] == nil {
		b.subs[sessionID] = make(map[string]*busSubscriber)
	}
	b.subs[sessionID][id] = entry
	snapshots, revision := b.tails.capture(sessionID)
	b.mu.Unlock()
	go func() { <-sub.Done(); b.Unsubscribe(id) }()
	return id, entry, snapshots, revision
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
	if pair, ok := eventSealsAssistantTail(event); ok {
		b.tails.seal(assistantTailKey{sessionID: sessionID, runID: pair.RunID, phase: pair.Phase})
	}
	drainers, overflow := b.enqueueLocked(sessionID, event)
	b.mu.Unlock()
	b.finishPublish(overflow, drainers)
}

func (b *Bus) PublishAssistantTail(
	owner *assistantTailOwner,
	sessionID, runID, kind string,
	generation, sequence uint64,
	text string,
	structuredOutput bool,
) error {
	if owner == nil {
		return errAssistantTailOwner
	}
	key := assistantTailKey{sessionID: sessionID, runID: runID, phase: assistantPhaseFinalAnswer}
	b.mu.Lock()
	if b.tails.isSealed(key) {
		b.mu.Unlock()
		return errAssistantTailSealed
	}
	if owner.token == 0 {
		begun, err := b.tails.begin(key)
		if err != nil {
			dropped := b.failTailLocked(sessionID, err)
			b.mu.Unlock()
			b.disconnectTailOverflow(dropped)
			return err
		}
		*owner = begun
	}
	snapshot, err := b.tails.publish(*owner, kind, generation, sequence, text, structuredOutput)
	if err != nil {
		dropped := b.failTailLocked(sessionID, err)
		b.mu.Unlock()
		b.disconnectTailOverflow(dropped)
		return err
	}
	event := tailStreamEvent(sessionID, runID, kind, snapshot, text, structuredOutput)
	b.published.Add(1)
	taps := append([]func(string, map[string]any){}, b.taps...)
	drainers, overflow := b.enqueueLocked(sessionID, event)
	b.mu.Unlock()
	for _, tap := range taps {
		tap(sessionID, event)
	}
	b.finishPublish(overflow, drainers)
	return nil
}

func (b *Bus) enqueueLocked(sessionID string, event map[string]any) (drainers, overflow []*busSubscriber) {
	entries := b.subs[sessionID]
	drainers = make([]*busSubscriber, 0, len(entries))
	overflow = make([]*busSubscriber, 0)
	for _, entry := range entries {
		if sealedTransient(event, entry.sealed) {
			continue
		}
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
	return drainers, overflow
}

func (b *Bus) finishPublish(overflow, drainers []*busSubscriber) {
	for _, entry := range overflow {
		b.slowDrops.Add(1)
		b.slowDisconnects.Add(1)
		_ = entry.sub.Disconnect()
	}
	for _, entry := range drainers {
		b.drainActive(entry)
	}
}

func (b *Bus) failTailLocked(sessionID string, err error) []*busSubscriber {
	if !errors.Is(err, errAssistantTailOverflow) {
		return nil
	}
	b.tailOverflows.Add(1)
	entries := b.subs[sessionID]
	delete(b.subs, sessionID)
	dropped := make([]*busSubscriber, 0, len(entries))
	for _, entry := range entries {
		dropped = append(dropped, entry)
	}
	return dropped
}

func (b *Bus) disconnectTailOverflow(entries []*busSubscriber) {
	for _, entry := range entries {
		entry.deliverMu.Lock()
		entry.closed = true
		entry.deliverMu.Unlock()
		_ = entry.sub.Disconnect()
	}
}

func (b *Bus) captureTails(sessionID string) ([]assistantTailSnapshot, uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tails.capture(sessionID)
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
			if sealedTransient(event, entry.sealed) {
				continue
			}
			if _, err := tryNotifyEvent(entry.sub, "event", event); err != nil {
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
	return BusStats{
		Published:       b.published.Load(),
		SlowDrops:       b.slowDrops.Load(),
		SlowDisconnects: b.slowDisconnects.Load(),
		TailOverflows:   b.tailOverflows.Load(),
	}
}

func filterSealedSnapshots(snapshots []assistantTailSnapshot, sealed []sealedRunPhase) []assistantTailSnapshot {
	if len(snapshots) == 0 || len(sealed) == 0 {
		return snapshots
	}
	drop := sealedSet(sealed)
	out := make([]assistantTailSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if _, sealed := drop[sealedRunPhase{RunID: snapshot.RunID, Phase: snapshot.Phase}]; sealed {
			continue
		}
		out = append(out, snapshot)
	}
	return out
}

func snapshotWireEvent(snapshot assistantTailSnapshot) (map[string]any, error) {
	generation, ok := uint64ToInt(snapshot.Generation)
	if !ok {
		return nil, errors.New("snapshot generation exceeds transport bound")
	}
	sequence, ok := uint64ToInt(snapshot.Sequence)
	if !ok {
		return nil, errors.New("snapshot sequence exceeds transport bound")
	}
	revision, ok := uint64ToInt(snapshot.Revision)
	if !ok {
		return nil, errors.New("snapshot tail_revision exceeds transport bound")
	}
	state := assistantTailStateOpen
	if snapshot.State == assistantTailAwaitingCanonical {
		state = assistantTailStateAwaitingFinal
	}
	return map[string]any{
		"type":       assistantMessageSnapshotType,
		"session_id": snapshot.SessionID,
		"run_id":     snapshot.RunID,
		"actor":      "model",
		"payload": map[string]any{
			"generation":        generation,
			"sequence":          sequence,
			"phase":             snapshot.Phase,
			"content":           snapshot.Content,
			"structured_output": snapshot.StructuredOutput,
			"tail_revision":     revision,
			"state":             state,
		},
	}, nil
}

func tailStreamEvent(sessionID, runID, kind string, snapshot assistantTailSnapshot, text string, structuredOutput bool) map[string]any {
	payload := map[string]any{
		"generation": snapshot.Generation,
		"sequence":   snapshot.Sequence,
		"phase":      snapshot.Phase,
	}
	switch kind {
	case "delta":
		payload["delta"] = text
		payload["structured_output"] = structuredOutput
	case "completed":
		payload["content"] = text
		payload["structured_output"] = structuredOutput
	}
	return map[string]any{
		"session_id": sessionID,
		"task_id":    runID,
		"run_id":     runID,
		"type":       "assistant.message." + kind,
		"actor":      "model",
		"payload":    payload,
	}
}

func uint64ToInt(value uint64) (int, bool) {
	maxInt := uint64(^uint(0) >> 1)
	if value > maxInt {
		return 0, false
	}
	return int(value), true
}
