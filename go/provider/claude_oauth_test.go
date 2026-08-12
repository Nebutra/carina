package provider

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseClaudeCodeOAuthSessionAcceptsRefreshableExpiredCredential(t *testing.T) {
	raw := []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-expired","refreshToken":"refresh-owned-by-claude","expiresAt":1}}`)
	if !parseClaudeCodeOAuthSession(raw) {
		t.Fatal("refreshable Claude Code session was not detected")
	}
	if parseClaudeCodeOAuthSession([]byte(`{"accessToken":"sk-ant-api03-not-oauth"}`)) {
		t.Fatal("API key was accepted as a Claude Code OAuth session")
	}
}

func TestLookupClaudeCodeOAuthSessionFromFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Force UserHomeDir via HOME on unix.
	credDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":  "sk-ant-oat01-from-file",
			"refreshToken": "refresh-owned-by-claude",
		},
	})
	if err := os.WriteFile(filepath.Join(credDir, ".credentials.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if !lookupClaudeCodeOAuthSession() {
		t.Fatal("file-backed Claude Code OAuth session was not detected")
	}
}

func TestLookupClaudeCodeKeychainSessionChecksPresenceWithoutReadingSecret(t *testing.T) {
	dir := t.TempDir()
	argsPath := filepath.Join(dir, "args")
	t.Setenv("CLAUDE_KEYCHAIN_ARGS", argsPath)
	security := filepath.Join(dir, "security")
	if err := os.WriteFile(security, []byte(`#!/bin/sh
printf '%s' "$*" > "$CLAUDE_KEYCHAIN_ARGS"
exit 0
`), 0o700); err != nil {
		t.Fatal(err)
	}
	if !lookupClaudeCodeKeychainSession(context.Background(), security) {
		t.Fatal("existing keychain item was not detected")
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(args); got != "find-generic-password -s Claude Code-credentials" || strings.Contains(got, "-w") {
		t.Fatalf("security args = %q", got)
	}
}

func TestLookupClaudeCodeKeychainSessionHonorsDeadline(t *testing.T) {
	dir := t.TempDir()
	security := filepath.Join(dir, "security")
	if err := os.WriteFile(security, []byte("#!/bin/sh\nexec sleep 5\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if lookupClaudeCodeKeychainSession(ctx, security) {
		t.Fatal("timed-out keychain lookup reported a session")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("keychain probe ignored deadline: %s", elapsed)
	}
}

func TestResolveCCSwitchClaudeDelegatesOAuthWithoutExportingToken(t *testing.T) {
	previous := claudeCodeOAuthSessionLookup
	claudeCodeOAuthSessionLookup = func() bool { return true }
	t.Cleanup(func() { claudeCodeOAuthSessionLookup = previous })

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
	if resolved.profile.CredentialKind != CCSwitchCredentialCLIOAuth {
		t.Fatalf("credential kind = %q", resolved.profile.CredentialKind)
	}
	if resolved.profile.CredentialOwner != CCSwitchCredentialOwnerClaudeCode {
		t.Fatalf("credential owner = %q", resolved.profile.CredentialOwner)
	}
	if resolved.credential != "" {
		t.Fatal("Claude Code OAuth token was exported into the CC Switch credential")
	}
	if resolved.profile.Model != "claude-opus-5" {
		t.Fatalf("model = %q", resolved.profile.Model)
	}
	resolved.profile.RuntimeID = "ccswitch-claude-test"
	resolved.profile.Route = CCSwitchRouteSavedProfile
	resolved.profile.Action = CCSwitchActionUseActiveRoute
	merged := MergeCCSwitchProviders(Catalog{}, []CCSwitchProfile{resolved.profile})
	if owner := merged[resolved.profile.RuntimeID].Source.CredentialOwner; owner != CCSwitchCredentialOwnerClaudeCode {
		t.Fatalf("catalog credential owner = %q", owner)
	}
}

func TestResolveCCSwitchClaudeOAuthOnlyWithoutLocalLoginStaysNonImportable(t *testing.T) {
	prevSession := claudeCodeOAuthSessionLookup
	claudeCodeOAuthSessionLookup = func() bool { return false }
	t.Cleanup(func() {
		claudeCodeOAuthSessionLookup = prevSession
	})

	resolved := resolveCCSwitchRecord(ccSwitchRecord{
		id: "claude-oauth-only", appType: "claude", name: "Official",
		settings: json.RawMessage(`{"env":{"ANTHROPIC_BASE_URL":"https://api.anthropic.com/v1"}}`),
	})
	if resolved.profile.Importable || resolved.profile.CredentialKind != CCSwitchCredentialCLIOAuth {
		t.Fatalf("without local OAuth must stay non-importable: %#v", resolved.profile)
	}
	if resolved.profile.CredentialOwner != CCSwitchCredentialOwnerClaudeCode {
		t.Fatalf("credential owner = %q", resolved.profile.CredentialOwner)
	}
	if !strings.Contains(resolved.profile.Reason, "OAuth") {
		t.Fatalf("reason = %q", resolved.profile.Reason)
	}
}

func TestResolveCCSwitchClaudeKeepsRefreshableSessionRunnableThroughCLI(t *testing.T) {
	prevSession := claudeCodeOAuthSessionLookup
	claudeCodeOAuthSessionLookup = func() bool { return true }
	t.Cleanup(func() {
		claudeCodeOAuthSessionLookup = prevSession
	})

	resolved := resolveCCSwitchRecord(ccSwitchRecord{
		id: "claude-refreshable", appType: "claude", name: "Official",
		settings: json.RawMessage(`{"env":{"ANTHROPIC_BASE_URL":"https://api.anthropic.com/v1"},"model":"claude-opus-5"}`),
	})
	if !resolved.profile.Importable || resolved.profile.CredentialKind != CCSwitchCredentialCLIOAuth ||
		resolved.profile.CredentialOwner != CCSwitchCredentialOwnerClaudeCode {
		t.Fatalf("refreshable profile = %#v", resolved.profile)
	}
}

func TestCurrentCCSwitchCredentialNeverExportsClaudeCodeOAuth(t *testing.T) {
	item := ccSwitchResolved{
		profile:    CCSwitchProfile{CredentialOwner: CCSwitchCredentialOwnerClaudeCode},
		credential: "sk-ant-oat01-cached",
	}
	if credential, ok := currentCCSwitchCredential(item); ok || credential != "" {
		t.Fatalf("Claude Code OAuth escaped through credential lookup: %q, ok=%v", credential, ok)
	}
}
