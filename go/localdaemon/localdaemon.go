// Package localdaemon auto-starts a user-owned carina-daemon for local
// interactive clients (bare `carina`, carina-tui). The daemon remains a
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

// ReachableDeadline bounds post-spawn dial retries.
var ReachableDeadline = 10 * time.Second

type ownershipRecord struct {
	Owner      string    `json:"owner"`
	PID        int       `json:"pid"`
	Socket     string    `json:"socket"`
	Executable string    `json:"executable,omitempty"`
	StartedAt  time.Time `json:"started_at"`
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
