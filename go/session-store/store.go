// Package sessionstore persists sessions and their append-only event logs.
// MVP storage: in-memory session table + one JSONL event log per session
// (PRD §15.2: SQLite + JSONL; SQLite lands with session recovery in Phase 1).
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
	CreatedAt         time.Time `json:"created_at"`
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

// Open prepares the store under dir (created if missing).
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sessionstore: create %s: %w", dir, err)
	}
	return &Store{dir: dir, sessions: make(map[string]*Session)}, nil
}

func (s *Store) CreateSession(workspaceRoot, profile string) (*Session, error) {
	if profile == "" {
		profile = "safe-edit"
	}
	sess := &Session{
		SessionID:         NewID("sess"),
		WorkspaceID:       NewID("ws"),
		WorkspaceRoot:     workspaceRoot,
		Status:            "active",
		PermissionProfile: profile,
		CreatedAt:         time.Now().UTC(),
	}
	s.mu.Lock()
	s.sessions[sess.SessionID] = sess
	s.mu.Unlock()
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
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("sessionstore: unknown session %s", sessionID)
	}
	updated := *sess
	updated.Status = status
	s.sessions[sessionID] = &updated
	return &updated, nil
}

// AppendEvent writes one event to the session's append-only JSONL log.
func (s *Store) AppendEvent(ev Event) error {
	if ev.EventID == "" {
		ev.EventID = NewID("evt")
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
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
	return filepath.Join(s.dir, sessionID+".events.jsonl")
}

// NewID returns a prefixed random identifier, e.g. "sess_3f2a…".
func NewID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
