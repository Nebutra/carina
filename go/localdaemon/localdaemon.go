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
	"slices"
	"syscall"
	"time"

	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/rpc"
)

// OwnershipMarker is written into ~/.carina/daemon.pid.json so
// `carina daemon stop` only signals processes this package started
// (never an unrelated carina-daemon the operator launched by hand).
const OwnershipMarker = "carina-cli/v1"

var requiredRuntimeMethods = []string{
	"execution.start",
	"conversation.import.discover",
	"conversation.import.apply",
}

// DialFunc dials a unix socket. Tests replace Dial.
var Dial = rpc.Dial

// SpawnFunc starts carina-daemon detached. Tests replace Spawn.
var Spawn = spawn

// SpawnRuntime starts one workspace runtime from its authoritative spec.
var SpawnRuntime = spawnRuntime

// RuntimeHandshake proves that a reachable endpoint owns the expected spec.
var RuntimeHandshake = runtimeHandshake

// RuntimeDescribe proves endpoint identity without requiring client feature
// compatibility. Lifecycle operations must remain able to inspect and stop an
// owned older runtime after the client has upgraded.
var RuntimeDescribe = runtimeDescribe

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

// RuntimeCompatibilityError identifies a correctly owned workspace runtime
// that is too old for this client. Description is retained so recovery can
// distinguish an idle runtime from one with active obligations without
// weakening the runtime identity checks.
type RuntimeCompatibilityError struct {
	Description    RuntimeDescription
	MissingMethods []string
}

func (e *RuntimeCompatibilityError) Error() string {
	return fmt.Sprintf("runtime is incompatible with this Carina client: missing RPC methods %v", e.MissingMethods)
}

// RuntimeConfigurationMismatchError identifies a correctly owned workspace
// runtime whose last-good configuration does not match the caller's resolved
// spec. It is deliberately separate from runtime identity: callers may ask
// the verified endpoint to reload, or replace an idle owned process, without
// weakening the workspace/runtime/socket/epoch proof.
type RuntimeConfigurationMismatchError struct {
	Description         RuntimeDescription
	ExpectedFingerprint string
	ObservedFingerprint string
	RefreshError        error
}

func (e *RuntimeConfigurationMismatchError) Error() string {
	message := fmt.Sprintf(
		"runtime configuration mismatch: expected %q, observed %q",
		e.ExpectedFingerprint,
		e.ObservedFingerprint,
	)
	if e.RefreshError != nil {
		message += ": refresh failed: " + e.RefreshError.Error()
	}
	return message
}

// Connect dials and validates a runtime without starting it.
func Connect(spec localruntime.Spec) (*rpc.Client, RuntimeDescription, error) {
	if err := spec.Validate(); err != nil {
		return nil, RuntimeDescription{}, err
	}
	return dialAndValidate(spec)
}

// Describe dials and validates runtime identity without asserting the client's
// feature inventory. It is intended for lifecycle status and recovery only.
func Describe(spec localruntime.Spec) (*rpc.Client, RuntimeDescription, error) {
	if err := spec.Validate(); err != nil {
		return nil, RuntimeDescription{}, err
	}
	client, err := Dial(spec.Paths.SocketPath)
	if err != nil {
		return nil, RuntimeDescription{}, err
	}
	description, err := RuntimeDescribe(client, spec)
	if err != nil {
		_ = client.Close()
		return nil, RuntimeDescription{}, err
	}
	return client, description, nil
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
	var compatibility *RuntimeCompatibilityError
	var configuration *RuntimeConfigurationMismatchError
	switch {
	case errors.As(err, &compatibility):
		description = compatibility.Description
		if len(description.Obligations) > 0 {
			return nil, RuntimeDescription{}, fmt.Errorf("runtime upgrade deferred while active obligations remain: %v: %w", description.Obligations, compatibility)
		}
	case errors.As(err, &configuration):
		description = configuration.Description
		if len(description.Obligations) > 0 {
			return nil, RuntimeDescription{}, fmt.Errorf("runtime configuration refresh deferred while active obligations remain: %v: %w", description.Obligations, configuration)
		}
	default:
		if !errors.Is(err, rpc.ErrDaemonUnreachable) {
			return nil, RuntimeDescription{}, err
		}
	}
	if compatibility != nil || configuration != nil {
		if _, stopErr := StopRuntime(spec, false); stopErr != nil {
			return nil, RuntimeDescription{}, fmt.Errorf("replace stale workspace runtime: %w", stopErr)
		}
		if waitErr := waitRuntimeUnreachable(spec.Paths.SocketPath); waitErr != nil {
			return nil, RuntimeDescription{}, fmt.Errorf("replace stale workspace runtime: %w", waitErr)
		}
		err = rpc.ErrDaemonUnreachable
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

func runtimeDescribe(client *rpc.Client, spec localruntime.Spec) (RuntimeDescription, error) {
	var description RuntimeDescription
	if err := client.Call("runtime.describe", map[string]any{}, &description); err != nil {
		return RuntimeDescription{}, fmt.Errorf("runtime describe: %w", err)
	}
	if err := validateRuntimeIdentity(spec, description); err != nil {
		return RuntimeDescription{}, err
	}
	return description, nil
}

func runtimeHandshake(client *rpc.Client, spec localruntime.Spec) (RuntimeDescription, error) {
	description, err := RuntimeDescribe(client, spec)
	if err != nil {
		return RuntimeDescription{}, err
	}
	var initialized struct {
		Runtime      RuntimeDescription `json:"runtime"`
		Capabilities struct {
			RPCMethods []string `json:"rpc_methods"`
		} `json:"capabilities"`
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
	if err := validateRuntimeIdentity(spec, initialized.Runtime); err != nil {
		return RuntimeDescription{}, err
	}
	if initialized.Runtime.Epoch != description.Epoch {
		return RuntimeDescription{}, &rpc.Error{Code: rpc.CodeRuntimeIdentityMismatch, Message: "runtime epoch changed during initialization", Data: map[string]any{"described": description.Epoch, "initialized": initialized.Runtime.Epoch}}
	}
	if missing := missingRuntimeMethods(initialized.Capabilities.RPCMethods, requiredRuntimeMethods...); len(missing) > 0 {
		return RuntimeDescription{}, &RuntimeCompatibilityError{
			Description:    initialized.Runtime,
			MissingMethods: missing,
		}
	}
	return refreshRuntimeConfiguration(client, spec, initialized.Runtime)
}

func refreshRuntimeConfiguration(client *rpc.Client, spec localruntime.Spec, description RuntimeDescription) (RuntimeDescription, error) {
	configurationErr := runtimeConfigurationMismatch(spec, description)
	if configurationErr == nil {
		return description, nil
	}
	var reloaded struct {
		Reloaded bool `json:"reloaded"`
	}
	if err := client.Call("daemon.reload", map[string]any{}, &reloaded); err != nil {
		return staleRuntimeConfiguration(spec, configurationErr, err)
	}
	if !reloaded.Reloaded {
		return staleRuntimeConfiguration(spec, configurationErr, errors.New("daemon did not confirm reload"))
	}
	refreshed, err := RuntimeDescribe(client, spec)
	if err != nil {
		if restoreErr := localruntime.WriteSpec(spec.Paths.SpecPath, spec); restoreErr != nil {
			return RuntimeDescription{}, fmt.Errorf("restore authoritative runtime spec after failed reload: %w", restoreErr)
		}
		return RuntimeDescription{}, fmt.Errorf("describe runtime after configuration reload: %w", err)
	}
	if refreshed.Epoch != description.Epoch {
		if restoreErr := localruntime.WriteSpec(spec.Paths.SpecPath, spec); restoreErr != nil {
			return RuntimeDescription{}, fmt.Errorf("restore authoritative runtime spec after changed reload epoch: %w", restoreErr)
		}
		return RuntimeDescription{}, &rpc.Error{
			Code:    rpc.CodeRuntimeIdentityMismatch,
			Message: "runtime epoch changed during configuration reload",
			Data:    map[string]any{"before": description.Epoch, "after": refreshed.Epoch},
		}
	}
	if configurationErr = runtimeConfigurationMismatch(spec, refreshed); configurationErr != nil {
		return staleRuntimeConfiguration(spec, configurationErr, nil)
	}
	return refreshed, nil
}

func staleRuntimeConfiguration(spec localruntime.Spec, configurationErr *RuntimeConfigurationMismatchError, refreshErr error) (RuntimeDescription, error) {
	// Older daemons may resolve different compiled defaults and rewrite the
	// shared spec during reload. Restore the caller's authoritative spec so an
	// idle replacement starts from the configuration we just validated.
	if err := localruntime.WriteSpec(spec.Paths.SpecPath, spec); err != nil {
		return RuntimeDescription{}, fmt.Errorf("restore authoritative runtime spec after stale reload: %w", err)
	}
	configurationErr.RefreshError = refreshErr
	return RuntimeDescription{}, configurationErr
}

func runtimeConfigurationMismatch(spec localruntime.Spec, description RuntimeDescription) *RuntimeConfigurationMismatchError {
	if description.ConfigFingerprint == spec.Config.Fingerprint {
		return nil
	}
	return &RuntimeConfigurationMismatchError{
		Description:         description,
		ExpectedFingerprint: spec.Config.Fingerprint,
		ObservedFingerprint: description.ConfigFingerprint,
	}
}

func requireRuntimeMethods(available []string, required ...string) error {
	missing := missingRuntimeMethods(available, required...)
	if len(missing) == 0 {
		return nil
	}
	return &rpc.Error{
		Code:    rpc.CodeRuntimeIdentityMismatch,
		Message: "runtime is incompatible with this Carina client",
		Data: map[string]any{
			"missing_rpc_methods": missing,
			"recovery":            "restart the workspace runtime with the installed Carina version",
		},
	}
}

func missingRuntimeMethods(available []string, required ...string) []string {
	missing := make([]string, 0, len(required))
	for _, method := range required {
		if !slices.Contains(available, method) {
			missing = append(missing, method)
		}
	}
	return missing
}

func waitRuntimeUnreachable(socket string) error {
	deadline := time.Now().Add(ReachableDeadline)
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		client, err := Dial(socket)
		if errors.Is(err, rpc.ErrDaemonUnreachable) {
			return nil
		}
		if err == nil {
			_ = client.Close()
		}
		time.Sleep(startupBackoff(attempt))
	}
	return fmt.Errorf("runtime did not stop before restart deadline")
}

func validateRuntimeIdentity(spec localruntime.Spec, description RuntimeDescription) error {
	expected := map[string]string{
		"mode": string(spec.Mode), "workspace_id": spec.Workspace.ID,
		"workspace_root": spec.Workspace.CanonicalRoot, "runtime_id": spec.RuntimeID,
		"socket_path": spec.Paths.SocketPath, "state_dir": spec.Paths.StateDir,
		"runtime_dir": spec.Paths.RuntimeDir,
	}
	observed := map[string]string{
		"mode": description.Mode, "workspace_id": description.WorkspaceID,
		"workspace_root": description.WorkspaceRoot, "runtime_id": description.RuntimeID,
		"socket_path": description.SocketPath, "state_dir": description.StateDir,
		"runtime_dir": description.RuntimeDir,
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
	if attempt <= 0 {
		return 0
	}
	d := 20 * time.Millisecond * time.Duration(1<<(attempt-1))
	if d > 200*time.Millisecond {
		d = 200 * time.Millisecond
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
	client, err := Dial(spec.Paths.SocketPath)
	if err != nil {
		return RuntimeDescription{}, fmt.Errorf("refusing to signal pid %d: runtime endpoint is not reachable and verified: %w", record.PID, err)
	}
	description, describeErr := RuntimeDescribe(client, spec)
	_ = client.Close()
	if describeErr != nil {
		return RuntimeDescription{}, fmt.Errorf("refusing to signal pid %d: runtime endpoint is not reachable and verified: %w", record.PID, describeErr)
	}
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
