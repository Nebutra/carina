// Package artifact provides a scoped, content-addressed store for tool output.
package artifact

import (
	"bytes"
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
	"time"
	"unicode/utf8"
)

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

type Scope struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id,omitempty"`
	CallID    string `json:"call_id,omitempty"`
}

type Metadata struct {
	ID          string     `json:"id"`
	Scope       Scope      `json:"scope"`
	MediaType   string     `json:"media_type,omitempty"`
	Bytes       int64      `json:"bytes"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	Preview     string     `json:"preview,omitempty"`
	Truncated   bool       `json:"truncated"`
	PreviewUTF8 bool       `json:"preview_utf8"`
}

type PutOptions struct {
	Scope        Scope
	MediaType    string
	PreviewBytes int
	PreviewLines int
	Now          time.Time
	TTL          time.Duration
}

type Config struct {
	MaxObjectBytes  int64
	MaxSessionBytes int64
	MaxStoreBytes   int64
}

type Store struct {
	root string
	cfg  Config
	mu   sync.RWMutex
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
	cfg := Config{MaxObjectBytes: DefaultMaxObjectBytes, MaxSessionBytes: DefaultMaxSessionBytes, MaxStoreBytes: DefaultMaxStoreBytes}
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
	}
	if cfg.MaxObjectBytes > cfg.MaxSessionBytes || cfg.MaxSessionBytes > cfg.MaxStoreBytes {
		return nil, errors.New("artifact: limits must satisfy object <= session <= store")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("artifact: resolve root: %w", err)
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
	if int64(len(raw)) > s.cfg.MaxObjectBytes {
		return Metadata{}, fmt.Errorf("%w: bytes=%d limit=%d", ErrObjectTooLarge, len(raw), s.cfg.MaxObjectBytes)
	}
	sum := sha256.Sum256(raw)
	id := hex.EncodeToString(sum[:])
	if err := s.checkQuota(opts.Scope.SessionID, id, int64(len(raw))); err != nil {
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
	if opts.TTL > 0 {
		expires := now.Add(opts.TTL)
		expiresAt = &expires
	}
	preview, truncated, valid := makePreview(raw, opts.PreviewBytes, opts.PreviewLines)
	meta := Metadata{ID: id, Scope: opts.Scope, MediaType: strings.TrimSpace(opts.MediaType), Bytes: int64(len(raw)), CreatedAt: now, ExpiresAt: expiresAt, Preview: preview, Truncated: truncated, PreviewUTF8: valid}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return Metadata{}, fmt.Errorf("artifact: encode metadata: %w", err)
	}
	if err := atomicWrite(s.refPath(opts.Scope, id), encoded, 0o600); err != nil {
		return Metadata{}, fmt.Errorf("artifact: write reference: %w", err)
	}
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
	return raw, meta, nil
}

type GCResult struct {
	ReferencesRemoved int   `json:"references_removed"`
	ObjectsRemoved    int   `json:"objects_removed"`
	BytesReclaimed    int64 `json:"bytes_reclaimed"`
}

// GC removes expired references followed by unreferenced content objects.
func (s *Store) GC(now time.Time) (GCResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
		return result, err
	}
	return result, nil
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

func makePreview(raw []byte, maxBytes, maxLines int) (string, bool, bool) {
	if !utf8.Valid(raw) {
		return "", len(raw) > 0, false
	}
	if maxBytes <= 0 && maxLines <= 0 {
		return "", len(raw) > 0, true
	}
	end := len(raw)
	if maxLines > 0 {
		if idx := nthIndex(raw, '\n', maxLines); idx >= 0 && idx+1 < end {
			end = idx + 1
		}
	}
	if maxBytes > 0 && maxBytes < end {
		end = maxBytes
		for end > 0 && !utf8.RuneStart(raw[end]) {
			end--
		}
	}
	return string(raw[:end]), end < len(raw), true
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
