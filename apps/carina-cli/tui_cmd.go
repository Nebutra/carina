package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui"
	"github.com/Nebutra/carina/go/tuiapp"
)

// cmdTUI is `carina tui` — explicit flags for the same shell as bare `carina`.
func cmdTUI(args []string) tui.Outcome {
	bootstrapLocale := microcopy.DetectBootstrapLocale()
	fs := flag.NewFlagSet("carina tui", flag.ContinueOnError)
	var parseOutput strings.Builder
	fs.SetOutput(&parseOutput)
	socket := fs.String("socket", "", microcopy.Bootstrap(microcopy.BootstrapFlagSocket, nil, bootstrapLocale))
	session := fs.String("session", "", microcopy.Bootstrap(microcopy.BootstrapFlagSession, nil, bootstrapLocale))
	workspace := fs.String("workspace", "", microcopy.Bootstrap(microcopy.BootstrapFlagWorkspace, nil, bootstrapLocale))
	locale := ""
	fs.Func("locale", microcopy.Bootstrap(microcopy.BootstrapFlagLocale, nil, bootstrapLocale), func(raw string) error {
		if _, err := microcopy.CanonicalLocale(raw); err != nil {
			return err
		}
		locale = raw
		return nil
	})
	noAltScreen := fs.Bool("no-alt-screen", false, microcopy.Bootstrap(microcopy.BootstrapFlagNoAltScreen, nil, bootstrapLocale))
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: carina tui [options]   (or bare: carina)")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		fmt.Fprint(os.Stderr, parseOutput.String())
		return tui.OutcomeUsage
	}
	return runTUI(tuiapp.Options{
		Socket:        *socket,
		SessionID:     *session,
		WorkspaceRoot: *workspace,
		Locale:        locale,
		NoAltScreen:   *noAltScreen,
		RequireTTY:    true,
	})
}
