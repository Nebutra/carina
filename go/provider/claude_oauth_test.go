package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseClaudeCodeOAuthPayloadKeychainShape(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "sk-ant-oat01-test-token",
			"expiresAt":   9_999_999_999_999,
			"scopes":      []string{"user:inference"},
		},
	})
	tok, ok := parseClaudeCodeOAuthPayload(raw)
	if !ok || tok != "sk-ant-oat01-test-token" {
		t.Fatalf("parse = %q %v", tok, ok)
	}
}

func TestParseClaudeCodeOAuthPayloadRejectsExpiredAndNonOAuth(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "sk-ant-oat01-expired",
			"expiresAt":   1,
		},
	})
	if tok, ok := parseClaudeCodeOAuthPayload(raw); ok {
		t.Fatalf("expired token accepted: %q", tok)
	}
	if tok, ok := parseClaudeCodeOAuthPayload([]byte(`{"accessToken":"sk-ant-api03-not-oauth"}`)); ok {
		t.Fatalf("API key accepted as OAuth: %q", tok)
	}
}

func TestLookupClaudeCodeOAuthFromFiles(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Force UserHomeDir via HOME on unix.
	credDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken": "sk-ant-oat01-from-file",
			"expiresAt":   9_999_999_999_999,
		},
	})
	if err := os.WriteFile(filepath.Join(credDir, ".credentials.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	tok, ok := lookupClaudeCodeOAuthFromFiles()
	if !ok || tok != "sk-ant-oat01-from-file" {
		t.Fatalf("file lookup = %q %v (dir=%s)", tok, ok, dir)
	}
}

func TestResolveCCSwitchClaudeUsesClaudeCodeOAuthWhenNoAPIKey(t *testing.T) {
	prev := claudeCodeOAuthLookup
	claudeCodeOAuthLookup = func() (string, bool) { return "sk-ant-oat01-imported", true }
	t.Cleanup(func() { claudeCodeOAuthLookup = prev })

	resolved := resolveCCSwitchRecord(ccSwitchRecord{
		id: "claude-official", appType: "claude", name: "官方", current: true,
		settings: json.RawMessage(`{
			"env":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:8787"},
			"model":"claude-opus-5"
		}`),
	})
	if !resolved.profile.Importable {
		t.Fatalf("OAuth-backed official profile must be importable: %#v", resolved.profile)
	}
	if resolved.profile.CredentialKind != CCSwitchCredentialBearer {
		t.Fatalf("credential kind = %q", resolved.profile.CredentialKind)
	}
	if resolved.credential != "sk-ant-oat01-imported" {
		t.Fatalf("credential not taken from Claude Code OAuth")
	}
	if resolved.profile.BaseURL != "https://api.anthropic.com/v1" {
		t.Fatalf("loopback base must rewrite to Anthropic API, got %q", resolved.profile.BaseURL)
	}
	if resolved.profile.Model != "claude-opus-5" {
		t.Fatalf("model = %q", resolved.profile.Model)
	}
	// Safe projection still omits the secret.
	profiles := []CCSwitchProfile{resolved.profile}
	encoded, _ := json.Marshal(profiles)
	if strings.Contains(string(encoded), "sk-ant-oat01-imported") {
		t.Fatalf("safe profile leaked OAuth token: %s", encoded)
	}
}

func TestResolveCCSwitchClaudeOAuthOnlyWithoutLocalLoginStaysNonImportable(t *testing.T) {
	prev := claudeCodeOAuthLookup
	claudeCodeOAuthLookup = func() (string, bool) { return "", false }
	t.Cleanup(func() { claudeCodeOAuthLookup = prev })

	resolved := resolveCCSwitchRecord(ccSwitchRecord{
		id: "claude-oauth-only", appType: "claude", name: "Official",
		settings: json.RawMessage(`{"env":{"ANTHROPIC_BASE_URL":"https://api.anthropic.com/v1"}}`),
	})
	if resolved.profile.Importable || resolved.profile.CredentialKind != CCSwitchCredentialCLIOAuth {
		t.Fatalf("without local OAuth must stay non-importable: %#v", resolved.profile)
	}
	if !strings.Contains(resolved.profile.Reason, "OAuth") {
		t.Fatalf("reason = %q", resolved.profile.Reason)
	}
}

func TestIsLoopbackBaseURL(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8787", "https://localhost/v1", "http://127.0.0.1/v1/"} {
		if !isLoopbackBaseURL(raw) {
			t.Fatalf("expected loopback: %q", raw)
		}
	}
	if isLoopbackBaseURL("https://api.anthropic.com/v1") {
		t.Fatal("anthropic API should not be loopback")
	}
}
