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
	var errOut strings.Builder
	got := run(nil, &errOut)
	if got != 2 {
		t.Fatalf("exit code = %d, want 2 (usage)", got)
	}
	if !strings.Contains(errOut.String(), "interactive terminal") {
		t.Fatalf("stderr missing TTY guidance: %q", errOut.String())
	}
}
