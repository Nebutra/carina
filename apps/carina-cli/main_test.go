package main

import "testing"

func TestParseRunArgsModel(t *testing.T) {
	prompt, model, err := parseRunArgs([]string{"--model", "openrouter/anthropic/claude-sonnet", "fix tests"})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "fix tests" || model != "openrouter/anthropic/claude-sonnet" {
		t.Fatalf("prompt=%q model=%q", prompt, model)
	}
}

func TestParseRunArgsShortModel(t *testing.T) {
	prompt, model, err := parseRunArgs([]string{"-m", "openai/gpt-5", "ship it"})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "ship it" || model != "openai/gpt-5" {
		t.Fatalf("prompt=%q model=%q", prompt, model)
	}
}

func TestParseRunArgsRequiresPrompt(t *testing.T) {
	if _, _, err := parseRunArgs([]string{"--model", "openai/gpt-5"}); err == nil {
		t.Fatal("missing prompt should error")
	}
}
