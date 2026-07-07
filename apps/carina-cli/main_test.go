package main

import (
	"strings"
	"testing"
)

func TestParseRunArgsModel(t *testing.T) {
	prompt, model, agent, err := parseRunArgs([]string{"--model", "openrouter/anthropic/claude-sonnet", "fix tests"})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "fix tests" || model != "openrouter/anthropic/claude-sonnet" || agent != "" {
		t.Fatalf("prompt=%q model=%q agent=%q", prompt, model, agent)
	}
}

func TestParseRunArgsShortModel(t *testing.T) {
	prompt, model, agent, err := parseRunArgs([]string{"-m", "openai/gpt-5", "-a", "plan", "ship it"})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "ship it" || model != "openai/gpt-5" || agent != "plan" {
		t.Fatalf("prompt=%q model=%q agent=%q", prompt, model, agent)
	}
}

func TestParseRunArgsRequiresPrompt(t *testing.T) {
	if _, _, _, err := parseRunArgs([]string{"--model", "openai/gpt-5"}); err == nil {
		t.Fatal("missing prompt should error")
	}
}

func TestUsageIsProductizedAndCarinaOnly(t *testing.T) {
	for _, want := range []string{"Start and run:", "Inspect sessions:", "Audit and rollback:", "Providers and BYOK:", "Native tools, no daemon:"} {
		if !strings.Contains(usage, want) {
			t.Fatalf("usage missing section %q", want)
		}
	}
	if strings.Contains(usage, " pi ") || strings.Contains(usage, "PI_") {
		t.Fatalf("usage must not expose historical aliases:\n%s", usage)
	}
}
