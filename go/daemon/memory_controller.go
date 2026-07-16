package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/kernel"
)

const memoryControllerVersion = 1

type memoryRevision struct {
	Revision        string      `json:"revision"`
	Parent          string      `json:"parent,omitempty"`
	DocumentID      string      `json:"document_id"`
	Target          string      `json:"target"`
	Entries         []string    `json:"entries"`
	Source          string      `json:"source"`
	CreatedAt       time.Time   `json:"created_at"`
	Published       bool        `json:"published"`
	PreviousEntries []string    `json:"previous_entries,omitempty"`
	IdempotencyKey  string      `json:"idempotency_key,omitempty"`
	Scope           memoryScope `json:"scope"`
	SessionID       string      `json:"session_id,omitempty"`
}

type memoryControllerState struct {
	Version     int                         `json:"version"`
	Documents   map[string][]memoryRevision `json:"documents"`
	Idempotency map[string]string           `json:"idempotency,omitempty"`
}

type memoryControllerStore struct {
	mu    sync.Mutex
	path  string
	state memoryControllerState
}

func newMemoryControllerStore(stateDir string) *memoryControllerStore {
	s := &memoryControllerStore{path: filepath.Join(stateDir, "memory-controller.json"), state: memoryControllerState{Version: memoryControllerVersion, Documents: map[string][]memoryRevision{}, Idempotency: map[string]string{}}}
	if raw, err := os.ReadFile(s.path); err == nil {
		var loaded memoryControllerState
		if json.Unmarshal(raw, &loaded) == nil && loaded.Version == memoryControllerVersion && loaded.Documents != nil {
			if loaded.Idempotency == nil {
				loaded.Idempotency = map[string]string{}
			}
			s.state = loaded
		}
	}
	return s
}

func memoryDocumentID(scope memoryScope, target string) string {
	key := scope.Profile
	if target == memoryTargetMemory {
		key += "\x00" + scope.WorkspaceHash
	}
	sum := sha256.Sum256([]byte("carina-memory-v1\x00" + target + "\x00" + key))
	return "mem_" + hex.EncodeToString(sum[:16])
}

func memoryRevisionID(documentID string, entries []string) string {
	raw, _ := json.Marshal(entries)
	sum := sha256.Sum256(append([]byte(documentID+"\x00"), raw...))
	return "mr_" + hex.EncodeToString(sum[:])
}

func (s *memoryControllerStore) persistLocked() error {
	raw, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, s.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(s.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *memoryControllerStore) prepare(sessionID string, scope memoryScope, target string, entries, previous []string, source, idempotency string) (memoryRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc := memoryDocumentID(scope, target)
	revision := memoryRevisionID(doc, entries)
	if idempotency != "" {
		if existing := s.state.Idempotency[idempotency]; existing != "" && existing != revision {
			return memoryRevision{}, fmt.Errorf("memory idempotency conflict")
		}
	}
	history := s.state.Documents[doc]
	if len(history) > 0 && history[len(history)-1].Revision == revision && history[len(history)-1].Published {
		return history[len(history)-1], nil
	}
	parent := ""
	if len(history) > 0 {
		parent = history[len(history)-1].Revision
	}
	row := memoryRevision{Revision: revision, Parent: parent, DocumentID: doc, Target: target, Entries: append([]string(nil), entries...), PreviousEntries: append([]string(nil), previous...), Source: source, CreatedAt: time.Now().UTC(), IdempotencyKey: idempotency, Scope: scope, SessionID: sessionID}
	s.state.Documents[doc] = append(history, row)
	if err := s.persistLocked(); err != nil {
		s.state.Documents[doc] = history
		return memoryRevision{}, err
	}
	return row, nil
}

func (s *memoryControllerStore) publish(documentID, revision string) (memoryRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.state.Documents[documentID]
	for i := range rows {
		if rows[i].Revision == revision {
			previous := rows[i]
			previousIdempotency, hadIdempotency := s.state.Idempotency[rows[i].IdempotencyKey]
			rows[i].Published = true
			rows[i].PreviousEntries = nil
			if rows[i].IdempotencyKey != "" {
				s.state.Idempotency[rows[i].IdempotencyKey] = revision
			}
			s.state.Documents[documentID] = rows
			if err := s.persistLocked(); err != nil {
				rows[i] = previous
				s.state.Documents[documentID] = rows
				if previous.IdempotencyKey != "" {
					if hadIdempotency {
						s.state.Idempotency[previous.IdempotencyKey] = previousIdempotency
					} else {
						delete(s.state.Idempotency, previous.IdempotencyKey)
					}
				}
				return memoryRevision{}, err
			}
			return rows[i], nil
		}
	}
	return memoryRevision{}, fmt.Errorf("unknown prepared revision %s", revision)
}
func (s *memoryControllerStore) abort(documentID, revision string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.state.Documents[documentID]
	for i := range rows {
		if rows[i].Revision == revision && !rows[i].Published {
			s.state.Documents[documentID] = append(rows[:i], rows[i+1:]...)
			if err := s.persistLocked(); err != nil {
				s.state.Documents[documentID] = rows
				return err
			}
			return nil
		}
	}
	return nil
}
func (s *memoryControllerStore) record(scope memoryScope, target string, entries []string, source, idempotency string) (memoryRevision, error) {
	row, err := s.prepare("", scope, target, entries, entries, source, idempotency)
	if err != nil {
		return row, err
	}
	if row.Published {
		return row, nil
	}
	return s.publish(row.DocumentID, row.Revision)
}

func (d *Daemon) reconcileMemoryVersions() {
	d.memoryVersions.mu.Lock()
	var pending []memoryRevision
	for _, rows := range d.memoryVersions.state.Documents {
		for _, row := range rows {
			if !row.Published {
				pending = append(pending, row)
			}
		}
	}
	d.memoryVersions.mu.Unlock()
	for _, row := range pending {
		committed := false
		if row.SessionID != "" {
			if raw, err := d.kern.ReadEvents(row.SessionID); err == nil {
				committed = memoryRevisionCommitted(raw, row.Revision)
			}
		}
		if committed {
			_, _ = d.memoryVersions.publish(row.DocumentID, row.Revision)
		} else {
			_ = d.memory.restore(row.Scope, row.Target, row.PreviousEntries)
			_ = d.memoryVersions.abort(row.DocumentID, row.Revision)
		}
	}
}

func memoryRevisionCommitted(raw []byte, revision string) bool {
	var events []struct {
		Type    string         `json:"type"`
		Payload map[string]any `json:"payload"`
	}
	if json.Unmarshal(raw, &events) != nil {
		return false
	}
	for _, event := range events {
		if event.Type == "MemoryWritten" && event.Payload["revision"] == revision {
			return event.Payload["success"] == true || event.Payload["status"] == "committed" || event.Payload["status"] == "memory_write"
		}
	}
	return false
}

func (s *memoryControllerStore) find(scope memoryScope, target, revision string) (memoryRevision, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.state.Documents[memoryDocumentID(scope, target)] {
		if r.Revision == revision && r.Published {
			return r, true
		}
	}
	return memoryRevision{}, false
}

func (d *Daemon) memoryRead(sessionID, target string) (map[string]any, error) {
	sess, ok := d.store.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", sessionID)
	}
	scope := memoryScopeFromSession(sess)
	state, err := d.memory.list(scope, target)
	if err != nil {
		return nil, err
	}
	rev, err := d.memoryVersions.record(scope, state.Target, state.Entries, "read-bootstrap", "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": memoryControllerVersion, "document_id": rev.DocumentID, "revision": rev.Revision, "target": state.Target, "scope": scope, "entries": state.Entries, "proof": map[string]any{"algorithm": "sha256", "content_revision": memoryRevisionID(rev.DocumentID, state.Entries)}}, nil
}

func (d *Daemon) handleMemoryRead(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Target    string `json:"target"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	return d.memoryRead(p.SessionID, p.Target)
}

func (d *Daemon) handleMemoryVerify(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Target    string `json:"target"`
		Revision  string `json:"revision"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	current, err := d.memoryRead(p.SessionID, p.Target)
	if err != nil {
		return nil, err
	}
	actual := current["revision"].(string)
	valid := p.Revision == "" || p.Revision == actual
	return map[string]any{"version": memoryControllerVersion, "valid": valid, "expected_revision": p.Revision, "actual_revision": actual, "document_id": current["document_id"], "proof": current["proof"]}, nil
}

func (d *Daemon) governedMemoryReplace(sessionID, target, expected, idempotency, source string, entries []string) (any, error) {
	sess, ok := d.store.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", sessionID)
	}
	scope := memoryScopeFromSession(sess)
	current, err := d.memory.list(scope, target)
	if err != nil {
		return nil, err
	}
	doc := memoryDocumentID(scope, current.Target)
	actual := memoryRevisionID(doc, current.Entries)
	if expected != "" && expected != actual {
		return nil, fmt.Errorf("memory revision conflict: expected %s, actual %s", expected, actual)
	}
	resource := fmt.Sprintf("target=%s scope=%s action=replace revision=%s", current.Target, doc, actual)
	dec, err := d.kern.Request(sessionID, "MemoryWrite", resource, "")
	if err != nil {
		return nil, err
	}
	if approved, ok := d.approveFromStoredGrant(sess, dec); ok {
		dec = approved
	}
	if dec.Decision == "requires_approval" {
		d.mu.Lock()
		d.pendingMemControls[dec.DecisionID] = pendingMemoryControl{
			kind:             "replace",
			sessionID:        sessionID,
			targetSessionID:  sessionID,
			target:           current.Target,
			expectedRevision: actual,
			idempotencyKey:   idempotency,
			source:           source,
			entries:          append([]string(nil), entries...),
		}
		d.mu.Unlock()
		return map[string]any{"decision": dec}, nil
	}
	if dec.Decision != "allowed" {
		return map[string]any{"decision": dec}, nil
	}
	return d.applyGovernedMemoryReplace(sessionID, current.Target, actual, idempotency, source, entries, dec)
}

func (d *Daemon) applyGovernedMemoryReplace(sessionID, target, expected, idempotency, source string, entries []string, dec *kernel.Decision) (any, error) {
	sess, ok := d.store.Get(sessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", sessionID)
	}
	scope := memoryScopeFromSession(sess)
	current, err := d.memory.list(scope, target)
	if err != nil {
		return nil, err
	}
	doc := memoryDocumentID(scope, current.Target)
	actual := memoryRevisionID(doc, current.Entries)
	if expected != actual {
		return nil, fmt.Errorf("memory revision conflict after approval: expected %s, actual %s", expected, actual)
	}
	payload := map[string]any{"target": current.Target, "expected_revision": actual, "source": source, "status": "prepared"}
	if err := d.recordChecked(sessionID, "MemoryWriteRequested", "", "go", payload, dec.DecisionID); err != nil {
		return nil, err
	}
	if err := d.memory.restore(scope, current.Target, entries); err != nil {
		return nil, err
	}
	prepared, err := d.memoryVersions.prepare(sessionID, scope, current.Target, entries, current.Entries, source, idempotency)
	if err != nil {
		_ = d.memory.restore(scope, current.Target, current.Entries)
		return nil, err
	}
	if err := d.recordChecked(sessionID, "MemoryWritten", "", "go", map[string]any{"target": current.Target, "revision": prepared.Revision, "parent_revision": prepared.Parent, "source": source, "status": "committed"}, dec.DecisionID); err != nil {
		_ = d.memory.restore(scope, current.Target, current.Entries)
		_ = d.memoryVersions.abort(prepared.DocumentID, prepared.Revision)
		return nil, err
	}
	rev, err := d.memoryVersions.publish(prepared.DocumentID, prepared.Revision)
	if err != nil {
		return map[string]any{"version": memoryControllerVersion, "document_id": prepared.DocumentID, "revision": prepared.Revision, "target": prepared.Target, "publication_pending": true}, nil
	}
	return map[string]any{"version": memoryControllerVersion, "document_id": rev.DocumentID, "revision": rev.Revision, "parent_revision": rev.Parent, "target": rev.Target}, nil
}

func (d *Daemon) resumePendingMemoryControl(p pendingMemoryControl, dec *kernel.Decision) (any, error) {
	switch p.kind {
	case "replace":
		return d.applyGovernedMemoryReplace(p.targetSessionID, p.target, p.expectedRevision, p.idempotencyKey, p.source, p.entries, dec)
	case "handoff":
		return d.governedMemoryReplace(p.targetSessionID, p.target, p.expectedRevision, p.idempotencyKey, p.source, p.entries)
	default:
		return nil, fmt.Errorf("unknown pending memory control action %q", p.kind)
	}
}

func (d *Daemon) handleMemoryRollback(params json.RawMessage) (any, error) {
	var p struct {
		SessionID        string `json:"session_id"`
		Target           string `json:"target"`
		Revision         string `json:"revision"`
		ExpectedRevision string `json:"expected_revision"`
		IdempotencyKey   string `json:"idempotency_key"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	sess, ok := d.store.Get(p.SessionID)
	if !ok {
		return nil, fmt.Errorf("unknown session %s", p.SessionID)
	}
	row, ok := d.memoryVersions.find(memoryScopeFromSession(sess), p.Target, p.Revision)
	if !ok {
		return nil, fmt.Errorf("unknown memory revision %s", p.Revision)
	}
	return d.governedMemoryReplace(p.SessionID, p.Target, p.ExpectedRevision, p.IdempotencyKey, "rollback:"+p.Revision, row.Entries)
}

func (d *Daemon) handleMemoryHandoff(params json.RawMessage) (any, error) {
	var p struct {
		SourceSessionID  string `json:"source_session_id"`
		TargetSessionID  string `json:"target_session_id"`
		Target           string `json:"target"`
		ExpectedRevision string `json:"expected_revision"`
		IdempotencyKey   string `json:"idempotency_key"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	src, ok := d.store.Get(p.SourceSessionID)
	if !ok {
		return nil, fmt.Errorf("unknown source session")
	}
	sourceState, err := d.memory.list(memoryScopeFromSession(src), p.Target)
	if err != nil {
		return nil, err
	}
	resource := fmt.Sprintf("memory_handoff source=%s target=%s revision=%s", p.SourceSessionID, p.TargetSessionID, memoryRevisionID(memoryDocumentID(memoryScopeFromSession(src), sourceState.Target), sourceState.Entries))
	dec, err := d.kern.Request(p.SourceSessionID, "MemoryExternalize", resource, "")
	if err != nil {
		return nil, err
	}
	if approved, ok := d.approveFromStoredGrant(src, dec); ok {
		dec = approved
	}
	if dec.Decision == "requires_approval" {
		d.mu.Lock()
		d.pendingMemControls[dec.DecisionID] = pendingMemoryControl{
			kind:             "handoff",
			sessionID:        p.SourceSessionID,
			targetSessionID:  p.TargetSessionID,
			target:           sourceState.Target,
			expectedRevision: p.ExpectedRevision,
			idempotencyKey:   p.IdempotencyKey,
			source:           "handoff:" + p.SourceSessionID,
			entries:          append([]string(nil), sourceState.Entries...),
		}
		d.mu.Unlock()
		return map[string]any{"decision": dec}, nil
	}
	if dec.Decision != "allowed" {
		return map[string]any{"decision": dec}, nil
	}
	return d.governedMemoryReplace(p.TargetSessionID, p.Target, p.ExpectedRevision, p.IdempotencyKey, "handoff:"+p.SourceSessionID, sourceState.Entries)
}
