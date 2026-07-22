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

func TestSelectReasonerBackendIsProviderFirst(t *testing.T) {
	tests := []struct {
		name     string
		offline  bool
		backend  string
		model    string
		runnable bool
		want     string
	}{
		{name: "offline", offline: true, backend: reasonerBackendClaudeCLI, runnable: true, want: reasonerBackendNone},
		{name: "auto provider", backend: reasonerBackendAuto, runnable: true, want: reasonerBackendRouter},
		{name: "auto explicit model unavailable", backend: reasonerBackendAuto, model: "openai/gpt-5", want: reasonerBackendNone},
		{name: "auto explicit model available", backend: reasonerBackendAuto, model: "openai/gpt-5", runnable: true, want: reasonerBackendRouter},
		{name: "auto unavailable", backend: reasonerBackendAuto, want: reasonerBackendNone},
		{name: "explicit router", backend: reasonerBackendRouter, want: reasonerBackendRouter},
		{name: "explicit claude", backend: reasonerBackendClaudeCLI, runnable: true, want: reasonerBackendClaudeCLI},
		{name: "explicit codex", backend: reasonerBackendCodexCLI, runnable: true, want: reasonerBackendCodexCLI},
		{name: "offline codex", offline: true, backend: reasonerBackendCodexCLI, runnable: true, want: reasonerBackendNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := selectReasonerBackend(test.offline, test.backend, test.model, test.runnable); got != test.want {
				t.Fatalf("backend = %q, want %q", got, test.want)
			}
		})
	}
}
