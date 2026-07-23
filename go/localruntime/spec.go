package localruntime

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SpecVersion = 1

type Mode string

const (
	ModeWorkspace Mode = "workspace"
	ModeLegacy    Mode = "legacy"
	ModeExternal  Mode = "external"
)

func (m Mode) valid() bool {
	return m == ModeWorkspace || m == ModeLegacy || m == ModeExternal
}

type ConfigIdentity struct {
	Fingerprint string            `json:"fingerprint,omitempty"`
	Sources     map[string]string `json:"sources,omitempty"`
}

type ExecutableIdentity struct {
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
}

// Spec is the authoritative launch identity passed to carina-daemon.
type Spec struct {
	Version     int                `json:"version"`
	Mode        Mode               `json:"mode"`
	Workspace   Workspace          `json:"workspace"`
	RuntimeID   string             `json:"runtime_id"`
	Paths       Paths              `json:"paths"`
	Config      ConfigIdentity     `json:"config,omitempty"`
	Executable  ExecutableIdentity `json:"executable,omitempty"`
	IdleGraceMS int64              `json:"idle_grace_ms,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

type SpecOptions struct {
	Mode       Mode
	Paths      Paths
	Config     ConfigIdentity
	Executable ExecutableIdentity
	IdleGrace  time.Duration
}

// EnsureSpec loads or creates a workspace spec, preserving RuntimeID across
// config/path updates. Callers must coordinate concurrent creation with the
// runtime start lock once process lifecycle is enabled.
func EnsureSpec(home string, workspace Workspace, opts SpecOptions) (Spec, error) {
	mode := opts.Mode
	if mode == "" {
		mode = ModeWorkspace
	}
	paths := opts.Paths
	if paths.RuntimeDir == "" {
		paths = DefaultPaths(home, workspace)
	}
	if err := paths.validate(); err != nil {
		return Spec{}, err
	}
	if err := ensurePrivateDir(paths.RuntimeDir); err != nil {
		return Spec{}, fmt.Errorf("localruntime: create runtime dir: %w", err)
	}
	if err := ensurePrivateDir(paths.StateDir); err != nil {
		return Spec{}, fmt.Errorf("localruntime: create state dir: %w", err)
	}

	now := time.Now().UTC()
	spec, err := LoadSpec(paths.SpecPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Spec{}, err
	}
	if errors.Is(err, os.ErrNotExist) {
		spec = Spec{
			Version: SpecVersion, Mode: mode, Workspace: workspace,
			CreatedAt: now,
		}
		spec.RuntimeID, err = newRuntimeID()
		if err != nil {
			return Spec{}, err
		}
	} else if spec.Workspace.ID != workspace.ID || spec.Workspace.CanonicalRoot != workspace.CanonicalRoot {
		return Spec{}, fmt.Errorf("localruntime: spec workspace mismatch: expected %s %q, observed %s %q",
			workspace.ID, workspace.CanonicalRoot, spec.Workspace.ID, spec.Workspace.CanonicalRoot)
	}
	if spec.Mode != mode {
		return Spec{}, fmt.Errorf("localruntime: spec mode mismatch: expected %s, observed %s", mode, spec.Mode)
	}

	spec.Version = SpecVersion
	spec.Workspace = workspace
	spec.Paths = paths
	spec.Config = cloneConfigIdentity(opts.Config)
	spec.Executable = opts.Executable
	spec.IdleGraceMS = opts.IdleGrace.Milliseconds()
	spec.UpdatedAt = now
	if err := WriteSpec(paths.SpecPath, spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func (s Spec) Validate() error {
	if s.Version != SpecVersion {
		return fmt.Errorf("localruntime: unsupported spec version %d", s.Version)
	}
	if !s.Mode.valid() {
		return fmt.Errorf("localruntime: invalid runtime mode %q", s.Mode)
	}
	if s.Workspace.ID == "" || s.Workspace.CanonicalRoot == "" {
		return errors.New("localruntime: workspace identity is required")
	}
	if want := WorkspaceID(s.Workspace.CanonicalRoot); s.Workspace.ID != want {
		return fmt.Errorf("localruntime: workspace id mismatch: got %s, want %s", s.Workspace.ID, want)
	}
	if !strings.HasPrefix(s.RuntimeID, "runtime_") {
		return fmt.Errorf("localruntime: invalid runtime id %q", s.RuntimeID)
	}
	if err := s.Paths.validate(); err != nil {
		return err
	}
	if s.IdleGraceMS < 0 {
		return errors.New("localruntime: idle grace cannot be negative")
	}
	return nil
}

func LoadSpec(path string) (Spec, error) {
	var spec Spec
	if err := readPrivateJSON(path, &spec); err != nil {
		return Spec{}, err
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func WriteSpec(path string, spec Spec) error {
	if err := spec.Validate(); err != nil {
		return err
	}
	return writePrivateJSONAtomic(path, spec)
}

func newRuntimeID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("localruntime: random runtime id: %w", err)
	}
	return "runtime_" + hex.EncodeToString(raw), nil
}

func cloneConfigIdentity(in ConfigIdentity) ConfigIdentity {
	out := ConfigIdentity{Fingerprint: in.Fingerprint}
	if len(in.Sources) > 0 {
		out.Sources = make(map[string]string, len(in.Sources))
		for key, value := range in.Sources {
			out.Sources[key] = value
		}
	}
	return out
}

func readPrivateJSON(path string, dst any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("localruntime: unsafe metadata file %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("localruntime: metadata file %s must be private", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("localruntime: decode %s: %w", path, err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("localruntime: decode %s: trailing data", path)
	}
	return nil
}

func writePrivateJSONAtomic(path string, value any) error {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("localruntime: encode %s: %w", path, err)
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
