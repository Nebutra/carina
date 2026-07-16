// carina-tui is the interactive terminal client for the Carina runtime: a
// live session transcript, kernel-backed approval prompts, and Ctrl-C as an
// audited cascading cancel. This binary is deliberately thin — the model,
// update logic, views, theme, and diff renderer live in go/tui so the CLI
// renderer can share them (one engine, two renderers).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/config"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui"
	"github.com/Nebutra/carina/go/tui/theme"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	bootstrapLocale := bootstrapLocaleForArgs(args)
	fs := flag.NewFlagSet("carina-tui", flag.ContinueOnError)
	var parseOutput strings.Builder
	fs.SetOutput(&parseOutput)
	socket := fs.String("socket", defaultSocket(), microcopy.Bootstrap(microcopy.BootstrapFlagSocket, nil, bootstrapLocale))
	session := fs.String("session", "", microcopy.Bootstrap(microcopy.BootstrapFlagSession, nil, bootstrapLocale))
	workspace := fs.String("workspace", "", microcopy.Bootstrap(microcopy.BootstrapFlagWorkspace, nil, bootstrapLocale))
	locale := &localeFlagValue{}
	fs.Var(locale, "locale", microcopy.Bootstrap(microcopy.BootstrapFlagLocale, nil, bootstrapLocale))
	noAltScreen := fs.Bool("no-alt-screen", false, microcopy.Bootstrap(microcopy.BootstrapFlagNoAltScreen, nil, bootstrapLocale))
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), microcopy.Bootstrap(microcopy.BootstrapTUIUsage, nil, bootstrapLocale))
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if locale.invalid {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage.ExitCode()
		}
		_, _ = io.WriteString(stderr, parseOutput.String())
		return tui.OutcomeUsage.ExitCode()
	}
	if value := os.Getenv("CARINA_LOCALE"); locale.raw == "" && value != "" {
		if _, err := microcopy.CanonicalLocale(value); err != nil {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage.ExitCode()
		}
	}
	if !isTTY(os.Stdout) || !isTTY(os.Stdin) {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapInteractiveRequired, nil, bootstrapLocale))
		return tui.OutcomeUsage.ExitCode()
	}

	th := theme.New(theme.Detect(os.Getenv, true))
	projectRoot := *workspace
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapResolveHomeFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError.ExitCode()
	}
	cfg, err := config.Load(home, projectRoot)
	if err != nil {
		if errors.Is(err, config.ErrInvalidTUILocale) {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage.ExitCode()
		}
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeRuntimeError.ExitCode()
	}
	loc, err := resolveTUILocale(locale.raw, cfg.TUILocale)
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, microcopy.Args{"reason": err.Error()}, bootstrapLocale))
		return tui.OutcomeUsage.ExitCode()
	}
	keybindings, err := tui.ParseKeyBindingOverrides(cfg.TUIKeybindings)
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapConfigFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeUsage.ExitCode()
	}
	sessionID := *session
	if sessionID == "" {
		sessionID, err = tui.LatestPendingSubmissionSession(cfg.StateDir, projectRoot)
		if err != nil {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapRecoveryFailed, microcopy.Args{"reason": err.Error()}, loc))
			return tui.OutcomeRuntimeError.ExitCode()
		}
	}

	connectionController := tui.NewConnectionController()
	model, err := tui.NewChecked(tui.Options{
		Theme:             th,
		Locale:            loc,
		Socket:            *socket,
		SessionID:         sessionID,
		WorkspaceRoot:     projectRoot,
		StateDir:          cfg.StateDir,
		Keybindings:       keybindings,
		NoAlternateScreen: *noAltScreen || strings.EqualFold(cfg.TUIAlternateScreen, "never"),
		KeymapUpdater:     keymapUpdater(home, projectRoot),
		SwitchSession:     connectionController.Switch,
	})
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapStartupFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeUsage.ExitCode()
	}
	defer model.Close()
	prog := tea.NewProgram(model)
	watchCtx, stopWatching := context.WithCancel(context.Background())
	defer stopWatching()
	go tui.WatchKeybindings(watchCtx, home, projectRoot, prog)
	tui.ConnectControlled(prog, *socket, sessionID, projectRoot, connectionController)

	final, err := prog.Run()
	if err != nil {
		fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapRuntimeFailed, microcopy.Args{"reason": err.Error()}, loc))
		return tui.OutcomeRuntimeError.ExitCode()
	}
	if m, ok := final.(*tui.Model); ok {
		return m.Outcome().ExitCode()
	}
	return tui.OutcomeOK.ExitCode()
}

type localeFlagValue struct {
	raw     string
	invalid bool
}

func (v *localeFlagValue) String() string { return v.raw }

func (v *localeFlagValue) Set(raw string) error {
	if _, err := microcopy.CanonicalLocale(raw); err != nil {
		v.invalid = true
		return err
	}
	v.raw = raw
	return nil
}

// bootstrapLocaleForArgs mirrors the small startup FlagSet closely enough to
// let a valid --locale select help copy before flag.Parse handles --help. It
// never accepts a locale itself: malformed values still reach localeFlagValue
// and fail through the formal parser.
func bootstrapLocaleForArgs(args []string) string {
	locale := microcopy.DetectBootstrapLocale()
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || arg == "-" || !strings.HasPrefix(arg, "-") {
			break
		}
		flagText := strings.TrimPrefix(arg, "-")
		flagText = strings.TrimPrefix(flagText, "-")
		name, value, hasValue := strings.Cut(flagText, "=")
		switch name {
		case "h", "help":
			return locale
		case "locale":
			if !hasValue {
				if i+1 >= len(args) {
					return locale
				}
				i++
				value = args[i]
			}
			if canonical, err := microcopy.CanonicalLocale(value); err == nil {
				locale = canonical
			}
		case "socket", "session", "workspace":
			if !hasValue {
				if i+1 >= len(args) {
					return locale
				}
				i++
			}
		case "no-alt-screen":
			// Boolean flags consume only their own token unless they use =value.
		default:
			return locale
		}
	}
	return locale
}

func resolveTUILocale(flagLocale, configLocale string) (string, error) {
	return microcopy.ResolveLocale(flagLocale, configLocale)
}

func keymapUpdater(home, projectRoot string) tui.KeymapUpdateFunc {
	path := filepath.Join(projectRoot, ".carina", "config.json")
	return func(action string, keys []string, remove bool) ([]tui.KeyBindingOverride, error) {
		_, locks, err := config.LoadWithManaged(home, projectRoot, config.DefaultManagedPath())
		if err != nil {
			return nil, err
		}
		if locks.Locked("tui_keybindings") {
			return nil, fmt.Errorf("tui_keybindings is locked by %s", locks.Source)
		}
		if err := config.UpdateTUIKeybinding(path, action, keys, remove); err != nil {
			return nil, err
		}
		cfg, err := config.Load(home, projectRoot)
		if err != nil {
			return nil, err
		}
		return tui.ParseKeyBindingOverrides(cfg.TUIKeybindings)
	}
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
