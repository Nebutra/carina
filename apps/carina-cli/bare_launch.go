package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/outcome"
)

const rustUIBinaryName = "carina-ui"

type interactiveOptions struct {
	Socket        string
	SessionID     string
	WorkspaceRoot string
	Locale        string
	NoAltScreen   bool
}

type rustUILaunch struct {
	Binary string
	Args   []string
}

func appendTerminalArgs(args []string, opts interactiveOptions, mode string) []string {
	if opts.NoAltScreen {
		return append(args, "--no-alt-screen")
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	return append(args, "--alt-screen", mode)
}

var (
	uiExecutable = os.Executable
	uiLookPath   = exec.LookPath
	uiStat       = os.Stat
	uiRunProcess = replaceWithUIProcess
)

// Capture the package defaults at init so temporary rebinds of
// localdaemon.Dial/Spawn (for tests) cannot recurse through these hooks.
var (
	defaultLocalDial        = localdaemon.Dial
	defaultLocalSpawn       = localdaemon.Spawn
	defaultRuntimeSpawn     = localdaemon.SpawnRuntime
	defaultRuntimeHandshake = localdaemon.RuntimeHandshake
)

// spawnDaemonHook lets tests observe/replace the actual daemon spawn without
// starting a real process (mirrors dialHook's seam in client.go).
var spawnDaemonHook = func() error {
	socket, err := defaultSocketPath()
	if err != nil {
		return err
	}
	return defaultLocalSpawn(socket)
}

// dialSocketHook lets tests observe/replace ensureDaemonReachable's dial
// calls without touching a real unix socket.
var dialSocketHook = defaultLocalDial

var spawnRuntimeHook = func(spec localruntime.Spec) error { return defaultRuntimeSpawn(spec) }
var runtimeHandshakeHook = defaultRuntimeHandshake

// daemonReachableDeadline bounds how long ensureDaemonReachable retries the
// dial after auto-starting the daemon before giving up.
var daemonReachableDeadline = 10 * time.Second

// runBareTUI is bare `carina` (no args) via the packaged Rust UI.
func runBareTUI() outcome.Outcome {
	return runTUI(interactiveOptions{})
}

// runTUI resolves and proves the runtime before replacing the Go router with
// the internal Rust surface. All non-interactive commands stay in this binary.
func runTUI(opts interactiveOptions) outcome.Outcome {
	launch, err := prepareRustUILaunch(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "carina: start interactive UI: %v\n", err)
		if errors.Is(err, localruntime.ErrModeDecisionRequired) {
			return outcome.OutcomeUsage
		}
		return classifyExitCode(err)
	}
	code, err := uiRunProcess(launch.Binary, launch.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "carina: launch %s: %v\n", rustUIBinaryName, err)
		return outcome.OutcomeRuntimeError
	}
	return outcomeFromExitCode(code)
}

func prepareRustUILaunch(opts interactiveOptions) (rustUILaunch, error) {
	uiBinary, err := resolveRustUIBinary()
	if err != nil {
		return rustUILaunch{}, err
	}
	carinaBinary, err := uiExecutable()
	if err != nil {
		return rustUILaunch{}, fmt.Errorf("resolve carina executable: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return rustUILaunch{}, fmt.Errorf("resolve home: %w", err)
	}
	workspace := strings.TrimSpace(opts.WorkspaceRoot)
	if workspace == "" {
		workspace, err = os.Getwd()
		if err != nil {
			return rustUILaunch{}, fmt.Errorf("resolve workspace: %w", err)
		}
	}

	locale, localePath, err := resolveInteractiveLocale(home, workspace, opts.Locale)
	if err != nil {
		return rustUILaunch{}, err
	}
	altScreen, err := config.InspectTUIAlternateScreen(home, workspace)
	if err != nil {
		return rustUILaunch{}, err
	}
	mode, err := localruntime.ResolveMode(home)
	if errors.Is(err, localruntime.ErrModeDecisionRequired) {
		return rustUILaunch{
			Binary: uiBinary,
			Args:   buildRuntimeModeSetupArgs(opts, home, carinaBinary),
		}, nil
	}
	if err != nil {
		return rustUILaunch{}, err
	}

	var socket string
	if mode == localruntime.ModeWorkspace {
		resolution, resolveErr := localruntime.Resolve(home, workspace, mode)
		if resolveErr != nil {
			return rustUILaunch{}, resolveErr
		}
		if explicitSocket := strings.TrimSpace(opts.Socket); explicitSocket != "" {
			resolution, resolveErr = localruntime.ApplyOverrides(home, resolution, localruntime.Overrides{Socket: explicitSocket})
			if resolveErr != nil {
				return rustUILaunch{}, resolveErr
			}
		}
		client, _, connectErr := connectOrStartRuntime(resolution.Spec)
		if connectErr != nil {
			var compatibility *localdaemon.RuntimeCompatibilityError
			if errors.As(connectErr, &compatibility) {
				return rustUILaunch{
					Binary: uiBinary,
					Args: buildRuntimeDiagnosticArgs(
						opts,
						resolution.Workspace.CanonicalRoot,
						locale,
						carinaBinary,
						resolution.Spec.Paths.LogPath,
						compatibility,
					),
				}, nil
			}
			return rustUILaunch{}, connectErr
		}
		_ = client.Close()
		workspace = resolution.Workspace.CanonicalRoot
		socket = resolution.Spec.Paths.SocketPath
	} else {
		cfg, loadErr := config.Load(home, workspace)
		if loadErr != nil {
			return rustUILaunch{}, loadErr
		}
		socket = strings.TrimSpace(opts.Socket)
		if socket == "" {
			socket = cfg.Socket
		}
		client, connectErr := ensureDaemonReachable(socket)
		if connectErr != nil {
			return rustUILaunch{}, connectErr
		}
		_ = client.Close()
		if canonical, canonicalErr := filepath.EvalSymlinks(workspace); canonicalErr == nil {
			workspace = canonical
		} else if absolute, absoluteErr := filepath.Abs(workspace); absoluteErr == nil {
			workspace = absolute
		}
	}

	args := buildRustUIArgs(opts, socket, workspace, locale, localePath, carinaBinary, altScreen)
	return rustUILaunch{Binary: uiBinary, Args: args}, nil
}

func buildRuntimeDiagnosticArgs(opts interactiveOptions, workspace, locale, carinaBinary, logPath string, compatibility *localdaemon.RuntimeCompatibilityError) []string {
	args := []string{
		"--runtime-diagnostic",
		"--workspace", workspace,
		"--carina-bin", carinaBinary,
		"--runtime-id", compatibility.Description.RuntimeID,
		"--runtime-log", logPath,
	}
	if locale != "" {
		args = append(args, "--locale", locale)
	}
	for _, method := range compatibility.MissingMethods {
		args = append(args, "--missing-method", method)
	}
	for _, obligation := range compatibility.Description.Obligations {
		args = append(args, "--obligation", obligation)
	}
	if opts.NoAltScreen {
		args = append(args, "--no-alt-screen")
	}
	return args
}

func buildRuntimeModeSetupArgs(opts interactiveOptions, home, carinaBinary string) []string {
	args := []string{
		"--runtime-mode-setup",
		"--home", home,
		"--carina-bin", carinaBinary,
	}
	if opts.NoAltScreen {
		args = append(args, "--no-alt-screen")
	}
	return args
}

func buildRustUIArgs(opts interactiveOptions, socket, workspace, locale, localePath, carinaBinary, altScreen string) []string {
	args := []string{
		"--socket", socket,
		"--workspace", workspace,
		"--carina-bin", carinaBinary,
	}
	if session := strings.TrimSpace(opts.SessionID); session != "" {
		args = append(args, "--session", session)
	}
	if locale != "" {
		args = append(args, "--locale", locale)
	}
	if localePath != "" {
		args = append(args, "--locale-path", localePath)
	}
	return appendTerminalArgs(args, opts, altScreen)
}

func connectOrStartRuntime(spec localruntime.Spec) (*rpcClient, localdaemon.RuntimeDescription, error) {
	origDial, origRuntimeSpawn, origHandshake, origDeadline := localdaemon.Dial, localdaemon.SpawnRuntime, localdaemon.RuntimeHandshake, localdaemon.ReachableDeadline
	localdaemon.Dial = dialSocketHook
	localdaemon.SpawnRuntime = spawnRuntimeHook
	localdaemon.RuntimeHandshake = runtimeHandshakeHook
	localdaemon.ReachableDeadline = daemonReachableDeadline
	defer func() {
		localdaemon.Dial, localdaemon.SpawnRuntime, localdaemon.RuntimeHandshake, localdaemon.ReachableDeadline = origDial, origRuntimeSpawn, origHandshake, origDeadline
	}()
	return localdaemon.ConnectOrStart(spec)
}

func resolveInteractiveLocale(home, workspace, explicit string) (string, string, error) {
	pref, err := config.InspectTUILocale(home, workspace)
	if err != nil {
		return "", "", err
	}
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		locale, err := microcopy.CanonicalLocale(explicit)
		return locale, pref.PersistPath, err
	}
	if environment := strings.TrimSpace(os.Getenv("CARINA_LOCALE")); environment != "" {
		locale, err := microcopy.CanonicalLocale(environment)
		if err != nil {
			return "", "", nil
		}
		return locale, pref.PersistPath, nil
	}
	if pref.Valid {
		return pref.Canonical, pref.PersistPath, nil
	}
	return "", pref.PersistPath, nil
}

func resolveRustUIBinary() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv("CARINA_UI_BIN")); explicit != "" {
		if executableFile(explicit) {
			return explicit, nil
		}
		return "", fmt.Errorf("CARINA_UI_BIN is not an executable file: %s", explicit)
	}
	if executable, err := uiExecutable(); err == nil {
		dir := filepath.Dir(executable)
		for _, candidate := range []string{
			filepath.Join(dir, rustUIBinaryName),
			filepath.Join(dir, "..", "target", "debug", rustUIBinaryName),
			filepath.Join(dir, "..", "target", "release", rustUIBinaryName),
		} {
			if executableFile(candidate) {
				absolute, absErr := filepath.Abs(candidate)
				if absErr == nil {
					return absolute, nil
				}
				return candidate, nil
			}
		}
	}
	for _, candidate := range []string{
		filepath.Join("target", "debug", rustUIBinaryName),
		filepath.Join("target", "release", rustUIBinaryName),
	} {
		if executableFile(candidate) {
			absolute, err := filepath.Abs(candidate)
			if err == nil {
				return absolute, nil
			}
		}
	}
	if found, err := uiLookPath(rustUIBinaryName); err == nil && executableFile(found) {
		return found, nil
	}
	return "", fmt.Errorf("internal %s binary is missing; reinstall Carina or run `make all`", rustUIBinaryName)
}

func executableFile(path string) bool {
	info, err := uiStat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func outcomeFromExitCode(code int) outcome.Outcome {
	if code < outcome.OutcomeOK.ExitCode() || code > outcome.OutcomeUserDenied.ExitCode() {
		return outcome.OutcomeRuntimeError
	}
	return outcome.Outcome(code)
}

// Caller is the minimal RPC surface doctor / session helpers need.
type Caller interface {
	Call(method string, params any, result any) error
	Close() error
}

// ensureDaemonReachable dials the daemon socket, auto-starting carina-daemon
// when unreachable. Used by `carina daemon start` and other CLI paths.
func ensureDaemonReachable(socket string) (*rpcClient, error) {
	origDial, origSpawn, origDeadline := localdaemon.Dial, localdaemon.Spawn, localdaemon.ReachableDeadline
	localdaemon.Dial = dialSocketHook
	localdaemon.Spawn = func(sock string) error {
		_ = sock
		return spawnDaemonHook()
	}
	localdaemon.ReachableDeadline = daemonReachableDeadline
	defer func() {
		localdaemon.Dial, localdaemon.Spawn, localdaemon.ReachableDeadline = origDial, origSpawn, origDeadline
	}()
	return localdaemon.EnsureReachable(socket)
}

// daemonStartupBackoff mirrors go/localdaemon retry cadence (kept for CLI unit tests).
func daemonStartupBackoff(attempt int) time.Duration {
	d := 100 * time.Millisecond * time.Duration(attempt+1)
	if d > time.Second {
		d = time.Second
	}
	return d
}

// resolveBareTUILocale kept for bare_invocation_test locale precedence.
func resolveBareTUILocale(configLocale string) (string, error) {
	return microcopy.ResolveLocale("", configLocale)
}
