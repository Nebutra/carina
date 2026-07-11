// Package artifact provides a scoped, content-addressed store for tool output.
package artifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// RunPeriodicGC survives transient scan failures. Failures remain visible in
// Metrics/Health while retries use bounded exponential backoff plus jitter.
func (s *Store) RunPeriodicGC(ctx context.Context, interval time.Duration, now func() time.Time) error {
	if interval <= 0 {
		return errors.New("artifact: GC interval must be positive")
	}
	if now == nil {
		now = time.Now
	}
	failures := 0
	for {
		if _, err := s.GC(now()); err != nil {
			failures++
		} else {
			failures = 0
		}
		delay := gcRetryDelay(interval, failures, now().UnixNano())
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func gcRetryDelay(interval time.Duration, failures int, entropy int64) time.Duration {
	base := interval
	if failures > 0 {
		shift := failures
		if shift > 5 {
			shift = 5
		}
		base = interval * time.Duration(1<<shift)
		if base > 15*time.Minute {
			base = 15 * time.Minute
		}
	}
	window := base / 5
	if window <= 0 {
		return base
	}
	if entropy < 0 {
		entropy = -entropy
	}
	return base + time.Duration(entropy%int64(window))
}

var (
	ErrNotFound       = errors.New("artifact not found")
	ErrObjectTooLarge = errors.New("artifact exceeds object size limit")
	ErrTooLarge       = ErrObjectTooLarge // compatibility alias
	ErrQuotaExceeded  = errors.New("artifact quota exceeded")
)

const (
	DefaultMaxObjectBytes  int64 = 8 << 20
	DefaultMaxSessionBytes int64 = 256 << 20
	DefaultMaxStoreBytes   int64 = 1 << 30
	MaxBytes                     = DefaultMaxObjectBytes // compatibility alias
)

type RetentionTier string

const (
	RetentionEphemeral RetentionTier = "ephemeral"
	RetentionNormal    RetentionTier = "normal"
	RetentionPinned    RetentionTier = "pinned"

	DefaultEphemeralTTL = 24 * time.Hour
	DefaultNormalTTL    = 30 * 24 * time.Hour
	DefaultPinnedTTL    = 180 * 24 * time.Hour
	DefaultMaxPinnedTTL = 365 * 24 * time.Hour
)

type Scope struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
}

type Metadata struct {
	ID          string        `json:"id"`
	Scope       Scope         `json:"scope"`
	MediaType   string        `json:"media_type,omitempty"`
	Bytes       int64         `json:"bytes"`
	CreatedAt   time.Time     `json:"created_at"`
	ExpiresAt   *time.Time    `json:"expires_at,omitempty"`
	Retention   RetentionTier `json:"retention,omitempty"`
	Preview     string        `json:"preview,omitempty"`
	Truncated   bool          `json:"truncated"`
	PreviewUTF8 bool          `json:"preview_utf8"`
}

type PutOptions struct {
	Scope        Scope
	MediaType    string
	PreviewBytes int
	PreviewLines int
	Now          time.Time
	TTL          time.Duration
	Retention    RetentionTier
}

type Config struct {
	MaxObjectBytes  int64
	MaxSessionBytes int64
	MaxStoreBytes   int64
	EphemeralTTL    time.Duration
	NormalTTL       time.Duration
	PinnedTTL       time.Duration
	MaxPinnedTTL    time.Duration
}

type Store struct {
	root           string
	cfg            Config
	mu             sync.RWMutex
	puts           atomic.Uint64
	reads          atomic.Uint64
	quotaRejects   atomic.Uint64
	gcRuns         atomic.Uint64
	gcErrors       atomic.Uint64
	putBytes       atomic.Uint64
	readBytes      atomic.Uint64
	reclaimedBytes atomic.Uint64
	lastGCMu       sync.RWMutex
	lastGC         *GCStatus
}

type Usage struct {
	PhysicalBytes         int64                 `json:"physical_bytes"`
	ObjectCount           int                   `json:"object_count"`
	LogicalReferenceBytes int64                 `json:"logical_reference_bytes"`
	ReferenceCount        int                   `json:"reference_count"`
	MaxStoreBytes         int64                 `json:"max_store_bytes"`
	MaxSessionBytes       int64                 `json:"max_session_bytes"`
	MaxObjectBytes        int64                 `json:"max_object_bytes"`
	ReferencesByRetention map[RetentionTier]int `json:"references_by_retention"`
}

type Health struct {
	OK       bool   `json:"ok"`
	RootMode string `json:"root_mode,omitempty"`
	Usage    Usage  `json:"usage"`
	Error    string `json:"error,omitempty"`
}

func (s *Store) Health() Health {
	u, err := s.Usage()
	h := Health{OK: err == nil, Usage: u}
	if info, statErr := os.Stat(s.root); statErr != nil {
		if err == nil {
			err = statErr
		}
		h.OK = false
	} else {
		h.RootMode = fmt.Sprintf("%04o", info.Mode().Perm())
		if info.Mode().Perm()&0o077 != 0 {
			h.OK = false
			if err == nil {
				err = errors.New("artifact root is accessible by group or other users")
			}
		}
	}
	if err != nil {
		h.Error = err.Error()
	}
	if m := s.Metrics(); m.LastGC != nil && m.LastGC.Error != "" {
		h.OK = false
		h.Error = m.LastGC.Error
	}
	return h
}

type GCStatus struct {
	StartedAt  time.Time `json:"started_at"`
	DurationMS int64     `json:"duration_ms"`
	Result     GCResult  `json:"result"`
	Error      string    `json:"error,omitempty"`
}

type Metrics struct {
	Puts           uint64    `json:"puts"`
	Reads          uint64    `json:"reads"`
	QuotaRejects   uint64    `json:"quota_rejects"`
	GCRuns         uint64    `json:"gc_runs"`
	GCErrors       uint64    `json:"gc_errors"`
	PutBytes       uint64    `json:"put_bytes"`
	ReadBytes      uint64    `json:"read_bytes"`
	ReclaimedBytes uint64    `json:"reclaimed_bytes"`
	LastGC         *GCStatus `json:"last_gc,omitempty"`
}

// New initializes a store with safe defaults. A single optional Config may
// override non-zero limits.
func New(root string, configs ...Config) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("artifact: root is required")
	}
	if len(configs) > 1 {
		return nil, errors.New("artifact: at most one config is allowed")
	}
	cfg := Config{MaxObjectBytes: DefaultMaxObjectBytes, MaxSessionBytes: DefaultMaxSessionBytes, MaxStoreBytes: DefaultMaxStoreBytes, EphemeralTTL: DefaultEphemeralTTL, NormalTTL: DefaultNormalTTL, PinnedTTL: DefaultPinnedTTL, MaxPinnedTTL: DefaultMaxPinnedTTL}
	if len(configs) == 1 {
		if configs[0].MaxObjectBytes > 0 {
			cfg.MaxObjectBytes = configs[0].MaxObjectBytes
		}
		if configs[0].MaxSessionBytes > 0 {
			cfg.MaxSessionBytes = configs[0].MaxSessionBytes
		}
		if configs[0].MaxStoreBytes > 0 {
			cfg.MaxStoreBytes = configs[0].MaxStoreBytes
		}
		if configs[0].EphemeralTTL > 0 {
			cfg.EphemeralTTL = configs[0].EphemeralTTL
		}
		if configs[0].NormalTTL > 0 {
			cfg.NormalTTL = configs[0].NormalTTL
		}
		if configs[0].PinnedTTL > 0 {
			cfg.PinnedTTL = configs[0].PinnedTTL
		}
		if configs[0].MaxPinnedTTL > 0 {
			cfg.MaxPinnedTTL = configs[0].MaxPinnedTTL
		}
	}
	if cfg.MaxObjectBytes > cfg.MaxSessionBytes || cfg.MaxSessionBytes > cfg.MaxStoreBytes {
		return nil, errors.New("artifact: limits must satisfy object <= session <= store")
	}
	if cfg.EphemeralTTL > cfg.NormalTTL || cfg.NormalTTL > cfg.PinnedTTL || cfg.PinnedTTL > cfg.MaxPinnedTTL {
		return nil, errors.New("artifact: retention ttls must satisfy ephemeral <= normal <= pinned <= max pinned")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("artifact: resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("artifact: prepare root: %w", err)
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		return nil, fmt.Errorf("artifact: secure root: %w", err)
	}
	for _, dir := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(abs, dir), 0o700); err != nil {
			return nil, fmt.Errorf("artifact: prepare %s: %w", dir, err)
		}
	}
	return &Store{root: abs, cfg: cfg}, nil
}

func (s *Store) Put(raw []byte, opts PutOptions) (Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateScope(opts.Scope); err != nil {
		return Metadata{}, err
	}
	if opts.TTL < 0 {
		return Metadata{}, errors.New("artifact: ttl must be non-negative")
	}
	retentionTTL, err := s.retentionTTL(opts.Retention, opts.TTL)
	if err != nil {
		return Metadata{}, err
	}
	if int64(len(raw)) > s.cfg.MaxObjectBytes {
		return Metadata{}, fmt.Errorf("%w: bytes=%d limit=%d", ErrObjectTooLarge, len(raw), s.cfg.MaxObjectBytes)
	}
	sum := sha256.Sum256(raw)
	id := hex.EncodeToString(sum[:])
	if err := s.checkQuota(opts.Scope.SessionID, id, int64(len(raw))); err != nil {
		if errors.Is(err, ErrQuotaExceeded) {
			s.quotaRejects.Add(1)
		}
		return Metadata{}, err
	}
	if err := s.writeObject(id, raw); err != nil {
		return Metadata{}, err
	}
	now := opts.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var expiresAt *time.Time
	if retentionTTL > 0 {
		expires := now.Add(retentionTTL)
		expiresAt = &expires
	}
	preview, truncated, valid := makePreview(raw, opts.PreviewBytes, opts.PreviewLines)
	meta := Metadata{ID: id, Scope: opts.Scope, MediaType: strings.TrimSpace(opts.MediaType), Bytes: int64(len(raw)), CreatedAt: now, ExpiresAt: expiresAt, Retention: opts.Retention, Preview: preview, Truncated: truncated, PreviewUTF8: valid}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return Metadata{}, fmt.Errorf("artifact: encode metadata: %w", err)
	}
	if err := atomicWrite(s.refPath(opts.Scope, id), encoded, 0o600); err != nil {
		return Metadata{}, fmt.Errorf("artifact: write reference: %w", err)
	}
	s.puts.Add(1)
	s.putBytes.Add(uint64(len(raw)))
	return meta, nil
}

func (s *Store) Stat(scope Scope, id string) (Metadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stat(scope, id, time.Now().UTC())
}

func (s *Store) stat(scope Scope, id string, now time.Time) (Metadata, error) {
	if err := validateScope(scope); err != nil {
		return Metadata{}, err
	}
	if !validID(id) {
		return Metadata{}, errors.New("artifact: invalid id")
	}
	raw, err := os.ReadFile(s.refPath(scope, id))
	if os.IsNotExist(err) {
		return Metadata{}, ErrNotFound
	}
	if err != nil {
		return Metadata{}, fmt.Errorf("artifact: read metadata: %w", err)
	}
	var meta Metadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Metadata{}, fmt.Errorf("artifact: decode metadata: %w", err)
	}
	if meta.ID != id || meta.Scope != scope {
		return Metadata{}, ErrNotFound
	}
	if meta.ExpiresAt != nil && !now.Before(*meta.ExpiresAt) {
		return Metadata{}, ErrNotFound
	}
	return meta, nil
}

func (s *Store) Read(scope Scope, id string) ([]byte, Metadata, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	meta, err := s.stat(scope, id, time.Now().UTC())
	if err != nil {
		return nil, Metadata{}, err
	}
	raw, err := os.ReadFile(s.objectPath(id))
	if os.IsNotExist(err) {
		return nil, Metadata{}, ErrNotFound
	}
	if err != nil {
		return nil, Metadata{}, fmt.Errorf("artifact: read object: %w", err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != id || int64(len(raw)) != meta.Bytes {
		return nil, Metadata{}, errors.New("artifact: object integrity check failed")
	}
	s.reads.Add(1)
	s.readBytes.Add(uint64(len(raw)))
	return raw, meta, nil
}

type GCResult struct {
	ReferencesRemoved int   `json:"references_removed"`
	ObjectsRemoved    int   `json:"objects_removed"`
	BytesReclaimed    int64 `json:"bytes_reclaimed"`
}

// GC removes expired references followed by unreferenced content objects.
func (s *Store) GC(now time.Time) (GCResult, error) {
	started := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	defer func() { s.gcRuns.Add(1) }()
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	result := GCResult{}
	refs := make(map[string]bool)
	err := filepath.WalkDir(filepath.Join(s.root, "refs"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var meta Metadata
		if err := json.Unmarshal(raw, &meta); err != nil {
			return fmt.Errorf("artifact: decode metadata during gc: %w", err)
		}
		if meta.ExpiresAt != nil && !now.Before(*meta.ExpiresAt) {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			result.ReferencesRemoved++
			return nil
		}
		if validID(meta.ID) {
			refs[meta.ID] = true
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		s.recordGC(started, result, err)
		s.gcErrors.Add(1)
		return result, err
	}
	err = filepath.WalkDir(filepath.Join(s.root, "objects"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if refs[filepath.Base(path)] {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		result.ObjectsRemoved++
		result.BytesReclaimed += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		s.recordGC(started, result, err)
		s.gcErrors.Add(1)
		return result, err
	}
	s.recordGC(started, result, nil)
	s.reclaimedBytes.Add(uint64(result.BytesReclaimed))
	return result, nil
}

func (s *Store) recordGC(started time.Time, result GCResult, err error) {
	status := &GCStatus{StartedAt: started, DurationMS: time.Since(started).Milliseconds(), Result: result}
	if err != nil {
		status.Error = err.Error()
	}
	s.lastGCMu.Lock()
	s.lastGC = status
	s.lastGCMu.Unlock()
}

func (s *Store) Metrics() Metrics {
	s.lastGCMu.RLock()
	var last *GCStatus
	if s.lastGC != nil {
		copy := *s.lastGC
		last = &copy
	}
	s.lastGCMu.RUnlock()
	return Metrics{Puts: s.puts.Load(), Reads: s.reads.Load(), QuotaRejects: s.quotaRejects.Load(), GCRuns: s.gcRuns.Load(), GCErrors: s.gcErrors.Load(), PutBytes: s.putBytes.Load(), ReadBytes: s.readBytes.Load(), ReclaimedBytes: s.reclaimedBytes.Load(), LastGC: last}
}

func (s *Store) Usage() (Usage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := Usage{MaxStoreBytes: s.cfg.MaxStoreBytes, MaxSessionBytes: s.cfg.MaxSessionBytes, MaxObjectBytes: s.cfg.MaxObjectBytes, ReferencesByRetention: map[RetentionTier]int{}}
	err := filepath.WalkDir(filepath.Join(s.root, "objects"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		u.ObjectCount++
		u.PhysicalBytes += info.Size()
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return u, err
	}
	err = filepath.WalkDir(filepath.Join(s.root, "refs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var m Metadata
		if err = json.Unmarshal(raw, &m); err != nil {
			return err
		}
		u.ReferenceCount++
		u.LogicalReferenceBytes += m.Bytes
		u.ReferencesByRetention[m.Retention]++
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return u, err
	}
	return u, nil
}

func (s *Store) retentionTTL(tier RetentionTier, requested time.Duration) (time.Duration, error) {
	switch tier {
	case "":
		return requested, nil // legacy callers retain explicit-TTL semantics
	case RetentionEphemeral:
		if requested == 0 {
			return s.cfg.EphemeralTTL, nil
		}
		if requested > s.cfg.NormalTTL {
			return 0, errors.New("artifact: ephemeral ttl exceeds normal retention")
		}
		return requested, nil
	case RetentionNormal:
		if requested == 0 {
			return s.cfg.NormalTTL, nil
		}
		if requested > s.cfg.PinnedTTL {
			return 0, errors.New("artifact: normal ttl exceeds pinned retention")
		}
		return requested, nil
	case RetentionPinned:
		if requested == 0 {
			return s.cfg.PinnedTTL, nil
		}
		if requested > s.cfg.MaxPinnedTTL {
			return 0, errors.New("artifact: pinned ttl exceeds maximum")
		}
		return requested, nil
	default:
		return 0, fmt.Errorf("artifact: invalid retention tier %q", tier)
	}
}

// DeleteSessionRefs removes logical references owned by sessionID. Shared
// content objects remain until GC proves that no other scope references them.
func (s *Store) DeleteSessionRefs(sessionID string) (int, error) {
	if err := validateScope(Scope{SessionID: sessionID}); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root := filepath.Join(s.root, "refs", sessionID)
	count := 0
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && filepath.Ext(d.Name()) == ".json" {
			count++
		}
		return nil
	})
	if err := os.RemoveAll(root); err != nil {
		return 0, fmt.Errorf("artifact: delete session references: %w", err)
	}
	return count, nil
}

func (s *Store) checkQuota(sessionID, id string, objectBytes int64) error {
	storeBytes, objectExists, err := s.storeUsage(id)
	if err != nil {
		return err
	}
	if !objectExists && storeBytes+objectBytes > s.cfg.MaxStoreBytes {
		return fmt.Errorf("%w: store bytes=%d requested=%d limit=%d", ErrQuotaExceeded, storeBytes, objectBytes, s.cfg.MaxStoreBytes)
	}
	sessionBytes, sessionHasObject, err := s.sessionUsage(sessionID, id)
	if err != nil {
		return err
	}
	if !sessionHasObject && sessionBytes+objectBytes > s.cfg.MaxSessionBytes {
		return fmt.Errorf("%w: session bytes=%d requested=%d limit=%d", ErrQuotaExceeded, sessionBytes, objectBytes, s.cfg.MaxSessionBytes)
	}
	return nil
}

func (s *Store) storeUsage(wantedID string) (int64, bool, error) {
	var total int64
	found := false
	err := filepath.WalkDir(filepath.Join(s.root, "objects"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		if filepath.Base(path) == wantedID {
			found = true
		}
		return nil
	})
	return total, found, err
}

func (s *Store) sessionUsage(sessionID, wantedID string) (int64, bool, error) {
	ids := make(map[string]int64)
	err := filepath.WalkDir(filepath.Join(s.root, "refs", sessionID), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var meta Metadata
		if err := json.Unmarshal(raw, &meta); err != nil {
			return err
		}
		ids[meta.ID] = meta.Bytes
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		return 0, false, err
	}
	var total int64
	for _, size := range ids {
		total += size
	}
	_, found := ids[wantedID]
	return total, found, nil
}

func (s *Store) writeObject(id string, raw []byte) error {
	path := s.objectPath(id)
	if info, err := os.Stat(path); err == nil {
		if info.Size() != int64(len(raw)) {
			return errors.New("artifact: existing object size mismatch")
		}
		existing, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("artifact: verify existing object: %w", err)
		}
		sum := sha256.Sum256(existing)
		if hex.EncodeToString(sum[:]) != id {
			return errors.New("artifact: existing object integrity check failed")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("artifact: stat object: %w", err)
	}
	if err := atomicWrite(path, raw, 0o600); err != nil {
		return fmt.Errorf("artifact: write object: %w", err)
	}
	return nil
}

func (s *Store) objectPath(id string) string { return filepath.Join(s.root, "objects", id[:2], id) }
func (s *Store) refPath(scope Scope, id string) string {
	return filepath.Join(s.root, "refs", scope.SessionID, emptySegment(scope.TaskID), emptySegment(scope.CallID), id+".json")
}
func emptySegment(v string) string {
	if v == "" {
		return "_"
	}
	return v
}

func validateScope(scope Scope) error {
	if scope.SessionID == "" {
		return errors.New("artifact: session id is required")
	}
	for name, value := range map[string]string{"session id": scope.SessionID, "task id": scope.TaskID, "call id": scope.CallID} {
		if value != "" && (filepath.Base(value) != value || value == "." || value == ".." || strings.ContainsAny(value, `/\\`)) {
			return fmt.Errorf("artifact: invalid %s", name)
		}
	}
	return nil
}

func validID(id string) bool {
	if len(id) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func atomicWrite(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".artifact-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// makePreview builds a bounded preview of raw content for Metadata.Preview.
// When the content fits within maxBytes/maxLines it is returned verbatim.
// When it must be cut, the preview keeps BOTH ends — a head slice and a tail
// slice, separated by an elision marker naming how much was skipped — rather
// than a head-only cut. Command output commonly carries its most actionable
// signal (a build's final error, a test summary) at the tail, so a head-only
// preview silently drops exactly the part a model most needs to see next;
// keeping both ends lets the truncated preview alone answer "did this
// succeed" for the common case, with the full object still available via
// Store.Read for anything the preview elides.
func makePreview(raw []byte, maxBytes, maxLines int) (string, bool, bool) {
	if !utf8.Valid(raw) {
		return "", len(raw) > 0, false
	}
	if maxBytes <= 0 && maxLines <= 0 {
		return "", len(raw) > 0, true
	}
	const noLimit = -1 // internal sentinel distinct from "budget of zero"
	headEnd := len(raw)
	if maxLines > 0 {
		if idx := nthIndex(raw, '\n', maxLines); idx >= 0 && idx+1 < headEnd {
			headEnd = idx + 1
		}
	}
	if maxBytes > 0 && maxBytes < headEnd {
		headEnd = alignUTF8End(raw, maxBytes)
	}
	if headEnd >= len(raw) {
		// Fits within budget: no truncation, no split needed.
		return string(raw), false, true
	}
	headBytes, headLines := noLimit, noLimit
	if maxLines > 0 {
		headLines = (maxLines + 1) / 2 // head gets the larger half on odd splits
	}
	if maxBytes > 0 {
		headBytes = (maxBytes + 1) / 2
	}
	head := headBudget(raw, headBytes, headLines)

	tailBytes, tailLines := noLimit, noLimit
	if maxLines > 0 {
		tailLines = maxLines - headLines
	}
	if maxBytes > 0 {
		tailBytes = maxBytes - headBytes
	}
	tail := tailBudget(raw, tailBytes, tailLines, len(head))

	if len(tail) == 0 {
		// No room left for a tail slice (tiny budgets): fall back to a
		// head-only preview rather than emit a marker with nothing after it.
		return head, true, true
	}
	omitted := len(raw) - len(head) - len(tail)
	if omitted <= 0 {
		// Head and tail already cover (or overlap) the whole object.
		return string(raw), false, true
	}
	marker := fmt.Sprintf("\n… [%d bytes omitted] …\n", omitted)
	if maxBytes > 0 && len(head)+len(marker)+len(tail) > maxBytes {
		// The elision marker is part of the byte budget. Reallocate what
		// remains between both ends; if the budget cannot hold a marker and
		// meaningful content on both sides, prefer a truthful head-only cut.
		available := maxBytes - len(marker)
		if available < 2 {
			return headBudget(raw, maxBytes, headLines), true, true
		}
		for range 3 { // omitted's decimal width can change after reallocation
			head = headBudget(raw, (available+1)/2, headLines)
			tail = tailBudget(raw, available/2, tailLines, len(head))
			if len(tail) == 0 {
				return headBudget(raw, maxBytes, headLines), true, true
			}
			omitted = len(raw) - len(head) - len(tail)
			marker = fmt.Sprintf("\n… [%d bytes omitted] …\n", omitted)
			available = maxBytes - len(marker)
			if available < 2 {
				return headBudget(raw, maxBytes, headLines), true, true
			}
		}
	}
	return head + marker + tail, true, true
}

// Preview returns the same bounded, UTF-8-safe head+tail projection stored in
// artifact metadata. Callers that need a model-facing projection can reuse
// the retention-tested truncation contract without duplicating it.
func Preview(raw []byte, maxBytes, maxLines int) (string, bool, bool) {
	return makePreview(raw, maxBytes, maxLines)
}

// headBudget returns the leading slice of raw bounded by maxBytes and
// maxLines, each either >=0 (an explicit budget, 0 meaning "show nothing")
// or -1 meaning "no limit on this dimension". UTF-8-boundary-safe.
func headBudget(raw []byte, maxBytes, maxLines int) string {
	end := len(raw)
	if maxLines >= 0 {
		if maxLines == 0 {
			return ""
		}
		if idx := nthIndex(raw, '\n', maxLines); idx >= 0 && idx+1 < end {
			end = idx + 1
		}
	}
	if maxBytes >= 0 && maxBytes < end {
		end = alignUTF8End(raw, maxBytes)
	}
	return string(raw[:end])
}

// tailBudget returns the trailing slice of raw bounded by maxBytes and
// maxLines (same >=0/-1 convention as headBudget), never reaching back past
// headLen so head and tail cannot overlap.
func tailBudget(raw []byte, maxBytes, maxLines, headLen int) string {
	if maxBytes == 0 || maxLines == 0 {
		return ""
	}
	start := headLen
	if maxLines > 0 {
		if idx := tailLineStart(raw, maxLines); idx > start {
			start = idx
		}
	}
	if maxBytes >= 0 && len(raw)-start > maxBytes {
		start = len(raw) - maxBytes
		for start < len(raw) && !utf8.RuneStart(raw[start]) {
			start++
		}
	}
	if start >= len(raw) || start < headLen {
		return ""
	}
	return string(raw[start:])
}

// alignUTF8End walks end back to the nearest rune boundary at or before n.
func alignUTF8End(raw []byte, n int) int {
	end := n
	if end > len(raw) {
		end = len(raw)
	}
	for end > 0 && !utf8.RuneStart(raw[end]) {
		end--
	}
	return end
}

func nthIndex(raw []byte, needle byte, n int) int {
	offset := 0
	for range n {
		i := bytes.IndexByte(raw[offset:], needle)
		if i < 0 {
			return -1
		}
		offset += i + 1
	}
	return offset - 1
}

// tailLineStart returns the byte offset where the Nth line from the end of
// raw begins (n=1 -> the last line). A trailing '\n' terminates the last
// line rather than introducing an extra empty one, so
// tailLineStart("a\nb\n", 1) == 2 (start of "b\n"), not len(raw). Returns 0
// if raw has fewer than n lines (the whole input is the tail).
func tailLineStart(raw []byte, n int) int {
	end := len(raw)
	if end > 0 && raw[end-1] == '\n' {
		end--
	}
	offset := end
	for range n {
		idx := bytes.LastIndexByte(raw[:offset], '\n')
		if idx < 0 {
			return 0
		}
		offset = idx
	}
	return offset + 1
}

func PutReader(s *Store, r io.Reader, opts PutOptions) (Metadata, error) {
	if s == nil {
		return Metadata{}, errors.New("artifact: store is required")
	}
	limited := &io.LimitedReader{R: r, N: s.cfg.MaxObjectBytes + 1}
	raw, err := io.ReadAll(limited)
	if err != nil {
		return Metadata{}, fmt.Errorf("artifact: read input: %w", err)
	}
	if int64(len(raw)) > s.cfg.MaxObjectBytes {
		return Metadata{}, fmt.Errorf("%w: limit=%d", ErrObjectTooLarge, s.cfg.MaxObjectBytes)
	}
	return s.Put(raw, opts)
}
