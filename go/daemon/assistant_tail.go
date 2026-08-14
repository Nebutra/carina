package daemon

import (
	"errors"
	"fmt"
	"sort"
)

const (
	assistantTailMaxSessionEntries = 16
	assistantTailMaxGlobalEntries  = 64
	assistantTailMaxSessionBytes   = 128 << 20
	assistantTailMaxGlobalBytes    = 256 << 20
	assistantTailMaxSeals          = 1024
)

var (
	errAssistantTailOwner      = errors.New("assistant tail owner is stale")
	errAssistantTailTransition = errors.New("invalid assistant tail transition")
	errAssistantTailOverflow   = errors.New("assistant tail registry overflow")
	errAssistantTailSealed     = errors.New("assistant tail is sealed")
)

type assistantTailKey struct {
	sessionID string
	runID     string
	phase     string
}

type assistantTailOwner struct {
	key            assistantTailKey
	token          uint64
	generationBase uint64
}

type assistantTailState uint8

const (
	assistantTailOpen assistantTailState = iota
	assistantTailAwaitingCanonical
)

type assistantTailEntry struct {
	key              assistantTailKey
	ownerToken       uint64
	generation       uint64
	sequence         uint64
	body             string
	structuredOutput bool
	state            assistantTailState
	revision         uint64
}

type assistantTailSnapshot struct {
	SessionID        string
	RunID            string
	Phase            string
	Generation       uint64
	Sequence         uint64
	Content          string
	StructuredOutput bool
	State            assistantTailState
	Revision         uint64
}

type assistantTailRegistry struct {
	entries         map[assistantTailKey]*assistantTailEntry
	lastGeneration  map[assistantTailKey]uint64
	sessionRevision map[string]uint64
	sessionBytes    map[string]int
	totalBytes      int
	nextOwnerToken  uint64
	sealed          map[assistantTailKey]struct{}
	sealedOrder     []assistantTailKey
}

func (r *assistantTailRegistry) begin(key assistantTailKey) (assistantTailOwner, error) {
	if key.sessionID == "" || key.runID == "" || key.phase == "" {
		return assistantTailOwner{}, fmt.Errorf("%w: missing session, run, or phase", errAssistantTailTransition)
	}
	r.init()
	if r.isSealed(key) {
		return assistantTailOwner{}, errAssistantTailSealed
	}
	if _, exists := r.entries[key]; !exists {
		if r.sessionEntryCount(key.sessionID) >= assistantTailMaxSessionEntries || len(r.entries) >= assistantTailMaxGlobalEntries {
			return assistantTailOwner{}, errAssistantTailOverflow
		}
	}
	r.removeEntry(key)
	r.nextOwnerToken++
	if r.nextOwnerToken == 0 {
		r.nextOwnerToken++
	}
	generationBase := r.lastGeneration[key]
	entry := &assistantTailEntry{key: key, ownerToken: r.nextOwnerToken}
	r.entries[key] = entry
	return assistantTailOwner{key: key, token: entry.ownerToken, generationBase: generationBase}, nil
}

func (r *assistantTailRegistry) publish(
	owner assistantTailOwner,
	kind string,
	generation uint64,
	sequence uint64,
	text string,
	structuredOutput bool,
) (assistantTailSnapshot, error) {
	r.init()
	if r.isSealed(owner.key) {
		return assistantTailSnapshot{}, errAssistantTailSealed
	}
	entry := r.entries[owner.key]
	if entry == nil || owner.token == 0 || entry.ownerToken != owner.token {
		return assistantTailSnapshot{}, errAssistantTailOwner
	}
	if generation == 0 || sequence == 0 {
		return assistantTailSnapshot{}, fmt.Errorf("%w: generation and sequence must be positive", errAssistantTailTransition)
	}
	if generation > ^uint64(0)-owner.generationBase {
		r.removeEntry(owner.key)
		return assistantTailSnapshot{}, fmt.Errorf("%w: generation overflow", errAssistantTailOverflow)
	}
	generation += owner.generationBase
	if entry.state == assistantTailAwaitingCanonical {
		return assistantTailSnapshot{}, fmt.Errorf("%w: completed tail awaits canonical retirement", errAssistantTailTransition)
	}
	if entry.generation == 0 || generation > entry.generation {
		if kind != "reset" || sequence != 1 {
			return assistantTailSnapshot{}, fmt.Errorf("%w: a generation must start with reset sequence 1", errAssistantTailTransition)
		}
	} else if generation < entry.generation || sequence != entry.sequence+1 {
		return assistantTailSnapshot{}, fmt.Errorf("%w: stale, duplicate, or gapped update", errAssistantTailTransition)
	}

	oldBytes := len(entry.body)
	newBody := entry.body
	switch kind {
	case "reset":
		newBody = ""
	case "delta":
		newBody += text
	case "completed":
		newBody = text
	default:
		return assistantTailSnapshot{}, fmt.Errorf("%w: unknown update kind %q", errAssistantTailTransition, kind)
	}
	if len(newBody) > maxProviderResponseBytes {
		r.removeEntry(owner.key)
		return assistantTailSnapshot{}, fmt.Errorf("%w: assistant body exceeds provider response bound", errAssistantTailOverflow)
	}
	deltaBytes := len(newBody) - oldBytes
	if r.sessionBytes[owner.key.sessionID]+deltaBytes > assistantTailMaxSessionBytes || r.totalBytes+deltaBytes > assistantTailMaxGlobalBytes {
		r.removeEntry(owner.key)
		return assistantTailSnapshot{}, errAssistantTailOverflow
	}

	entry.generation = generation
	entry.sequence = sequence
	entry.body = newBody
	entry.structuredOutput = structuredOutput
	entry.state = assistantTailOpen
	if kind == "completed" {
		entry.state = assistantTailAwaitingCanonical
	}
	entry.revision = r.nextRevision(owner.key.sessionID)
	r.sessionBytes[owner.key.sessionID] += deltaBytes
	r.totalBytes += deltaBytes
	return snapshotFromEntry(entry), nil
}

func (r *assistantTailRegistry) revoke(owner assistantTailOwner) bool {
	r.init()
	entry := r.entries[owner.key]
	if entry == nil || owner.token == 0 || entry.ownerToken != owner.token {
		return false
	}
	r.removeEntry(owner.key)
	return true
}

func (r *assistantTailRegistry) retire(key assistantTailKey) bool {
	r.init()
	if r.entries[key] == nil {
		return false
	}
	r.removeEntry(key)
	return true
}

func (r *assistantTailRegistry) seal(key assistantTailKey) {
	r.init()
	r.removeEntry(key)
	if _, exists := r.sealed[key]; exists {
		return
	}
	if len(r.sealedOrder) >= assistantTailMaxSeals {
		oldest := r.sealedOrder[0]
		r.sealedOrder = append([]assistantTailKey(nil), r.sealedOrder[1:]...)
		delete(r.sealed, oldest)
	}
	r.sealed[key] = struct{}{}
	r.sealedOrder = append(r.sealedOrder, key)
}

func (r *assistantTailRegistry) isSealed(key assistantTailKey) bool {
	if r == nil || r.sealed == nil {
		return false
	}
	_, ok := r.sealed[key]
	return ok
}

func snapshotFromEntry(entry *assistantTailEntry) assistantTailSnapshot {
	return assistantTailSnapshot{
		SessionID:        entry.key.sessionID,
		RunID:            entry.key.runID,
		Phase:            entry.key.phase,
		Generation:       entry.generation,
		Sequence:         entry.sequence,
		Content:          entry.body,
		StructuredOutput: entry.structuredOutput,
		State:            entry.state,
		Revision:         entry.revision,
	}
}

func (r *assistantTailRegistry) capture(sessionID string) ([]assistantTailSnapshot, uint64) {
	r.init()
	snapshots := make([]assistantTailSnapshot, 0, r.sessionEntryCount(sessionID))
	for key, entry := range r.entries {
		if key.sessionID != sessionID || entry.generation == 0 {
			continue
		}
		snapshots = append(snapshots, snapshotFromEntry(entry))
	}
	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].Revision != snapshots[j].Revision {
			return snapshots[i].Revision < snapshots[j].Revision
		}
		if snapshots[i].RunID != snapshots[j].RunID {
			return snapshots[i].RunID < snapshots[j].RunID
		}
		return snapshots[i].Phase < snapshots[j].Phase
	})
	return snapshots, r.sessionRevision[sessionID]
}

func (r *assistantTailRegistry) init() {
	if r.entries == nil {
		r.entries = make(map[assistantTailKey]*assistantTailEntry)
	}
	if r.lastGeneration == nil {
		r.lastGeneration = make(map[assistantTailKey]uint64)
	}
	if r.sessionRevision == nil {
		r.sessionRevision = make(map[string]uint64)
	}
	if r.sessionBytes == nil {
		r.sessionBytes = make(map[string]int)
	}
	if r.sealed == nil {
		r.sealed = make(map[assistantTailKey]struct{})
	}
}

func (r *assistantTailRegistry) nextRevision(sessionID string) uint64 {
	r.sessionRevision[sessionID]++
	if r.sessionRevision[sessionID] == 0 {
		r.sessionRevision[sessionID]++
	}
	return r.sessionRevision[sessionID]
}

func (r *assistantTailRegistry) sessionEntryCount(sessionID string) int {
	count := 0
	for key := range r.entries {
		if key.sessionID == sessionID {
			count++
		}
	}
	return count
}

func (r *assistantTailRegistry) removeEntry(key assistantTailKey) {
	entry := r.entries[key]
	if entry == nil {
		return
	}
	delete(r.entries, key)
	if entry.generation > r.lastGeneration[key] {
		r.lastGeneration[key] = entry.generation
	}
	r.sessionBytes[key.sessionID] -= len(entry.body)
	if r.sessionBytes[key.sessionID] <= 0 {
		delete(r.sessionBytes, key.sessionID)
	}
	r.totalBytes -= len(entry.body)
	if r.totalBytes < 0 {
		r.totalBytes = 0
	}
}
