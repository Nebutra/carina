// Package tuiapp is the single interactive launch path for Carina.
//
// Operator entry (single binary, single shell):
//
//	carina                 # bare, on a TTY
//	carina [shell flags]   # same shell with -session / -workspace / …
//
// Auto-starts carina-daemon when the socket is down; UI is go/tui.
package tuiapp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/localruntime"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/rpc"
	"github.com/Nebutra/carina/go/tui"
	"github.com/Nebutra/carina/go/tui/theme"
)

// Options configures the interactive shell. Zero values pick sensible
// defaults (default socket, cwd workspace, config locale, auto session).
type Options struct {
	Socket        string
	SessionID     string // empty → pending / last-active / workspace-recent / fresh
	WorkspaceRoot string // empty → cwd
	Locale        string // flag locale; empty → config / env / system
	NoAltScreen   bool
	// Stderr receives bootstrap / recovery messages (default os.Stderr).
	Stderr io.Writer
	// AfterDaemon runs once the daemon is reachable and before the TUI
	// attaches (e.g. first-launch doctor). Failures must not abort launch.
	AfterDaemon func(call RPC)
	// RequireTTY refuses non-interactive stdio (flag-form `carina -…` sets true).
	// Bare no-arg `carina` decides TTY before calling Run.
	RequireTTY bool
}

// RPC is the minimal dial surface AfterDaemon / session resolution need.
type RPC interface {
	Call(method string, params any, result any) error
	Close() error
}

// Run starts the interactive TUI and returns its governance outcome.
func Run(opts Options) tui.Outcome {
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}
	bootstrapLocale := microcopy.DetectBootstrapLocale()

	// Validate explicit env locale before the TTY gate so
	// CARINA_LOCALE=de fails closed with a locale error even under go test
	// (non-TTY).
	if value := os.Getenv("CARINA_LOCALE"); opts.Locale == "" && value != "" {
		if _, err := microcopy.CanonicalLocale(value); err != nil {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage
		}
	}

	if opts.RequireTTY && (!isTTY(os.Stdout) || !isTTY(os.Stdin)) {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapInteractiveRequired, nil, bootstrapLocale))
		return tui.OutcomeUsage
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapResolveHomeFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}
	mode, err := localruntime.ResolveMode(home)
	if err != nil {
		if errors.Is(err, localruntime.ErrModeDecisionRequired) && isTTY(os.Stdout) && isTTY(os.Stdin) {
			mode, err = chooseRuntimeMode(os.Stdin, stderr, bootstrapLocale)
			if errors.Is(err, errRuntimeModeChoiceCancelled) {
				return tui.OutcomeOK
			}
			if err == nil {
				err = localruntime.WriteMode(home, mode)
			}
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}
	projectRoot := strings.TrimSpace(opts.WorkspaceRoot)
	var cfg config.Config
	var socket string
	var runtimeSpec *localruntime.Spec
	if mode == localruntime.ModeWorkspace {
		resolution, resolveErr := localruntime.Resolve(home, projectRoot, mode)
		if resolveErr != nil {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": resolveErr.Error()}, bootstrapLocale))
			return tui.OutcomeRuntimeError
		}
		if explicitSocket := strings.TrimSpace(opts.Socket); explicitSocket != "" {
			resolution, resolveErr = localruntime.ApplyOverrides(home, resolution, localruntime.Overrides{Socket: explicitSocket})
			if resolveErr != nil {
				fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": resolveErr.Error()}, bootstrapLocale))
				return tui.OutcomeRuntimeError
			}
		}
		cfg = resolution.Config
		projectRoot = resolution.Workspace.CanonicalRoot
		socket = resolution.Spec.Paths.SocketPath
		runtimeSpec = &resolution.Spec
	} else {
		if projectRoot == "" {
			projectRoot, err = os.Getwd()
			if err != nil {
				fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
				return tui.OutcomeRuntimeError
			}
		}
		cfg, err = config.Load(home, projectRoot)
		if err == nil {
			socket = strings.TrimSpace(opts.Socket)
			if socket == "" {
				socket = cfg.Socket
			}
		}
	}
	if err != nil {
		if errors.Is(err, config.ErrInvalidTUILocale) {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage
		}
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError
	}
	loc, err := microcopy.ResolveLocale(opts.Locale, cfg.TUILocale)
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeUsage
	}
	keybindings, err := tui.ParseKeyBindingOverrides(cfg.TUIKeybindings)
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeUsage
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		sessionID, err = resolveSession(nil, cfg.StateDir, projectRoot)
		if err != nil {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapRecoveryFailed, microcopy.Args{"reason": err.Error()}, loc))
			return tui.OutcomeRuntimeError
		}
	}

	th := theme.New(theme.Detect(os.Getenv, true))
	initialTarget := tui.ConnectionTarget{
		Socket: socket, SessionID: sessionID, WorkspaceRoot: projectRoot, StateDir: cfg.StateDir,
		RuntimeSpec: runtimeSpec, AutoStart: true,
	}
	connectionController := tui.NewConnectionController(initialTarget)
	coordinator := newRuntimeCoordinator(home, projectRoot, connectionController)
	defer coordinator.close()
	model, err := tui.NewChecked(tui.Options{
		Theme:             th,
		Locale:            loc,
		Socket:            socket,
		SessionID:         sessionID,
		WorkspaceRoot:     projectRoot,
		StateDir:          cfg.StateDir,
		Keybindings:       keybindings,
		NoAlternateScreen: opts.NoAltScreen || strings.EqualFold(cfg.TUIAlternateScreen, "never"),
		KeymapUpdater:     coordinator.keymapUpdater(),
		SwitchSession:     connectionController.Switch,
		PrepareTarget:     coordinator.prepare,
		CommitTarget:      coordinator.commit,
		AbortTarget:       coordinator.abort,
		ListWorkspaces:    coordinator.listWorkspaces,
		LoadWorkspace:     workspaceLoader(home),
		ResumeWorkspace:   workspaceResumer,
		OnFirstConnection: func(call tui.Caller) {
			if opts.AfterDaemon != nil {
				opts.AfterDaemon(borrowedRPC{Caller: call})
			}
		},
	})
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeUsage
	}
	defer model.Close()
	prog := tea.NewProgram(model)
	coordinator.startWatcher(prog)
	if runtimeSpec != nil {
		tui.ConnectControlledRuntime(prog, socket, sessionID, projectRoot, connectionController, *runtimeSpec)
	} else {
		tui.ConnectControlled(prog, socket, sessionID, projectRoot, connectionController)
	}

	final, err := prog.Run()
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapRuntimeFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeRuntimeError
	}
	if m, ok := final.(*tui.Model); ok {
		return m.Outcome()
	}
	return tui.OutcomeOK
}

type borrowedRPC struct{ tui.Caller }

func (borrowedRPC) Close() error { return nil }

var errRuntimeModeChoiceCancelled = errors.New("runtime mode choice cancelled")

type runtimeModeChoiceCopy struct {
	title, workspace, legacy, cancel, prompt, invalid string
}

func chooseRuntimeMode(in io.Reader, out io.Writer, locale string) (localruntime.Mode, error) {
	copy := runtimeModeChoiceText(locale)
	fmt.Fprintf(out, "%s\n  [w] %s\n  [l] %s\n  [c] %s\n%s", copy.title, copy.workspace, copy.legacy, copy.cancel, copy.prompt)
	scanner := bufio.NewScanner(in)
	for scanner.Scan() {
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "w", "workspace", "1":
			return localruntime.ModeWorkspace, nil
		case "l", "legacy", "2":
			return localruntime.ModeLegacy, nil
		case "c", "cancel", "q", "3", "":
			return "", errRuntimeModeChoiceCancelled
		default:
			fmt.Fprintf(out, "%s\n%s", copy.invalid, copy.prompt)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errRuntimeModeChoiceCancelled
}

func runtimeModeChoiceText(locale string) runtimeModeChoiceCopy {
	base := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(base, "-_@."); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "zh":
		return runtimeModeChoiceCopy{
			title:     "检测到旧版全局运行时数据。请选择 Carina 的项目运行方式：",
			workspace: "使用独立 workspace runtime（旧数据保持不变）",
			legacy:    "继续使用旧版全局 runtime", cancel: "取消并退出",
			prompt: "选择 [w/l/c]: ", invalid: "请输入 w、l 或 c。",
		}
	case "ja":
		return runtimeModeChoiceCopy{title: "従来のグローバルランタイムデータが見つかりました。", workspace: "分離された workspace runtime を使用", legacy: "従来のグローバル runtime を継続", cancel: "キャンセル", prompt: "選択 [w/l/c]: ", invalid: "w、l、c のいずれかを入力してください。"}
	case "ko":
		return runtimeModeChoiceCopy{title: "기존 전역 런타임 데이터가 발견되었습니다.", workspace: "격리된 workspace runtime 사용", legacy: "기존 전역 runtime 계속 사용", cancel: "취소", prompt: "선택 [w/l/c]: ", invalid: "w, l 또는 c를 입력하세요."}
	case "es":
		return runtimeModeChoiceCopy{title: "Se encontraron datos del runtime global anterior.", workspace: "Usar un runtime aislado por workspace", legacy: "Continuar con el runtime global anterior", cancel: "Cancelar", prompt: "Elige [w/l/c]: ", invalid: "Escribe w, l o c."}
	case "fr":
		return runtimeModeChoiceCopy{title: "Des données de l'ancien runtime global ont été détectées.", workspace: "Utiliser un runtime isolé par workspace", legacy: "Continuer avec l'ancien runtime global", cancel: "Annuler", prompt: "Choix [w/l/c] : ", invalid: "Saisissez w, l ou c."}
	default:
		return runtimeModeChoiceCopy{title: "Legacy global runtime data was found. Choose how Carina should run projects:", workspace: "Use an isolated workspace runtime (legacy data stays unchanged)", legacy: "Continue using the legacy global runtime", cancel: "Cancel and exit", prompt: "Choose [w/l/c]: ", invalid: "Enter w, l, or c."}
	}
}

func workspaceLoader(home string) func(string) (tui.WorkspaceDestination, error) {
	return func(root string) (tui.WorkspaceDestination, error) {
		resolution, err := localruntime.Resolve(home, root, localruntime.ModeWorkspace)
		if err != nil {
			return tui.WorkspaceDestination{}, err
		}
		client, _, err := localdaemon.ConnectOrStart(resolution.Spec)
		if err != nil {
			return tui.WorkspaceDestination{}, err
		}
		defer client.Close()
		var sessions []tui.SessionListItem
		if err := client.Call("session.list", map[string]any{}, &sessions); err != nil {
			return tui.WorkspaceDestination{}, err
		}
		return tui.WorkspaceDestination{
			Target: tui.ConnectionTarget{
				Socket: resolution.Spec.Paths.SocketPath, WorkspaceRoot: resolution.Workspace.CanonicalRoot,
				StateDir: resolution.Config.StateDir, RuntimeSpec: &resolution.Spec, AutoStart: true,
			},
			Sessions: sessions,
		}, nil
	}
}

func workspaceResumer(target tui.ConnectionTarget, sessionID string) (tui.ConnectionTarget, error) {
	if target.RuntimeSpec == nil {
		return tui.ConnectionTarget{}, fmt.Errorf("workspace runtime identity is required")
	}
	client, _, err := localdaemon.ConnectOrStart(*target.RuntimeSpec)
	if err != nil {
		return tui.ConnectionTarget{}, err
	}
	defer client.Close()
	var resumed tui.SessionListItem
	if err := client.Call("session.resume", map[string]any{"session_id": sessionID}, &resumed); err != nil {
		return tui.ConnectionTarget{}, err
	}
	if resumed.SessionID == "" {
		return tui.ConnectionTarget{}, fmt.Errorf("session resume returned no session id")
	}
	if resumed.WorkspaceRoot != "" && filepath.Clean(resumed.WorkspaceRoot) != filepath.Clean(target.WorkspaceRoot) {
		return tui.ConnectionTarget{}, fmt.Errorf("session workspace mismatch")
	}
	target.SessionID = resumed.SessionID
	return target, nil
}

func resolveSession(call RPC, stateDir, projectRoot string) (string, error) {
	sessionID, err := tui.LatestPendingSubmissionSession(stateDir, projectRoot)
	if err != nil {
		return "", err
	}
	if sessionID != "" {
		return sessionID, nil
	}
	sessionID, err = tui.LastActiveSession(stateDir, projectRoot)
	if err != nil {
		return "", err
	}
	if sessionID != "" {
		return sessionID, nil
	}
	// Fall through to daemon session.list for this workspace (bare-carina contract).
	if call == nil {
		return "", nil
	}
	var sessions []struct {
		SessionID     string `json:"session_id"`
		WorkspaceRoot string `json:"workspace_root"`
		CreatedAt     string `json:"created_at"`
	}
	if err := call.Call("session.list", map[string]any{}, &sessions); err != nil {
		return "", nil // degrade to fresh session
	}
	var latest string
	var latestAt string
	for _, s := range sessions {
		if s.WorkspaceRoot != projectRoot {
			continue
		}
		if latest == "" || s.CreatedAt > latestAt {
			latest = s.SessionID
			latestAt = s.CreatedAt
		}
	}
	return latest, nil
}

func defaultSocket() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".carina", "daemon.sock")
	}
	return filepath.Join(home, ".carina", "daemon.sock")
}

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Ensure rpc.Client satisfies RPC.
var _ RPC = (*rpc.Client)(nil)
