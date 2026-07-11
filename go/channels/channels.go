// Package channels authenticates and deduplicates external events before they
// are relayed into a running session. It never executes event payloads.
package channels

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Sender struct {
	ID                 string   `json:"id"`
	Secret             []byte   `json:"-"`
	SecretRef          string   `json:"secret_ref,omitempty"`
	Sessions           []string `json:"sessions"`
	Kinds              []string `json:"kinds"`
	CanRelayPermission bool     `json:"can_relay_permission"`
}
type Event struct {
	ID                   string         `json:"id"`
	SenderID             string         `json:"sender_id"`
	SessionID            string         `json:"session_id"`
	Kind                 string         `json:"kind"`
	Timestamp            time.Time      `json:"timestamp"`
	Payload              map[string]any `json:"payload,omitempty"`
	PermissionDecisionID string         `json:"permission_decision_id,omitempty"`
	PermissionAllow      *bool          `json:"permission_allow,omitempty"`
}
type Receipt struct {
	Accepted  bool  `json:"accepted"`
	Duplicate bool  `json:"duplicate"`
	Event     Event `json:"event"`
}
type Registry struct {
	mu                  sync.Mutex
	senders             map[string]Sender
	seen                map[string]time.Time
	pending             map[string]string
	path                string
	resolveSecret       func(string) ([]byte, error)
	maxSkew, timeToLive time.Duration
	now                 func() time.Time
}

func New(maxSkew, timeToLive time.Duration) *Registry {
	if maxSkew <= 0 {
		maxSkew = 5 * time.Minute
	}
	if timeToLive <= 0 {
		timeToLive = 24 * time.Hour
	}
	return &Registry{senders: map[string]Sender{}, seen: map[string]time.Time{}, pending: map[string]string{}, maxSkew: maxSkew, timeToLive: timeToLive, now: time.Now}
}

// Open loads non-secret sender policy and committed deduplication receipts.
// Secret material is resolved only when a signature is verified.
func Open(stateDir string, maxSkew, timeToLive time.Duration, resolver func(string) ([]byte, error)) (*Registry, error) {
	r := New(maxSkew, timeToLive)
	r.path = filepath.Join(stateDir, "channels.json")
	r.resolveSecret = resolver
	raw, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	var disk struct {
		Senders map[string]Sender    `json:"senders"`
		Seen    map[string]time.Time `json:"seen"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		return nil, fmt.Errorf("channels: load: %w", err)
	}
	if disk.Senders != nil {
		r.senders = disk.Senders
	}
	if disk.Seen != nil {
		r.seen = disk.Seen
	}
	return r, nil
}
func (r *Registry) Register(s Sender) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.ID == "" || (len(s.Secret) < 32 && s.SecretRef == "") {
		return errors.New("channels: sender id and a secret handle are required")
	}
	if s.SecretRef != "" && r.resolveSecret == nil {
		return errors.New("channels: secret resolver is not configured")
	}
	if s.SecretRef != "" {
		secret, err := r.resolveSecret(s.SecretRef)
		if err != nil || len(secret) < 32 {
			return errors.New("channels: referenced secret must resolve to at least 32 bytes")
		}
	}
	r.senders[s.ID] = s
	return r.persistLocked()
}
func Canonical(e Event) []byte {
	// encoding/json sorts map keys, making this stable while binding the full
	// payload and permission relay fields to the signature.
	raw, _ := json.Marshal(struct {
		ID                   string         `json:"id"`
		SenderID             string         `json:"sender_id"`
		SessionID            string         `json:"session_id"`
		Kind                 string         `json:"kind"`
		Timestamp            int64          `json:"timestamp_unix_nano"`
		Payload              map[string]any `json:"payload,omitempty"`
		PermissionDecisionID string         `json:"permission_decision_id,omitempty"`
		PermissionAllow      *bool          `json:"permission_allow,omitempty"`
	}{e.ID, e.SenderID, e.SessionID, e.Kind, e.Timestamp.UnixNano(), e.Payload, e.PermissionDecisionID, e.PermissionAllow})
	return raw
}
func Sign(secret []byte, e Event) string {
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write(Canonical(e))
	return hex.EncodeToString(m.Sum(nil))
}

type Reservation struct {
	Key     string
	Token   string
	Receipt Receipt
}

// Reserve authenticates and exclusively reserves an event ID. It does not
// commit deduplication state; callers must Commit only after all side effects
// succeed, or Abort so the event can be retried safely.
func (r *Registry) Reserve(e Event, signature string) (Reservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.senders[e.SenderID]
	if !ok {
		return Reservation{}, errors.New("channels: untrusted sender")
	}
	now := r.now().UTC()
	if e.ID == "" || e.SessionID == "" || e.Kind == "" {
		return Reservation{}, errors.New("channels: id, session_id and kind are required")
	}
	if delta := now.Sub(e.Timestamp); delta > r.maxSkew || delta < -r.maxSkew {
		return Reservation{}, errors.New("channels: event timestamp outside allowed skew")
	}
	secret := s.Secret
	if len(secret) == 0 && s.SecretRef != "" {
		var err error
		secret, err = r.resolveSecret(s.SecretRef)
		if err != nil || len(secret) < 32 {
			return Reservation{}, errors.New("channels: sender secret unavailable")
		}
	}
	want := Sign(secret, e)
	got, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal([]byte(want), []byte(hex.EncodeToString(got))) {
		return Reservation{}, errors.New("channels: invalid signature")
	}
	if !allowed(s.Sessions, e.SessionID) || !allowed(s.Kinds, e.Kind) {
		return Reservation{}, errors.New("channels: sender is not allowed for event target")
	}
	if e.PermissionDecisionID != "" && !s.CanRelayPermission {
		return Reservation{}, errors.New("channels: sender cannot relay permission decisions")
	}
	for id, at := range r.seen {
		if now.Sub(at) > r.timeToLive {
			delete(r.seen, id)
		}
	}
	key := e.SenderID + ":" + e.ID
	if _, ok := r.seen[key]; ok {
		return Reservation{Receipt: Receipt{Accepted: true, Duplicate: true, Event: e}}, nil
	}
	if _, ok := r.pending[key]; ok {
		return Reservation{}, errors.New("channels: event is already being processed")
	}
	token := Sign(secret, Event{ID: e.ID, SenderID: e.SenderID, SessionID: e.SessionID, Kind: "reservation", Timestamp: now})
	r.pending[key] = token
	return Reservation{Key: key, Token: token, Receipt: Receipt{Accepted: true, Event: e}}, nil
}

func (r *Registry) Commit(res Reservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if res.Receipt.Duplicate {
		return nil
	}
	if res.Key == "" || r.pending[res.Key] != res.Token {
		return errors.New("channels: invalid reservation")
	}
	delete(r.pending, res.Key)
	r.seen[res.Key] = r.now().UTC()
	return r.persistLocked()
}
func (r *Registry) Abort(res Reservation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pending[res.Key] == res.Token {
		delete(r.pending, res.Key)
	}
}
func (r *Registry) Ingest(e Event, signature string) (Receipt, error) {
	res, err := r.Reserve(e, signature)
	if err != nil {
		return Receipt{}, err
	}
	if err := r.Commit(res); err != nil {
		r.Abort(res)
		return Receipt{}, err
	}
	return res.Receipt, nil
}
func allowed(values []string, v string) bool {
	for _, x := range values {
		if x == "*" || strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func (r *Registry) persistLocked() error {
	if r.path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	senders := map[string]Sender{}
	for id, s := range r.senders {
		s.Secret = nil
		senders[id] = s
	}
	raw, err := json.MarshalIndent(struct {
		Senders map[string]Sender    `json:"senders"`
		Seen    map[string]time.Time `json:"seen"`
	}{senders, r.seen}, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}
func (r *Registry) Senders() []Sender {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Sender, 0, len(r.senders))
	for _, s := range r.senders {
		s.Secret = nil
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
