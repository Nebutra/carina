// Package channels authenticates and deduplicates external events before they
// are relayed into a running session. It never executes event payloads.
package channels

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

type Sender struct {
	ID                 string   `json:"id"`
	Secret             []byte   `json:"-"`
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
	return &Registry{senders: map[string]Sender{}, seen: map[string]time.Time{}, maxSkew: maxSkew, timeToLive: timeToLive, now: time.Now}
}
func (r *Registry) Register(s Sender) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.ID == "" || len(s.Secret) < 32 {
		return errors.New("channels: sender id and 32-byte secret are required")
	}
	r.senders[s.ID] = s
	return nil
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
func (r *Registry) Ingest(e Event, signature string) (Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.senders[e.SenderID]
	if !ok {
		return Receipt{}, errors.New("channels: untrusted sender")
	}
	now := r.now().UTC()
	if e.ID == "" || e.SessionID == "" || e.Kind == "" {
		return Receipt{}, errors.New("channels: id, session_id and kind are required")
	}
	if delta := now.Sub(e.Timestamp); delta > r.maxSkew || delta < -r.maxSkew {
		return Receipt{}, errors.New("channels: event timestamp outside allowed skew")
	}
	want := Sign(s.Secret, e)
	got, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal([]byte(want), []byte(hex.EncodeToString(got))) {
		return Receipt{}, errors.New("channels: invalid signature")
	}
	if !allowed(s.Sessions, e.SessionID) || !allowed(s.Kinds, e.Kind) {
		return Receipt{}, errors.New("channels: sender is not allowed for event target")
	}
	if e.PermissionDecisionID != "" && !s.CanRelayPermission {
		return Receipt{}, errors.New("channels: sender cannot relay permission decisions")
	}
	for id, at := range r.seen {
		if now.Sub(at) > r.timeToLive {
			delete(r.seen, id)
		}
	}
	key := e.SenderID + ":" + e.ID
	if _, ok := r.seen[key]; ok {
		return Receipt{Accepted: true, Duplicate: true, Event: e}, nil
	}
	r.seen[key] = now
	return Receipt{Accepted: true, Event: e}, nil
}
func allowed(values []string, v string) bool {
	for _, x := range values {
		if x == "*" || strings.EqualFold(x, v) {
			return true
		}
	}
	return false
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
