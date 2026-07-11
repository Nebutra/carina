// Package worktree manages git worktrees as governed session isolation units.
package worktree

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

type Record struct {
	ID        string    `json:"id"`
	RepoRoot  string    `json:"repo_root"`
	Path      string    `json:"path"`
	BaseRef   string    `json:"base_ref"`
	Commit    string    `json:"commit"`
	Branch    string    `json:"branch,omitempty"`
	Owner     string    `json:"owner,omitempty"`
	LockedBy  string    `json:"locked_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type Manager struct {
	mu   sync.Mutex
	root string
}

func New(stateDir string) (*Manager, error) {
	root, err := filepath.Abs(filepath.Join(stateDir, "worktrees"))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("worktree: create state: %w", err)
	}
	return &Manager{root: root}, nil
}

func (m *Manager) Create(id, repoRoot, baseRef, branch, owner string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !validID.MatchString(id) {
		return nil, fmt.Errorf("worktree: invalid id %q", id)
	}
	repo, err := canonicalRepo(repoRoot)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(baseRef) == "" {
		baseRef = "HEAD"
	}
	commit, err := gitOutput(repo, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("worktree: invalid base ref %q: %w", baseRef, err)
	}
	path := filepath.Join(m.root, id)
	if err := ensureWithin(m.root, path); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("worktree: id %q already exists", id)
	}
	args := []string{"worktree", "add", "--detach", path, commit}
	if branch != "" {
		args = []string{"worktree", "add", "-b", branch, path, commit}
	}
	if _, err := gitOutput(repo, args...); err != nil {
		return nil, fmt.Errorf("worktree: add: %w", err)
	}
	rec := &Record{ID: id, RepoRoot: repo, Path: path, BaseRef: baseRef, Commit: commit, Branch: branch, Owner: owner, CreatedAt: time.Now().UTC()}
	if err := m.persist(rec); err != nil {
		_, _ = gitOutput(repo, "worktree", "remove", "--force", path)
		return nil, err
	}
	return rec, nil
}

func (m *Manager) Get(id string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.load(id)
}

func (m *Manager) List() ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		rec, err := m.load(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			out = append(out, *rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Manager) Lock(id, owner string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, errors.New("worktree: lock owner required")
	}
	rec, err := m.load(id)
	if err != nil {
		return nil, err
	}
	if rec.LockedBy != "" && rec.LockedBy != owner {
		return nil, fmt.Errorf("worktree: %s locked by %s", id, rec.LockedBy)
	}
	rec.LockedBy = owner
	return rec, m.persist(rec)
}

func (m *Manager) Unlock(id, owner string) (*Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, err := m.load(id)
	if err != nil {
		return nil, err
	}
	if rec.LockedBy != "" && rec.LockedBy != owner {
		return nil, fmt.Errorf("worktree: %s locked by %s", id, rec.LockedBy)
	}
	rec.LockedBy = ""
	return rec, m.persist(rec)
}

func (m *Manager) Cleanup(id, owner string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, err := m.load(id)
	if err != nil {
		return err
	}
	if err := ensureWithin(m.root, rec.Path); err != nil {
		return err
	}
	if rec.LockedBy != "" && rec.LockedBy != owner {
		return fmt.Errorf("worktree: %s locked by %s", id, rec.LockedBy)
	}
	status, err := gitOutput(rec.Path, "status", "--porcelain")
	if err != nil && !force {
		return fmt.Errorf("worktree: inspect changes: %w", err)
	}
	if status != "" && !force {
		return errors.New("worktree: uncommitted changes; use force to discard")
	}
	args := []string{"worktree", "remove", rec.Path}
	if force {
		args = append(args, "--force")
	}
	if _, err := gitOutput(rec.RepoRoot, args...); err != nil {
		return fmt.Errorf("worktree: remove: %w", err)
	}
	return os.Remove(m.metaPath(id))
}

func (m *Manager) load(id string) (*Record, error) {
	if !validID.MatchString(id) {
		return nil, fmt.Errorf("worktree: invalid id %q", id)
	}
	raw, err := os.ReadFile(m.metaPath(id))
	if err != nil {
		return nil, fmt.Errorf("worktree: get %s: %w", id, err)
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("worktree: corrupt metadata: %w", err)
	}
	if rec.ID != id {
		return nil, errors.New("worktree: metadata id mismatch")
	}
	if err := ensureWithin(m.root, rec.Path); err != nil {
		return nil, err
	}
	return &rec, nil
}

func (m *Manager) persist(rec *Record) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.metaPath(rec.ID) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.metaPath(rec.ID))
}
func (m *Manager) metaPath(id string) string { return filepath.Join(m.root, id+".json") }

func canonicalRepo(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("worktree: repo root: %w", err)
	}
	top, err := gitOutput(real, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("worktree: not a git repository: %w", err)
	}
	top, err = filepath.EvalSymlinks(top)
	if err != nil {
		return "", err
	}
	return top, nil
}
func ensureWithin(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return errors.New("worktree: path escapes managed root")
	}
	return nil
}
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(raw)), err)
	}
	return strings.TrimSpace(string(raw)), nil
}
