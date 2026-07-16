// carina-tui is a thin alias of the unified interactive shell.
//
// Preferred entries (same code path):
//
//	carina            # bare, on a TTY
//	carina tui [...]  # explicit
//	carina-tui [...]  # this binary
//
// The model lives in go/tui; launch lives in go/tuiapp.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui"
	"github.com/Nebutra/carina/go/tuiapp"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	bootstrapLocale := bootstrapLocaleForArgs(args)
	fs := flag.NewFlagSet("carina-tui", flag.ContinueOnError)
	var parseOutput strings.Builder
	fs.SetOutput(&parseOutput)
	socket := fs.String("socket", "", microcopy.Bootstrap(microcopy.BootstrapFlagSocket, nil, bootstrapLocale))
	session := fs.String("session", "", microcopy.Bootstrap(microcopy.BootstrapFlagSession, nil, bootstrapLocale))
	workspace := fs.String("workspace", "", microcopy.Bootstrap(microcopy.BootstrapFlagWorkspace, nil, bootstrapLocale))
	locale := &localeFlagValue{}
	fs.Var(locale, "locale", microcopy.Bootstrap(microcopy.BootstrapFlagLocale, nil, bootstrapLocale))
	noAltScreen := fs.Bool("no-alt-screen", false, microcopy.Bootstrap(microcopy.BootstrapFlagNoAltScreen, nil, bootstrapLocale))
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), microcopy.Bootstrap(microcopy.BootstrapTUIUsage, nil, bootstrapLocale))
		fs.PrintDefaults()
		fmt.Fprintln(fs.Output(), "\nPreferred: carina   or   carina tui [options]")
	}
	if err := fs.Parse(args); err != nil {
		if locale.invalid {
			fmt.Fprintln(stderr, microcopy.Bootstrap(microcopy.BootstrapLocaleInvalid, nil, bootstrapLocale))
			return tui.OutcomeUsage.ExitCode()
		}
		_, _ = io.WriteString(stderr, parseOutput.String())
		return tui.OutcomeUsage.ExitCode()
	}
	return tuiapp.Run(tuiapp.Options{
		Socket:        *socket,
		SessionID:     *session,
		WorkspaceRoot: *workspace,
		Locale:        locale.raw,
		NoAltScreen:   *noAltScreen,
		Stderr:        stderr,
		RequireTTY:    true,
	}).ExitCode()
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

// resolveTUILocale kept for main_test locale precedence.
func resolveTUILocale(flagLocale, configLocale string) (string, error) {
	return microcopy.ResolveLocale(flagLocale, configLocale)
}

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
		default:
			return locale
		}
	}
	return locale
}
