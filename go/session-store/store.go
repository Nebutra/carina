// Package sessionstore persists sessions and their append-only event logs.
// Session metadata is written to one JSON file per session so the daemon
// can recover live sessions after a crash (PRD §17.3). Event logs are owned
// and written by the Rust kernel (single audit writer); this package reads
// session rows and reloads them on startup.
package sessionstore

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Session struct {
	SessionID         string    `json:"session_id"`
	WorkspaceID       string    `json:"workspace_id"`
	WorkspaceRoot     string    `json:"workspace_root"`
	Status            string    `json:"status"` // active | paused | closed
	PermissionProfile string    `json:"permission_profile"`
	ApprovalMode      string    `json:"approval_mode,omitempty"` // untrusted|on_request|never
	ParentID          string    `json:"parent_id,omitempty"`     // set for subagent sessions
	ForkedFromTaskID  string    `json:"forked_from_task_id,omitempty"`
	ForkedThroughTurn int       `json:"forked_through_turn,omitempty"`
	Depth             int       `json:"depth"` // 0 = main; bounded to prevent runaway nesting
	CreatedAt         time.Time `json:"created_at"`
}

// SetForkLineage persists the immutable model-context boundary inherited by a
// fork. Audit events remain in the parent log and are referenced, not copied.
func (s *Store) SetForkLineage(sessionID, taskID string, turn int) (*Session, error) {
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("sessionstore: unknown session %s", sessionID)
	}
	updated := *sess
	updated.ForkedFromTaskID = taskID
	updated.ForkedThroughTurn = turn
	s.sessions[sessionID] = &updated
	s.mu.Unlock()
	if err := s.persist(&updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// Event mirrors protocol/schemas/event.schema.json.
type Event struct {
	EventID              string          `json:"event_id"`
	SessionID            string          `json:"session_id"`
	TaskID               string          `json:"task_id,omitempty"`
	Type                 string          `json:"type"`
	Timestamp            time.Time       `json:"timestamp"`
	Payload              json.RawMessage `json:"payload,omitempty"`
	PermissionDecisionID string          `json:"permission_decision_id,omitempty"`
}

type Store struct {
	mu       sync.RWMutex
	dir      string
	sessions map[string]*Session
}

// Open prepares the store under dir and loads any persisted sessions.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		return nil, fmt.Errorf("sessionstore: create %s: %w", dir, err)
	}
	s := &Store{dir: dir, sessions: make(map[string]*Session)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads persisted session rows from disk (crash recovery).
func (s *Store) load() error {
	entries, err := os.ReadDir(filepath.Join(s.dir, "sessions"))
	if err != nil {
		return nil // no sessions yet
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, "sessions", e.Name()))
		if err != nil {
			continue
		}
		var sess Session
		if err := json.Unmarshal(raw, &sess); err != nil {
			continue
		}
		s.sessions[sess.SessionID] = &sess
	}
	return nil
}

// Recoverable returns sessions that were active at crash time and should be
// re-initialized in the kernel on startup.
func (s *Store) Recoverable() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*Session
	for _, sess := range s.sessions {
		if sess.Status == "active" {
			out = append(out, sess)
		}
	}
	return out
}

func (s *Store) CreateSession(workspaceRoot, profile string) (*Session, error) {
	return s.CreateSessionMode(workspaceRoot, profile, "")
}

// CreateSessionMode also sets the per-session approval mode (goal axis).
func (s *Store) CreateSessionMode(workspaceRoot, profile, approvalMode string) (*Session, error) {
	return s.createSession(workspaceRoot, profile, approvalMode, "", 0)
}

// CreateSubSession creates an isolated subagent session linked to a parent,
// at depth = parent.Depth + 1 (bounded by the caller to prevent runaway
// nesting).
func (s *Store) CreateSubSession(workspaceRoot, profile, approvalMode, parentID string, depth int) (*Session, error) {
	return s.createSession(workspaceRoot, profile, approvalMode, parentID, depth)
}

func (s *Store) createSession(workspaceRoot, profile, approvalMode, parentID string, depth int) (*Session, error) {
	if profile == "" {
		profile = "safe-edit"
	}
	sess := &Session{
		SessionID:         NewID("sess"),
		WorkspaceID:       NewID("ws"),
		WorkspaceRoot:     workspaceRoot,
		Status:            "active",
		PermissionProfile: profile,
		ApprovalMode:      approvalMode,
		ParentID:          parentID,
		Depth:             depth,
		CreatedAt:         time.Now().UTC(),
	}
	s.mu.Lock()
	s.sessions[sess.SessionID] = sess
	s.mu.Unlock()
	if err := s.persist(sess); err != nil {
		return nil, err
	}
	return sess, nil
}

func (s *Store) Get(sessionID string) (*Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[sessionID]
	return sess, ok
}

func (s *Store) List() []*Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	return out
}

func (s *Store) SetStatus(sessionID, status string) (*Session, error) {
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return nil, fmt.Errorf("sessionstore: unknown session %s", sessionID)
	}
	updated := *sess
	updated.Status = status
	s.sessions[sessionID] = &updated
	s.mu.Unlock()
	if err := s.persist(&updated); err != nil {
		return nil, err
	}
	return &updated, nil
}

// Delete removes a terminal session from durable recovery state.
func (s *Store) Delete(sessionID string) error {
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		s.mu.Unlock()
		return nil
	}
	if sess.Status != "closed" {
		s.mu.Unlock()
		return fmt.Errorf("sessionstore: session %s is %s, not closed", sessionID, sess.Status)
	}
	delete(s.sessions, sessionID)
	s.mu.Unlock()
	if err := os.Remove(filepath.Join(s.dir, "sessions", sessionID+".json")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("sessionstore: delete: %w", err)
	}
	return nil
}

// persist atomically writes a session row (temp + rename).
func (s *Store) persist(sess *Session) error {
	path := filepath.Join(s.dir, "sessions", sess.SessionID+".json")
	raw, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return fmt.Errorf("sessionstore: marshal: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("sessionstore: write: %w", err)
	}
	return os.Rename(tmp, path)
}

// AppendEvent writes one event to the session's append-only JSONL log.
// (Retained for tooling/tests; the kernel is the primary event writer.)
func (s *Store) AppendEvent(ev Event) error {
	if ev.EventID == "" {
		ev.EventID = NewID("evt")
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "events"), 0o700); err != nil {
		return fmt.Errorf("sessionstore: create events dir: %w", err)
	}
	f, err := os.OpenFile(s.eventLogPath(ev.SessionID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("sessionstore: open event log: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("sessionstore: marshal event: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("sessionstore: append event: %w", err)
	}
	return nil
}

// ReadEvents replays the full event stream of a session.
func (s *Store) ReadEvents(sessionID string) ([]Event, error) {
	f, err := os.Open(s.eventLogPath(sessionID))
	if os.IsNotExist(err) {
		return []Event{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sessionstore: open event log: %w", err)
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			return nil, fmt.Errorf("sessionstore: corrupt event log %s: %w", sessionID, err)
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}

func (s *Store) eventLogPath(sessionID string) string {
	return filepath.Join(s.dir, "events", sessionID+".events.jsonl")
}

// NewID returns a prefixed random identifier, e.g. "sess_3f2a…".
func NewID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
