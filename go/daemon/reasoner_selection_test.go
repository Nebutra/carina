package daemon

import (
	"strings"
	"testing"
)

func TestNormalizeReasonerBackend(t *testing.T) {
	tests := map[string]string{
		"":             reasonerBackendAuto,
		"auto":         reasonerBackendAuto,
		"router":       reasonerBackendRouter,
		"model-router": reasonerBackendRouter,
		"claude":       reasonerBackendClaudeCLI,
		"claude-cli":   reasonerBackendClaudeCLI,
		"codex":        reasonerBackendCodexCLI,
		"codex-cli":    reasonerBackendCodexCLI,
	}
	for input, want := range tests {
		got, err := normalizeReasonerBackend(input)
		if err != nil || got != want {
			t.Errorf("normalizeReasonerBackend(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := normalizeReasonerBackend("unknown"); err == nil {
		t.Fatal("unknown backend must be rejected")
	} else if !strings.Contains(err.Error(), "codex-cli") {
		t.Fatalf("unknown backend error = %q, want codex-cli guidance", err)
	}
}

func TestSelectReasonerBackendKeepsAutoRouterStable(t *testing.T) {
	tests := []struct {
		name    string
		offline bool
		backend string
		want    string
	}{
		{name: "offline", offline: true, backend: reasonerBackendClaudeCLI, want: reasonerBackendNone},
		{name: "auto provider", backend: reasonerBackendAuto, want: reasonerBackendRouter},
		{name: "auto unavailable", backend: reasonerBackendAuto, want: reasonerBackendRouter},
		{name: "explicit router", backend: reasonerBackendRouter, want: reasonerBackendRouter},
		{name: "explicit claude", backend: reasonerBackendClaudeCLI, want: reasonerBackendClaudeCLI},
		{name: "explicit codex", backend: reasonerBackendCodexCLI, want: reasonerBackendCodexCLI},
		{name: "offline codex", offline: true, backend: reasonerBackendCodexCLI, want: reasonerBackendNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := selectReasonerBackend(test.offline, test.backend); got != test.want {
				t.Fatalf("backend = %q, want %q", got, test.want)
			}
		})
	}
}
