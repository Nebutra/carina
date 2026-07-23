package localruntime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const runtimeLayoutVersion = "v1"

// Paths contains every default filesystem path owned by one workspace runtime.
type Paths struct {
	RuntimeDir     string `json:"runtime_dir"`
	SpecPath       string `json:"spec_path"`
	DescriptorPath string `json:"descriptor_path"`
	OwnerPath      string `json:"owner_path"`
	StartLockPath  string `json:"start_lock_path"`
	SocketPath     string `json:"socket_path"`
	LogPath        string `json:"log_path"`
	StateDir       string `json:"state_dir"`
}

// RuntimeRoot returns the passive registry root for a user home directory.
func RuntimeRoot(home string) string {
	return filepath.Join(home, ".carina", "runtimes", runtimeLayoutVersion)
}

// DefaultPaths derives the private runtime layout from a stable workspace ID.
func DefaultPaths(home string, workspace Workspace) Paths {
	root := filepath.Join(RuntimeRoot(home), workspace.ID)
	socketKey := strings.TrimPrefix(workspace.ID, "ws1_")
	if len(socketKey) > 24 {
		socketKey = socketKey[:24]
	}
	socket := filepath.Join(home, ".carina", "run", runtimeLayoutVersion, "rt1_"+socketKey+".sock")
	return Paths{
		RuntimeDir:     root,
		SpecPath:       filepath.Join(root, "spec.json"),
		DescriptorPath: filepath.Join(root, "descriptor.json"),
		OwnerPath:      filepath.Join(root, "owner.json"),
		StartLockPath:  filepath.Join(root, "start.lock"),
		SocketPath:     socket,
		LogPath:        filepath.Join(root, "runtime.log"),
		StateDir:       filepath.Join(root, "state"),
	}
}

func (p Paths) validate() error {
	for name, path := range map[string]string{
		"runtime_dir": p.RuntimeDir, "spec_path": p.SpecPath,
		"descriptor_path": p.DescriptorPath, "owner_path": p.OwnerPath,
		"start_lock_path": p.StartLockPath, "socket_path": p.SocketPath,
		"log_path": p.LogPath, "state_dir": p.StateDir,
	} {
		if path == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("localruntime: %s must be an absolute path", name)
		}
	}
	for name, path := range map[string]string{
		"spec_path": p.SpecPath, "descriptor_path": p.DescriptorPath,
		"owner_path": p.OwnerPath, "start_lock_path": p.StartLockPath,
		"log_path": p.LogPath,
	} {
		if err := ensureWithin(p.RuntimeDir, path); err != nil {
			return fmt.Errorf("localruntime: %s: %w", name, err)
		}
	}
	return nil
}

func ensureWithin(root, path string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return errors.New("path escapes runtime directory")
	}
	return nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("localruntime: unsafe runtime directory %s", path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return nil
}
