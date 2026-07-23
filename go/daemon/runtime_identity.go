package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func validateRuntimeSpec(input *localruntime.Spec, stateDir string) (*localruntime.Spec, error) {
	if input == nil {
		return nil, nil
	}
	spec := *input
	spec.Config.Sources = cloneStringMap(input.Config.Sources)
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("runtime spec: %w", err)
	}
	if !samePath(spec.Paths.StateDir, stateDir) {
		return nil, fmt.Errorf("runtime state mismatch: spec %q, daemon %q", spec.Paths.StateDir, stateDir)
	}
	return &spec, nil
}

func samePath(left, right string) bool {
	canonical := func(path string) string {
		abs, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(abs)
	}
	return canonical(left) == canonical(right)
}

func validateRuntimeSessions(spec *localruntime.Spec, sessions []*sessionstore.Session) error {
	if spec == nil || spec.Mode != localruntime.ModeWorkspace {
		return nil
	}
	for _, sess := range sessions {
		workspace, err := localruntime.ResolveWorkspace(sess.WorkspaceRoot)
		if err != nil {
			return fmt.Errorf("session %s workspace %q cannot be resolved: %w", sess.SessionID, sess.WorkspaceRoot, err)
		}
		if workspace.ID != spec.Workspace.ID || workspace.CanonicalRoot != spec.Workspace.CanonicalRoot || sess.WorkspaceID != spec.Workspace.ID {
			return fmt.Errorf(
				"session %s workspace mismatch: expected %s %q, observed %s %q (row workspace_id %s)",
				sess.SessionID, spec.Workspace.ID, spec.Workspace.CanonicalRoot,
				workspace.ID, workspace.CanonicalRoot, sess.WorkspaceID,
			)
		}
	}
	return nil
}

func (d *Daemon) validateSessionWorkspace(root string) (string, error) {
	if d.runtimeSpec == nil || d.runtimeSpec.Mode != localruntime.ModeWorkspace {
		return root, nil
	}
	workspace, err := localruntime.DiscoverWorkspace(root)
	if err != nil {
		return "", fmt.Errorf("workspace_root: %w", err)
	}
	if workspace.ID != d.runtimeSpec.Workspace.ID || workspace.CanonicalRoot != d.runtimeSpec.Workspace.CanonicalRoot {
		return "", &rpc.Error{
			Code:    rpc.CodeRuntimeIdentityMismatch,
			Message: "session workspace does not belong to this runtime",
			Data: map[string]any{
				"expected": map[string]any{"workspace_id": d.runtimeSpec.Workspace.ID, "workspace_root": d.runtimeSpec.Workspace.CanonicalRoot},
				"observed": map[string]any{"workspace_id": workspace.ID, "workspace_root": workspace.CanonicalRoot},
			},
		}
	}
	return workspace.CanonicalRoot, nil
}

func (d *Daemon) createSession(workspaceRoot, profile, approvalMode string) (*sessionstore.Session, error) {
	if d.runtimeSpec != nil && d.runtimeSpec.Mode == localruntime.ModeWorkspace {
		return d.store.CreateSessionModeForWorkspace(d.runtimeSpec.Workspace.ID, workspaceRoot, profile, approvalMode)
	}
	return d.store.CreateSessionMode(workspaceRoot, profile, approvalMode)
}

func (d *Daemon) createSubSession(workspaceRoot, profile, approvalMode, parentID string, depth int) (*sessionstore.Session, error) {
	if d.runtimeSpec != nil && d.runtimeSpec.Mode == localruntime.ModeWorkspace {
		return d.store.CreateSubSessionForWorkspace(d.runtimeSpec.Workspace.ID, workspaceRoot, profile, approvalMode, parentID, depth)
	}
	return d.store.CreateSubSession(workspaceRoot, profile, approvalMode, parentID, depth)
}

func (d *Daemon) runtimeDescription() map[string]any {
	d.runtimeMu.Lock()
	defer d.runtimeMu.Unlock()
	var epoch string
	var processEpoch int64
	if d.runtimeLease != nil {
		epoch = d.runtimeLease.state.InstanceID
		processEpoch = d.runtimeLease.state.Epoch
	}
	description := map[string]any{
		"mode":             string(localruntime.ModeLegacy),
		"epoch":            epoch,
		"process_epoch":    processEpoch,
		"pid":              os.Getpid(),
		"socket_path":      d.socketPath,
		"state_dir":        d.stateDir,
		"binary_version":   Version,
		"protocol_version": runtimeProtocolVersion,
		"lifecycle":        d.runtimeLifecycle,
	}
	if d.runtimeSpec == nil {
		return description
	}
	connections, obligations, idleDeadline := d.runtimeIdleSnapshot()
	description["mode"] = string(d.runtimeSpec.Mode)
	description["workspace"] = d.runtimeSpec.Workspace
	description["workspace_id"] = d.runtimeSpec.Workspace.ID
	description["workspace_root"] = d.runtimeSpec.Workspace.CanonicalRoot
	description["runtime_id"] = d.runtimeSpec.RuntimeID
	description["runtime_dir"] = d.runtimeSpec.Paths.RuntimeDir
	description["socket_path"] = d.runtimeSpec.Paths.SocketPath
	description["state_dir"] = d.runtimeSpec.Paths.StateDir
	description["config_fingerprint"] = d.runtimeSpec.Config.Fingerprint
	description["config_sources"] = cloneStringMap(d.runtimeSpec.Config.Sources)
	description["connections"] = connections
	description["obligations"] = obligations
	description["idle_deadline"] = idleDeadline
	return description
}

func (d *Daemon) handleRuntimeDescribe(_ json.RawMessage) (any, error) {
	return d.runtimeDescription(), nil
}

func (d *Daemon) validateExpectedRuntimeIdentity(workspaceID, runtimeID, epoch string) error {
	observed := d.runtimeDescription()
	mismatches := map[string]map[string]string{}
	check := func(name, expected string) {
		if expected == "" {
			return
		}
		actual, _ := observed[name].(string)
		if actual != expected {
			mismatches[name] = map[string]string{"expected": expected, "observed": actual}
		}
	}
	check("workspace_id", strings.TrimSpace(workspaceID))
	check("runtime_id", strings.TrimSpace(runtimeID))
	check("epoch", strings.TrimSpace(epoch))
	if len(mismatches) == 0 {
		return nil
	}
	return &rpc.Error{
		Code:    rpc.CodeRuntimeIdentityMismatch,
		Message: "runtime identity mismatch",
		Data: map[string]any{
			"mismatches": mismatches,
			"observed":   observed,
		},
	}
}

func (d *Daemon) publishRuntimeDescriptor(lifecycle, socketPath string) error {
	d.runtimeMu.Lock()
	defer d.runtimeMu.Unlock()
	if d.runtimeSpec == nil {
		d.runtimeLifecycle = lifecycle
		return nil
	}
	if socketPath != "" && !samePath(socketPath, d.runtimeSpec.Paths.SocketPath) {
		return fmt.Errorf("runtime socket mismatch: spec %q, daemon %q", d.runtimeSpec.Paths.SocketPath, socketPath)
	}
	d.runtimeLifecycle = lifecycle
	descriptor := localruntime.DescriptorFromSpec(*d.runtimeSpec)
	descriptor.Epoch = d.runtimeLease.state.InstanceID
	descriptor.PID = os.Getpid()
	descriptor.BinaryVersion = Version
	descriptor.Lifecycle = lifecycle
	descriptor.StartedAt = timePointer(d.started)
	descriptor.Connections, descriptor.Obligations, descriptor.IdleDeadline = d.runtimeIdleSnapshot()
	if lifecycle == localruntime.LifecycleStopped {
		descriptor.StoppedAt = timePointer(time.Now().UTC())
	}
	return localruntime.WriteDescriptor(d.runtimeSpec.Paths.DescriptorPath, descriptor)
}

func (d *Daemon) setRuntimeSocket(socketPath string) {
	d.runtimeMu.Lock()
	d.socketPath = socketPath
	d.runtimeMu.Unlock()
}

func (d *Daemon) runtimePublishState() (string, string) {
	d.runtimeMu.Lock()
	defer d.runtimeMu.Unlock()
	return d.runtimeLifecycle, d.socketPath
}

// UpdateRuntimeConfigIdentity publishes a successful hot reload without
// changing workspace, runtime, or process identity.
func (d *Daemon) UpdateRuntimeConfigIdentity(identity localruntime.ConfigIdentity) error {
	d.runtimeMu.Lock()
	if d.runtimeSpec == nil {
		d.runtimeMu.Unlock()
		return nil
	}
	d.runtimeSpec.Config = localruntime.ConfigIdentity{
		Fingerprint: identity.Fingerprint,
		Sources:     cloneStringMap(identity.Sources),
	}
	lifecycle, socketPath := d.runtimeLifecycle, d.socketPath
	d.runtimeMu.Unlock()
	return d.publishRuntimeDescriptor(lifecycle, socketPath)
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}
