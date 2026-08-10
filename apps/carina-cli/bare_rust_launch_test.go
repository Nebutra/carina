package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/localdaemon"
	"github.com/Nebutra/carina/go/outcome"
)

func TestBuildRustUIArgsUsesStructuredNonSecretFields(t *testing.T) {
	opts := interactiveOptions{SessionID: " session-1 ", NoAltScreen: true}
	got := buildRustUIArgs(opts, "/tmp/carina.sock", "/work/project", "zh-Hant", "/home/user/.carina/config.json", "comfortable", "/work/project/.carina/config.json", "/opt/carina/bin/carina", "always")
	want := []string{
		"--socket", "/tmp/carina.sock",
		"--workspace", "/work/project",
		"--carina-bin", "/opt/carina/bin/carina",
		"--session", "session-1",
		"--locale", "zh-Hant",
		"--locale-path", "/home/user/.carina/config.json",
		"--density", "comfortable",
		"--density-path", "/work/project/.carina/config.json",
		"--no-alt-screen",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildRustUIArgs() = %#v, want %#v", got, want)
	}
	if strings.Contains(strings.Join(got, " "), "credential") {
		t.Fatal("launcher arguments must never contain credential fields")
	}
}

func TestBuildRustUIArgsPassesConfiguredAltScreenPolicy(t *testing.T) {
	got := buildRustUIArgs(interactiveOptions{}, "/tmp/carina.sock", "/work/project", "en", "", "compact", "", "/opt/carina/bin/carina", "never")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--alt-screen never") {
		t.Fatalf("launcher args %#v did not pass configured terminal policy", got)
	}
}

func TestBuildRustUIArgsPassesFirstClassScreenMode(t *testing.T) {
	got := buildRustUIArgs(interactiveOptions{ScreenMode: " fullscreen "}, "/tmp/carina.sock", "/work/project", "en", "", "compact", "", "/opt/carina/bin/carina", "never")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--screen-mode fullscreen") {
		t.Fatalf("launcher args %#v did not pass screen mode", got)
	}
	if strings.Contains(joined, "--alt-screen") {
		t.Fatalf("first-class screen mode must supersede legacy alt policy: %#v", got)
	}
}

func TestBuildRuntimeDiagnosticArgsRetainsSafeRecoveryContext(t *testing.T) {
	compatibility := &localdaemon.RuntimeCompatibilityError{
		Description: localdaemon.RuntimeDescription{
			RuntimeID:   "runtime_old",
			Obligations: []string{"execution:run_active"},
		},
		MissingMethods: []string{"execution.start"},
	}
	got := buildRuntimeDiagnosticArgs(
		interactiveOptions{NoAltScreen: true},
		"/work/project",
		"zh-Hans",
		"/opt/carina/bin/carina",
		"/state/runtime.log",
		compatibility,
	)
	joined := strings.Join(got, " ")
	for _, field := range []string{
		"--runtime-diagnostic", "runtime_old", "execution.start",
		"execution:run_active", "/state/runtime.log", "--no-alt-screen",
	} {
		if !strings.Contains(joined, field) {
			t.Fatalf("diagnostic args %#v missing %q", got, field)
		}
	}
}

func TestInternalRustUIIsNotAPublicCommand(t *testing.T) {
	if strings.Contains(usage, rustUIBinaryName) {
		t.Fatalf("usage exposes internal helper %q", rustUIBinaryName)
	}
}

func TestRuntimeModeDecisionRoutesToInternalProductScene(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".carina", "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(t.TempDir(), rustUIBinaryName)
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CARINA_LOCALE", "")
	t.Setenv("CARINA_TUI_LOCALE", "")
	t.Setenv("CARINA_UI_BIN", helper)

	originalRunner := uiRunProcess
	defer func() { uiRunProcess = originalRunner }()
	var gotBinary string
	var gotArgs []string
	uiRunProcess = func(binary string, args []string) (int, error) {
		gotBinary = binary
		gotArgs = append([]string(nil), args...)
		return 0, nil
	}

	if got := runTUI(interactiveOptions{WorkspaceRoot: t.TempDir(), NoAltScreen: true}); got != outcome.OutcomeOK {
		t.Fatalf("runTUI mode setup outcome = %v", got)
	}
	if gotBinary != helper {
		t.Fatalf("mode setup binary = %q, want %q", gotBinary, helper)
	}
	want := []string{"--runtime-mode-setup", "--home", home, "--carina-bin"}
	joined := strings.Join(gotArgs, " ")
	for _, field := range want {
		if !strings.Contains(joined, field) {
			t.Fatalf("mode setup args %#v missing %q", gotArgs, field)
		}
	}
	if gotArgs[len(gotArgs)-1] != "--no-alt-screen" {
		t.Fatalf("mode setup args = %#v, want no-alt-screen preserved", gotArgs)
	}
}

func TestResolveRustUIBinaryPrefersExplicitExecutable(t *testing.T) {
	path := filepath.Join(t.TempDir(), rustUIBinaryName)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARINA_UI_BIN", path)
	got, err := resolveRustUIBinary()
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolveRustUIBinary() = %q, want %q", got, path)
	}
}

func TestResolveRustUIBinaryRejectsMissingExplicitOverride(t *testing.T) {
	t.Setenv("CARINA_UI_BIN", filepath.Join(t.TempDir(), "missing"))
	if _, err := resolveRustUIBinary(); err == nil || !strings.Contains(err.Error(), "CARINA_UI_BIN") {
		t.Fatalf("resolveRustUIBinary() error = %v, want actionable override error", err)
	}
}

func TestResolveInteractiveLocaleDefersCleanHomeToProductSelector(t *testing.T) {
	t.Setenv("CARINA_LOCALE", "")
	t.Setenv("CARINA_TUI_LOCALE", "")
	home := t.TempDir()
	workspace := t.TempDir()
	locale, path, err := resolveInteractiveLocale(home, workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	if locale != "" {
		t.Fatalf("clean-home locale = %q, want product selector", locale)
	}
	wantPath := filepath.Join(home, ".carina", "config.json")
	if path != wantPath {
		t.Fatalf("locale path = %q, want %q", path, wantPath)
	}
}

func TestResolveInteractiveLocaleCanonicalizesExplicitFlag(t *testing.T) {
	t.Setenv("CARINA_LOCALE", "")
	t.Setenv("CARINA_TUI_LOCALE", "")
	locale, _, err := resolveInteractiveLocale(t.TempDir(), t.TempDir(), "fr-FR")
	if err != nil {
		t.Fatal(err)
	}
	if locale != "fr" {
		t.Fatalf("locale = %q, want fr", locale)
	}
}

func TestOutcomeFromRustUIExitCodePreservesGovernanceContract(t *testing.T) {
	for code := 0; code <= 7; code++ {
		if got := outcomeFromExitCode(code).ExitCode(); got != code {
			t.Fatalf("outcomeFromExitCode(%d) = %d", code, got)
		}
	}
	if got := outcomeFromExitCode(127); got != outcome.OutcomeRuntimeError {
		t.Fatalf("unexpected exit code maps to %v, want runtime error", got)
	}
}
