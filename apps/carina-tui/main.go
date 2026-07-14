// carina-tui is the interactive terminal client for the Carina runtime: a
// live session transcript, kernel-backed approval prompts, and Ctrl-C as an
// audited cascading cancel. This binary is deliberately thin — the model,
// update logic, views, theme, and diff renderer live in go/tui so the CLI
// renderer can share them (one engine, two renderers).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

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
	fs := flag.NewFlagSet("carina-tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", defaultSocket(), "carina daemon unix socket")
	session := fs.String("session", "", "attach to an existing session id (default: create one)")
	workspace := fs.String("workspace", "", "workspace root for session.create (default: cwd)")
	locale := fs.String("locale", "", "copy locale (en, zh; default: CARINA_LOCALE/LC_ALL/LANG)")
	if err := fs.Parse(args); err != nil {
		return tui.OutcomeUsage.ExitCode()
	}
	if !isTTY(os.Stdout) || !isTTY(os.Stdin) {
		fmt.Fprintln(stderr, "carina-tui requires an interactive terminal; use `carina watch --json` for pipes.")
		return tui.OutcomeUsage.ExitCode()
	}

	loc := *locale
	if loc == "" {
		loc = microcopy.DetectLocale()
	}
	th := theme.New(theme.Detect(os.Getenv, true))
	projectRoot := *workspace
	if projectRoot == "" {
		projectRoot, _ = os.Getwd()
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "carina-tui: resolve home: %v\n", err)
		return tui.OutcomeRuntimeError.ExitCode()
	}
	cfg, err := config.Load(home, projectRoot)
	if err != nil {
		fmt.Fprintf(stderr, "carina-tui: %v\n", err)
		return tui.OutcomeRuntimeError.ExitCode()
	}
	keybindings, err := tui.ParseKeyBindingOverrides(cfg.TUIKeybindings)
	if err != nil {
		fmt.Fprintf(stderr, "carina-tui: %v\n", err)
		return tui.OutcomeUsage.ExitCode()
	}
	sessionID := *session
	if sessionID == "" {
		sessionID, err = tui.LatestPendingSubmissionSession(cfg.StateDir, projectRoot)
		if err != nil {
			fmt.Fprintf(stderr, "carina-tui: submission recovery: %v\n", err)
			return tui.OutcomeRuntimeError.ExitCode()
		}
	}

	model, err := tui.NewChecked(tui.Options{
		Theme:         th,
		Locale:        loc,
		Socket:        *socket,
		SessionID:     sessionID,
		WorkspaceRoot: projectRoot,
		StateDir:      cfg.StateDir,
		Keybindings:   keybindings,
	})
	if err != nil {
		fmt.Fprintf(stderr, "carina-tui: %v\n", err)
		return tui.OutcomeUsage.ExitCode()
	}
	defer model.Close()
	prog := tea.NewProgram(model)
	tui.Connect(prog, *socket, sessionID, projectRoot)

	final, err := prog.Run()
	if err != nil {
		fmt.Fprintf(stderr, "carina-tui: %v\n", err)
		return tui.OutcomeRuntimeError.ExitCode()
	}
	if m, ok := final.(*tui.Model); ok {
		return m.Outcome().ExitCode()
	}
	return tui.OutcomeOK.ExitCode()
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
