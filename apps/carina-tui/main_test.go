package main

import (
	"strings"
	"testing"
)

// Bad flags exit with the governance usage code (2), not a generic 1.
func TestRunUsageExitCode(t *testing.T) {
	var errOut strings.Builder
	if got := run([]string{"-definitely-not-a-flag"}, &errOut); got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage)", got)
	}
}

// Under `go test` stdout is not a TTY: the TUI must refuse with the usage
// code and a pointer at the pipe-friendly surface, never start half-blind.
func TestRunRequiresTTY(t *testing.T) {
	// Pin English so the assertion is independent of the host LANG.
	for _, key := range []string{"CARINA_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(key, "")
	}
	t.Setenv("LANG", "en_US.UTF-8")

	var errOut strings.Builder
	got := run(nil, &errOut)
	if got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage)", got)
	}
	if !strings.Contains(errOut.String(), "interactive terminal") {
		t.Fatalf("stderr missing TTY guidance: %q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "carina watch --json") {
		t.Fatalf("stderr missing pipe-friendly surface pointer: %q", errOut.String())
	}
}

func TestResolveTUILocalePrecedence(t *testing.T) {
	for _, key := range []string{"CARINA_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(key, "")
	}
	t.Setenv("LANG", "zh_CN.UTF-8")
	t.Setenv("LC_MESSAGES", "es_ES.UTF-8")
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	t.Setenv("CARINA_LOCALE", "ko_KR.UTF-8")

	if got, err := resolveTUILocale("ja-JP", "es-ES"); err != nil || got != "ja" {
		t.Fatalf("flag locale = %q, want ja", got)
	}
	if got, err := resolveTUILocale("", "es-ES"); err != nil || got != "ko" {
		t.Fatalf("CARINA_LOCALE = %q, want ko", got)
	}
	t.Setenv("CARINA_LOCALE", "")
	if got, err := resolveTUILocale("", "es-ES"); err != nil || got != "es" {
		t.Fatalf("config tui_locale = %q, want es", got)
	}
	if got, err := resolveTUILocale("", ""); err != nil || got != "fr" {
		t.Fatalf("system locale = %q, want fr", got)
	}
}

func TestResolveTUILocaleRejectsExplicitUnsupportedValues(t *testing.T) {
	for _, key := range []string{"CARINA_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(key, "")
	}
	if got, err := resolveTUILocale("zh-TW", ""); err != nil || got != "zh-Hant" {
		t.Fatalf("zh-TW locale = %q, %v; want zh-Hant", got, err)
	}
	t.Setenv("CARINA_LOCALE", "de-DE")
	if _, err := resolveTUILocale("", ""); err == nil {
		t.Fatal("unsupported CARINA_LOCALE must fail")
	}
}

func TestLocalizedBootstrapUsageAndLocaleError(t *testing.T) {
	for _, key := range []string{"CARINA_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(key, "")
	}
	t.Setenv("LANG", "zh_CN.UTF-8")

	var help strings.Builder
	if got := run([]string{"--help"}, &help); got != 2 {
		t.Fatalf("help exit = %d, want usage", got)
	}
	for _, english := range []string{"unix socket", "attach to an existing", "workspace root", "copy locale", "preserve native scrollback"} {
		if strings.Contains(strings.ToLower(help.String()), english) {
			t.Errorf("localized help contains English description %q: %q", english, help.String())
		}
	}

	var invalid strings.Builder
	if got := run([]string{"--locale", "de-DE"}, &invalid); got != 2 {
		t.Fatalf("invalid locale exit = %d, want usage", got)
	}
	if strings.Contains(invalid.String(), "unsupported locale") || !strings.Contains(invalid.String(), "支持") {
		t.Fatalf("invalid locale message is mixed or not localized: %q", invalid.String())
	}

	t.Setenv("CARINA_LOCALE", "de-DE")
	var envInvalid strings.Builder
	if got := run(nil, &envInvalid); got != 2 {
		t.Fatalf("invalid env locale exit = %d, want usage", got)
	}
	if strings.Contains(envInvalid.String(), "interactive terminal") || !strings.Contains(envInvalid.String(), "支持") {
		t.Fatalf("invalid env locale was not rejected before TTY startup: %q", envInvalid.String())
	}
}

func TestExplicitLocaleSelectsBootstrapHelpBeforeParse(t *testing.T) {
	for _, key := range []string{"CARINA_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(key, "")
	}
	for _, args := range [][]string{{"--locale", "ja", "--help"}, {"--locale=ja", "--help"}} {
		var help strings.Builder
		if got := run(args, &help); got != 2 {
			t.Fatalf("run(%v) = %d, want usage", args, got)
		}
		if !strings.Contains(help.String(), "使い方") || !strings.Contains(help.String(), "Carina デーモン") {
			t.Errorf("run(%v) did not use explicit locale for help: %q", args, help.String())
		}
		if strings.Contains(help.String(), "Carina daemon Unix socket") {
			t.Errorf("run(%v) leaked English help: %q", args, help.String())
		}
	}
}

func TestLocalePrescanDoesNotBypassFormalParseErrors(t *testing.T) {
	for _, key := range []string{"CARINA_LOCALE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		t.Setenv(key, "")
	}
	var missing strings.Builder
	if got := run([]string{"--locale"}, &missing); got != 2 {
		t.Fatalf("missing locale value exit = %d, want usage", got)
	}
	if !strings.Contains(missing.String(), "flag needs an argument") {
		t.Fatalf("missing locale value did not reach flag.Parse: %q", missing.String())
	}

	var hantHelp strings.Builder
	if got := run([]string{"--locale", "zh-Hant", "--help"}, &hantHelp); got != 2 {
		t.Fatalf("zh-Hant help exit = %d, want usage", got)
	}
	// Traditional help should not be English Usage.
	if strings.Contains(hantHelp.String(), "Carina daemon Unix socket") {
		t.Fatalf("zh-Hant help leaked English: %q", hantHelp.String())
	}
}
