// Package localdaemon auto-starts a user-owned carina-daemon for local
// interactive clients (bare `carina`). The daemon remains a
// long-lived control plane; clients only spawn it when the socket is down.
package localdaemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
)

// OwnershipMarker is written into ~/.carina/daemon.pid.json so
// `carina daemon stop` only signals processes this package started
// (never an unrelated carina-daemon the operator launched by hand).
const OwnershipMarker = "carina-cli/v1"

// DialFunc dials a unix socket. Tests replace Dial.
var Dial = rpc.Dial

// SpawnFunc starts carina-daemon detached. Tests replace Spawn.
var Spawn = spawn

// SpawnRuntime starts one workspace runtime from its authoritative spec.
var SpawnRuntime = spawnRuntime

// RuntimeHandshake proves that a reachable endpoint owns the expected spec.
var RuntimeHandshake = runtimeHandshake

// ReachableDeadline bounds post-spawn dial retries.
var ReachableDeadline = 10 * time.Second

type ownershipRecord struct {
	Owner       string    `json:"owner"`
	PID         int       `json:"pid"`
	Socket      string    `json:"socket"`
	Executable  string    `json:"executable,omitempty"`
	WorkspaceID string    `json:"workspace_id,omitempty"`
	RuntimeID   string    `json:"runtime_id,omitempty"`
	Epoch       string    `json:"epoch,omitempty"`
	StartedAt   time.Time `json:"started_at"`
}

// RuntimeDescription is the identity proof returned by runtime.describe.
type RuntimeDescription struct {
	Mode              string            `json:"mode"`
	WorkspaceID       string            `json:"workspace_id"`
	WorkspaceRoot     string            `json:"workspace_root"`
	RuntimeID         string            `json:"runtime_id"`
	Epoch             string            `json:"epoch"`
	ProcessEpoch      int64             `json:"process_epoch"`
	PID               int               `json:"pid"`
	SocketPath        string            `json:"socket_path"`
	StateDir          string            `json:"state_dir"`
	RuntimeDir        string            `json:"runtime_dir"`
	ConfigFingerprint string            `json:"config_fingerprint"`
	Lifecycle         string            `json:"lifecycle"`
	ConfigSources     map[string]string `json:"config_sources,omitempty"`
	Connections       int               `json:"connections,omitempty"`
	Obligations       []string          `json:"obligations,omitempty"`
	IdleDeadline      *time.Time        `json:"idle_deadline,omitempty"`
}

// Connect dials and validates a runtime without starting it.
func Connect(spec localruntime.Spec) (*rpc.Client, RuntimeDescription, error) {
	if err := spec.Validate(); err != nil {
		return nil, RuntimeDescription{}, err
	}
	return dialAndValidate(spec)
}

// ConnectOrStart holds the workspace start lock until a reachable endpoint
// proves the complete runtime identity.
func ConnectOrStart(spec localruntime.Spec) (*rpc.Client, RuntimeDescription, error) {
	if err := spec.Validate(); err != nil {
		return nil, RuntimeDescription{}, err
	}
	lock, err := acquireRuntimeStartLock(spec.Paths.StartLockPath)
	if err != nil {
		return nil, RuntimeDescription{}, fmt.Errorf("acquire runtime start lock: %w", err)
	}
	defer releaseRuntimeStartLock(lock)

	client, description, err := dialAndValidate(spec)
	if err == nil {
		return client, description, nil
	}
	if !errors.Is(err, rpc.ErrDaemonUnreachable) {
		return nil, RuntimeDescription{}, err
	}
	if err := SpawnRuntime(spec); err != nil {
		return nil, RuntimeDescription{}, fmt.Errorf("runtime unreachable and auto-start failed: %w", err)
	}

	deadline := time.Now().Add(ReachableDeadline)
	lastErr := err
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		time.Sleep(startupBackoff(attempt))
		client, description, err = dialAndValidate(spec)
		if err == nil {
			if err := updateRuntimeOwnerEpoch(spec, description); err != nil {
				_ = client.Close()
				return nil, RuntimeDescription{}, err
			}
			return client, description, nil
		}
		lastErr = err
		if !errors.Is(err, rpc.ErrDaemonUnreachable) {
			return nil, RuntimeDescription{}, err
		}
	}
	return nil, RuntimeDescription{}, fmt.Errorf("runtime did not become reachable after auto-start: %w", lastErr)
}

func dialAndValidate(spec localruntime.Spec) (*rpc.Client, RuntimeDescription, error) {
	client, err := Dial(spec.Paths.SocketPath)
	if err != nil {
		return nil, RuntimeDescription{}, err
	}
	description, err := RuntimeHandshake(client, spec)
	if err != nil {
		_ = client.Close()
		return nil, RuntimeDescription{}, err
	}
	return client, description, nil
}

func runtimeHandshake(client *rpc.Client, spec localruntime.Spec) (RuntimeDescription, error) {
	var description RuntimeDescription
	if err := client.Call("runtime.describe", map[string]any{}, &description); err != nil {
		return RuntimeDescription{}, fmt.Errorf("runtime describe: %w", err)
	}
	if err := validateRuntimeDescription(spec, description); err != nil {
		return RuntimeDescription{}, err
	}
	var initialized struct {
		Runtime RuntimeDescription `json:"runtime"`
	}
	if err := client.Call("runtime.initialize", map[string]any{
		"protocol_version":      "1.3.0",
		"schema_version":        "1.2.0",
		"client_name":           "carina-localdaemon",
		"expected_workspace_id": spec.Workspace.ID,
		"expected_runtime_id":   spec.RuntimeID,
		"expected_epoch":        description.Epoch,
	}, &initialized); err != nil {
		return RuntimeDescription{}, fmt.Errorf("runtime initialize: %w", err)
	}
	if err := validateRuntimeDescription(spec, initialized.Runtime); err != nil {
		return RuntimeDescription{}, err
	}
	if initialized.Runtime.Epoch != description.Epoch {
		return RuntimeDescription{}, &rpc.Error{Code: rpc.CodeRuntimeIdentityMismatch, Message: "runtime epoch changed during initialization", Data: map[string]any{"described": description.Epoch, "initialized": initialized.Runtime.Epoch}}
	}
	return initialized.Runtime, nil
}

func validateRuntimeDescription(spec localruntime.Spec, description RuntimeDescription) error {
	expected := map[string]string{
		"mode": string(spec.Mode), "workspace_id": spec.Workspace.ID,
		"workspace_root": spec.Workspace.CanonicalRoot, "runtime_id": spec.RuntimeID,
		"socket_path": spec.Paths.SocketPath, "state_dir": spec.Paths.StateDir,
		"runtime_dir": spec.Paths.RuntimeDir, "config_fingerprint": spec.Config.Fingerprint,
	}
	observed := map[string]string{
		"mode": description.Mode, "workspace_id": description.WorkspaceID,
		"workspace_root": description.WorkspaceRoot, "runtime_id": description.RuntimeID,
		"socket_path": description.SocketPath, "state_dir": description.StateDir,
		"runtime_dir": description.RuntimeDir, "config_fingerprint": description.ConfigFingerprint,
	}
	mismatches := map[string]map[string]string{}
	for key, want := range expected {
		if observed[key] != want {
			mismatches[key] = map[string]string{"expected": want, "observed": observed[key]}
		}
	}
	if description.Epoch == "" {
		mismatches["epoch"] = map[string]string{"expected": "non-empty", "observed": ""}
	}
	if len(mismatches) > 0 {
		return &rpc.Error{Code: rpc.CodeRuntimeIdentityMismatch, Message: "runtime identity mismatch", Data: map[string]any{"mismatches": mismatches}}
	}
	return nil
}

// EnsureReachable dials socket and, if the daemon is unreachable, spawns
// carina-daemon and retries until ReachableDeadline. Non-unreachable dial
// errors are returned immediately without spawning.
//
// The returned client is already connected; the caller owns Close.
func EnsureReachable(socket string) (*rpc.Client, error) {
	c, err := Dial(socket)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, rpc.ErrDaemonUnreachable) {
		return nil, err
	}
	if spawnErr := Spawn(socket); spawnErr != nil {
		return nil, fmt.Errorf("daemon unreachable and auto-start failed: %w", spawnErr)
	}
	deadline := time.Now().Add(ReachableDeadline)
	lastErr := err
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		time.Sleep(startupBackoff(attempt))
		c, err := Dial(socket)
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("daemon did not become reachable after auto-start: %w", lastErr)
}

// EnsureSocket is like EnsureReachable but only guarantees the daemon is up
// (closes the probe connection). Use before Connect-style loops that open
// their own long-lived streams.
func EnsureSocket(socket string) error {
	c, err := EnsureReachable(socket)
	if err != nil {
		return err
	}
	return c.Close()
}

func startupBackoff(attempt int) time.Duration {
	d := 100 * time.Millisecond * time.Duration(attempt+1)
	if d > time.Second {
		d = time.Second
	}
	return d
}

func ownershipPath(socket string) string {
	return filepath.Join(filepath.Dir(socket), "daemon.pid.json")
}

func logPath(socket string) string {
	return filepath.Join(filepath.Dir(socket), "daemon.log")
}

// resolveDaemonBinary prefers an explicit override, then a sibling of the
// current executable (release install layout), then PATH.
func resolveDaemonBinary() string {
	if bin := os.Getenv("CARINA_DAEMON_BIN"); bin != "" {
		return bin
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "carina-daemon")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate
		}
	}
	if p, err := exec.LookPath("carina-daemon"); err == nil {
		return p
	}
	return "carina-daemon"
}

func spawn(socket string) error {
	bin := resolveDaemonBinary()
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath(socket), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		_ = logFile.Close()
		return err
	}
	cmd := exec.Command(bin)
	// Prefer the caller's socket so a custom -socket still works when the
	// binary supports it; carina-daemon defaults match when omitted.
	if socket != "" {
		cmd.Args = append(cmd.Args, "-socket", socket)
	}
	cmd.Stdin = devnull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = devnull.Close()
		_ = logFile.Close()
		return fmt.Errorf("start %s: %w", bin, err)
	}
	_ = devnull.Close()
	_ = logFile.Close()

	executable, _ := filepath.Abs(bin)
	record := ownershipRecord{
		Owner:      OwnershipMarker,
		PID:        cmd.Process.Pid,
		Socket:     socket,
		Executable: executable,
		StartedAt:  time.Now().UTC(),
	}
	raw, err := json.Marshal(record)
	if err == nil {
		err = writePrivateFileAtomic(ownershipPath(socket), raw)
	}
	if err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Process.Release()
		return fmt.Errorf("record daemon ownership: %w", err)
	}
	// Detach: clients do not supervise the daemon process tree.
	return cmd.Process.Release()
}

func spawnRuntime(spec localruntime.Spec) error {
	bin := resolveDaemonBinary()
	if err := os.MkdirAll(spec.Paths.RuntimeDir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(spec.Paths.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	devnull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		_ = logFile.Close()
		return err
	}
	cmd := exec.Command(bin, "-runtime-spec", spec.Paths.SpecPath)
	cmd.Stdin = devnull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureDetachedProcess(cmd)
	if err := cmd.Start(); err != nil {
		_ = devnull.Close()
		_ = logFile.Close()
		return fmt.Errorf("start %s: %w", bin, err)
	}
	_ = devnull.Close()
	_ = logFile.Close()

	executable, _ := filepath.Abs(bin)
	record := ownershipRecord{
		Owner: OwnershipMarker, PID: cmd.Process.Pid, Socket: spec.Paths.SocketPath,
		Executable: executable, WorkspaceID: spec.Workspace.ID, RuntimeID: spec.RuntimeID,
		StartedAt: time.Now().UTC(),
	}
	if err := writeOwnershipRecord(spec.Paths.OwnerPath, record); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Process.Release()
		return fmt.Errorf("record runtime ownership: %w", err)
	}
	return cmd.Process.Release()
}

func updateRuntimeOwnerEpoch(spec localruntime.Spec, description RuntimeDescription) error {
	record, err := readOwnershipRecord(spec.Paths.OwnerPath)
	if err != nil {
		return fmt.Errorf("read runtime ownership: %w", err)
	}
	if record.Owner != OwnershipMarker || record.WorkspaceID != spec.Workspace.ID || record.RuntimeID != spec.RuntimeID || record.Socket != spec.Paths.SocketPath || record.PID != description.PID {
		return fmt.Errorf("runtime ownership does not match live endpoint")
	}
	record.Epoch = description.Epoch
	return writeOwnershipRecord(spec.Paths.OwnerPath, record)
}

func writeOwnershipRecord(path string, record ownershipRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writePrivateFileAtomic(path, raw)
}

func readOwnershipRecord(path string) (ownershipRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return ownershipRecord{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return ownershipRecord{}, fmt.Errorf("unsafe runtime ownership record %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ownershipRecord{}, err
	}
	var record ownershipRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return ownershipRecord{}, err
	}
	return record, nil
}

// ReleaseRuntimeOwnership removes only the CLI owner record that identifies
// the current workspace runtime process. The stopped descriptor remains as the
// passive registry entry.
func ReleaseRuntimeOwnership(spec localruntime.Spec, pid int) error {
	record, err := readOwnershipRecord(spec.Paths.OwnerPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if pid <= 1 || record.Owner != OwnershipMarker || record.PID != pid || record.Socket != spec.Paths.SocketPath || record.WorkspaceID != spec.Workspace.ID || record.RuntimeID != spec.RuntimeID {
		return fmt.Errorf("refusing to remove runtime ownership: record does not identify pid %d runtime %s", pid, spec.RuntimeID)
	}
	return os.Remove(spec.Paths.OwnerPath)
}

// StopRuntime verifies the live endpoint against the private ownership record
// before signalling the CLI-owned process.
func StopRuntime(spec localruntime.Spec, force bool) (RuntimeDescription, error) {
	record, err := readOwnershipRecord(spec.Paths.OwnerPath)
	if err != nil {
		return RuntimeDescription{}, fmt.Errorf("no valid CLI ownership record: %w", err)
	}
	client, description, err := Connect(spec)
	if err != nil {
		return RuntimeDescription{}, fmt.Errorf("refusing to signal pid %d: runtime endpoint is not reachable and verified: %w", record.PID, err)
	}
	_ = client.Close()
	if record.Owner != OwnershipMarker || record.WorkspaceID != spec.Workspace.ID || record.RuntimeID != spec.RuntimeID || record.Socket != spec.Paths.SocketPath || record.PID != description.PID || record.Epoch == "" || record.Epoch != description.Epoch {
		return RuntimeDescription{}, fmt.Errorf("refusing to signal pid %d: ownership record does not match live runtime", record.PID)
	}
	if record.Executable == "" {
		return RuntimeDescription{}, fmt.Errorf("refusing to signal pid %d: ownership record has no executable identity", record.PID)
	}
	actualExecutable, err := runtimeProcessExecutable(record.PID)
	if err != nil {
		return RuntimeDescription{}, fmt.Errorf("refusing to signal pid %d: verify process executable: %w", record.PID, err)
	}
	if !sameExecutable(record.Executable, actualExecutable) {
		return RuntimeDescription{}, fmt.Errorf("refusing to signal pid %d: process executable mismatch: expected %q, observed %q", record.PID, record.Executable, actualExecutable)
	}
	if len(description.Obligations) > 0 && !force {
		return RuntimeDescription{}, fmt.Errorf("runtime has active obligations: %v (use --force to stop)", description.Obligations)
	}
	if err := signalRuntimeProcess(record.PID); err != nil {
		return RuntimeDescription{}, fmt.Errorf("stop runtime pid %d: %w", record.PID, err)
	}
	return description, nil
}

func sameExecutable(left, right string) bool {
	canonical := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(absolute)
	}
	return canonical(left) == canonical(right)
}

func writePrivateFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".carina-owned-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
