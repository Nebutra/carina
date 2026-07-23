package localruntime

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const DescriptorVersion = 1

const (
	LifecycleStarting = "starting"
	LifecycleRunning  = "running"
	LifecycleStopping = "stopping"
	LifecycleStopped  = "stopped"
)

type Descriptor struct {
	Version           int               `json:"version"`
	Mode              Mode              `json:"mode"`
	Workspace         Workspace         `json:"workspace"`
	RuntimeID         string            `json:"runtime_id"`
	Epoch             string            `json:"epoch,omitempty"`
	PID               int               `json:"pid,omitempty"`
	SocketPath        string            `json:"socket_path"`
	StateDir          string            `json:"state_dir"`
	RuntimeDir        string            `json:"runtime_dir"`
	ConfigFingerprint string            `json:"config_fingerprint,omitempty"`
	ConfigSources     map[string]string `json:"config_sources,omitempty"`
	BinaryVersion     string            `json:"binary_version,omitempty"`
	Lifecycle         string            `json:"lifecycle"`
	Connections       int               `json:"connections,omitempty"`
	Obligations       []string          `json:"obligations,omitempty"`
	IdleDeadline      *time.Time        `json:"idle_deadline,omitempty"`
	StartedAt         *time.Time        `json:"started_at,omitempty"`
	StoppedAt         *time.Time        `json:"stopped_at,omitempty"`
}

func DescriptorFromSpec(spec Spec) Descriptor {
	return Descriptor{
		Version: DescriptorVersion, Mode: spec.Mode, Workspace: spec.Workspace,
		RuntimeID: spec.RuntimeID, SocketPath: spec.Paths.SocketPath,
		StateDir: spec.Paths.StateDir, RuntimeDir: spec.Paths.RuntimeDir,
		ConfigFingerprint: spec.Config.Fingerprint,
		ConfigSources:     cloneConfigIdentity(spec.Config).Sources,
		Lifecycle:         LifecycleStopped,
	}
}

func (d Descriptor) Validate() error {
	if d.Version != DescriptorVersion {
		return fmt.Errorf("localruntime: unsupported descriptor version %d", d.Version)
	}
	if !d.Mode.valid() {
		return fmt.Errorf("localruntime: invalid descriptor mode %q", d.Mode)
	}
	if d.Workspace.ID == "" || d.Workspace.CanonicalRoot == "" || d.RuntimeID == "" {
		return errors.New("localruntime: incomplete descriptor identity")
	}
	if d.Workspace.ID != WorkspaceID(d.Workspace.CanonicalRoot) {
		return errors.New("localruntime: descriptor workspace id mismatch")
	}
	if !strings.HasPrefix(d.RuntimeID, "runtime_") {
		return errors.New("localruntime: invalid descriptor runtime id")
	}
	switch d.Lifecycle {
	case LifecycleStarting, LifecycleRunning, LifecycleStopping, LifecycleStopped:
	default:
		return fmt.Errorf("localruntime: invalid lifecycle %q", d.Lifecycle)
	}
	if d.Connections < 0 {
		return errors.New("localruntime: negative connection count")
	}
	if d.Lifecycle == LifecycleRunning && (d.PID <= 1 || d.Epoch == "") {
		return errors.New("localruntime: running descriptor requires pid and epoch")
	}
	return nil
}

func LoadDescriptor(path string) (Descriptor, error) {
	var descriptor Descriptor
	if err := readPrivateJSON(path, &descriptor); err != nil {
		return Descriptor{}, err
	}
	if err := descriptor.Validate(); err != nil {
		return Descriptor{}, err
	}
	return descriptor, nil
}

func WriteDescriptor(path string, descriptor Descriptor) error {
	if err := descriptor.Validate(); err != nil {
		return err
	}
	return writePrivateJSONAtomic(path, descriptor)
}
