// Package localruntime resolves workspace-scoped local runtime identity and
// metadata. It deliberately does not spawn or signal processes; process
// lifecycle remains owned by go/localdaemon.
package localruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const workspaceIDSalt = "carina-workspace-v1\x00"

// Workspace is the stable local identity derived from one canonical root.
type Workspace struct {
	ID            string `json:"workspace_id"`
	CanonicalRoot string `json:"canonical_root"`
	DisplayName   string `json:"display_name"`
}

// ResolveWorkspace resolves an explicit path, or discovers the nearest
// workspace marker from cwd. A project config marker intentionally takes the
// same role as a Git root, allowing nested Carina workspaces.
func ResolveWorkspace(input string) (Workspace, error) {
	explicit := strings.TrimSpace(input) != ""
	candidate := strings.TrimSpace(input)
	if candidate == "" {
		var err error
		candidate, err = os.Getwd()
		if err != nil {
			return Workspace{}, fmt.Errorf("localruntime: resolve cwd: %w", err)
		}
	}

	root, err := canonicalDirectory(candidate)
	if err != nil {
		if explicit {
			return Workspace{}, fmt.Errorf("localruntime: workspace %q: %w", candidate, err)
		}
		return Workspace{}, fmt.Errorf("localruntime: cwd workspace: %w", err)
	}
	if !explicit {
		root = nearestWorkspaceMarker(root)
	}

	return Workspace{
		ID:            WorkspaceID(root),
		CanonicalRoot: root,
		DisplayName:   workspaceDisplayName(root),
	}, nil
}

// DiscoverWorkspace resolves path as a launch location and walks upward to
// the nearest workspace marker, matching cwd-based resolution.
func DiscoverWorkspace(path string) (Workspace, error) {
	root, err := canonicalDirectory(path)
	if err != nil {
		return Workspace{}, fmt.Errorf("localruntime: discover workspace %q: %w", path, err)
	}
	root = nearestWorkspaceMarker(root)
	return Workspace{ID: WorkspaceID(root), CanonicalRoot: root, DisplayName: workspaceDisplayName(root)}, nil
}

// WorkspaceID returns the versioned, deterministic ID for a canonical root.
func WorkspaceID(canonicalRoot string) string {
	sum := sha256.Sum256([]byte(workspaceIDSalt + filepath.Clean(canonicalRoot)))
	return "ws1_" + hex.EncodeToString(sum[:])
}

func canonicalDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(abs))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return filepath.Clean(real), nil
}

func nearestWorkspaceMarker(start string) string {
	for dir := start; ; dir = filepath.Dir(dir) {
		if hasWorkspaceMarker(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
	}
}

func hasWorkspaceMarker(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, ".carina", "config.json")); err == nil && info.Mode().IsRegular() {
		return true
	}
	info, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil && (info.IsDir() || info.Mode().IsRegular())
}

func workspaceDisplayName(root string) string {
	name := filepath.Base(filepath.Clean(root))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return root
	}
	return name
}
