package localruntime

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/Nebutra/carina/go/config"
)

const DefaultIdleGrace = 5 * time.Minute

type Resolution struct {
	Workspace  Workspace
	Spec       Spec
	Config     config.Config
	Locks      *config.LockReport
	Provenance config.Provenance
}

type Overrides struct {
	Socket   string
	StateDir string
}

// Resolve establishes workspace identity before config and runtime paths. It
// replaces only un-overridden global socket/state defaults in workspace mode.
func Resolve(home, workspaceRoot string, mode Mode) (Resolution, error) {
	return ResolveWithManaged(home, workspaceRoot, mode, config.DefaultManagedPath())
}

func ResolveWithManaged(home, workspaceRoot string, mode Mode, managedPath string) (Resolution, error) {
	workspace, err := ResolveWorkspace(workspaceRoot)
	if err != nil {
		return Resolution{}, err
	}
	resolved, err := config.LoadResolvedWithManaged(home, workspace.CanonicalRoot, managedPath)
	if err != nil {
		return Resolution{}, err
	}
	if mode == "" {
		mode = ModeWorkspace
	}
	paths := DefaultPaths(home, workspace)
	if mode == ModeWorkspace {
		if resolved.Provenance.KeySources["socket"] == "default" {
			resolved.Config.Socket = paths.SocketPath
			resolved.Provenance.KeySources["socket"] = "workspace_default"
		}
		if resolved.Provenance.KeySources["state_dir"] == "default" {
			resolved.Config.StateDir = paths.StateDir
			resolved.Provenance.KeySources["state_dir"] = "workspace_default"
		}
	}
	paths.SocketPath = resolved.Config.Socket
	paths.StateDir = resolved.Config.StateDir
	fingerprint, err := config.Fingerprint(resolved.Config)
	if err != nil {
		return Resolution{}, err
	}
	sources := make(map[string]string, len(resolved.Provenance.KeySources))
	for key, source := range resolved.Provenance.KeySources {
		sources[key] = source
	}
	spec, err := EnsureSpec(home, workspace, SpecOptions{
		Mode: mode, Paths: paths, IdleGrace: DefaultIdleGrace,
		Config: ConfigIdentity{Fingerprint: fingerprint, Sources: sources},
	})
	if err != nil {
		return Resolution{}, fmt.Errorf("localruntime: resolve spec: %w", err)
	}
	return Resolution{
		Workspace: workspace, Spec: spec, Config: resolved.Config,
		Locks: resolved.Locks, Provenance: resolved.Provenance,
	}, nil
}

// ApplyOverrides applies explicit CLI topology flags to an existing
// resolution and persists the resulting coherent spec.
func ApplyOverrides(home string, resolution Resolution, overrides Overrides) (Resolution, error) {
	if overrides.Socket != "" {
		socket, err := filepath.Abs(overrides.Socket)
		if err != nil {
			return Resolution{}, err
		}
		resolution.Config.Socket = socket
		resolution.Provenance.KeySources["socket"] = "explicit_flag"
		resolution.Spec.Paths.SocketPath = socket
	}
	if overrides.StateDir != "" {
		stateDir, err := filepath.Abs(overrides.StateDir)
		if err != nil {
			return Resolution{}, err
		}
		resolution.Config.StateDir = stateDir
		resolution.Provenance.KeySources["state_dir"] = "explicit_flag"
		resolution.Spec.Paths.StateDir = stateDir
	}
	fingerprint, err := config.Fingerprint(resolution.Config)
	if err != nil {
		return Resolution{}, err
	}
	sources := make(map[string]string, len(resolution.Provenance.KeySources))
	for key, source := range resolution.Provenance.KeySources {
		sources[key] = source
	}
	spec, err := EnsureSpec(home, resolution.Workspace, SpecOptions{
		Mode: resolution.Spec.Mode, Paths: resolution.Spec.Paths,
		Config:     ConfigIdentity{Fingerprint: fingerprint, Sources: sources},
		Executable: resolution.Spec.Executable,
		IdleGrace:  time.Duration(resolution.Spec.IdleGraceMS) * time.Millisecond,
	})
	if err != nil {
		return Resolution{}, fmt.Errorf("localruntime: apply overrides: %w", err)
	}
	resolution.Spec = spec
	return resolution, nil
}
