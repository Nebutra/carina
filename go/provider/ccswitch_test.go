package provider

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectCCSwitchProvidersProjectsSafeTypedProfiles(t *testing.T) {
	// Fixture OAuth-only Claude row must stay non-importable even when the
	// developer machine has a real Claude Code keychain login.
	prev := claudeCodeOAuthSessionLookup
	claudeCodeOAuthSessionLookup = func() bool { return false }
	t.Cleanup(func() { claudeCodeOAuthSessionLookup = prev })

	databasePath := createCCSwitchFixture(t)
	profiles, err := DetectCCSwitchProviders(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 3 {
		t.Fatalf("profiles = %#v, want 3", profiles)
	}
	if profiles[0].Name != "Relay" || !profiles[0].Current || profiles[0].Protocol != "openai-responses" {
		t.Fatalf("current codex profile = %#v", profiles[0])
	}
	if profiles[0].BaseURL != "https://relay.example/v1" || profiles[0].Model != "gpt-test" || !profiles[0].Importable {
		t.Fatalf("resolved codex profile = %#v", profiles[0])
	}
	if profiles[1].RuntimeID == "raw-claude-id" || !strings.HasPrefix(profiles[1].RuntimeID, "ccswitch-claude-") {
		t.Fatalf("raw source id leaked into runtime id: %q", profiles[1].RuntimeID)
	}
	if profiles[1].CredentialKind != CCSwitchCredentialBearer {
		t.Fatalf("Claude auth token credential kind = %q", profiles[1].CredentialKind)
	}
	if profiles[2].Importable || !strings.Contains(profiles[2].Reason, "OAuth") {
		t.Fatalf("OAuth-only profile must remain non-importable: %#v", profiles[2])
	}
	encoded, err := json.Marshal(profiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"codex-secret", "claude-secret", "raw-codex-id", "raw-claude-id"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("safe projection leaked %q: %s", secret, encoded)
		}
	}
}

func TestResolveCCSwitchClaudeAPIKeyKeepsAPIKeyHeaderSemantics(t *testing.T) {
	resolved := resolveCCSwitchRecord(ccSwitchRecord{
		id: "claude-api-key", appType: "claude", name: "Claude API key",
		settings: json.RawMessage(`{"env":{"ANTHROPIC_BASE_URL":"https://claude.example","ANTHROPIC_API_KEY":"secret"}}`),
	})
	if !resolved.profile.Importable || resolved.profile.CredentialKind != CCSwitchCredentialAPIKey {
		t.Fatalf("Claude API key profile = %#v", resolved.profile)
	}
}

func TestImportCCSwitchCredentialRequiresExplicitProfileLookup(t *testing.T) {
	prev := claudeCodeOAuthSessionLookup
	claudeCodeOAuthSessionLookup = func() bool { return false }
	t.Cleanup(func() { claudeCodeOAuthSessionLookup = prev })

	databasePath := createCCSwitchFixture(t)
	profiles, err := DetectCCSwitchProviders(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	profile, credential, err := ImportCCSwitchCredential(databasePath, profiles[0].RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "Relay" || credential != "codex-secret" {
		t.Fatalf("import = %#v credential length %d", profile, len(credential))
	}
	if _, _, err := ImportCCSwitchCredential(databasePath, profiles[2].RuntimeID); err == nil {
		t.Fatal("OAuth-only profile unexpectedly imported")
	}
}

func TestDetectCCSwitchProvidersProjectsActiveCodexManagedProxyFirst(t *testing.T) {
	databasePath := createCCSwitchFixture(t)
	enableCCSwitchCodexProxy(t, databasePath, 15721)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	config := `model_provider = "custom"
model = "gpt-live"
[model_providers.custom]
base_url = "http://127.0.0.1:15721/v1"
wire_api = "responses"
experimental_bearer_token = "proxy-secret"
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := DetectCCSwitchProviders(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 4 {
		t.Fatalf("profiles = %#v, want managed route plus 3 saved profiles", profiles)
	}
	managed := profiles[0]
	if managed.Route != CCSwitchRouteManagedProxy || managed.Action != CCSwitchActionUseActiveRoute || !managed.Importable {
		t.Fatalf("managed route = %#v", managed)
	}
	if managed.Name != "Relay · Proxy" || managed.BaseURL != "http://127.0.0.1:15721/v1" || managed.Model != "gpt-live" {
		t.Fatalf("managed route projection = %#v", managed)
	}
	if managed.CredentialKind != CCSwitchCredentialBearer || managed.Rank != 0 || managed.Revision == "" {
		t.Fatalf("managed route auth/rank = %#v", managed)
	}
	profile, credential, err := ImportCCSwitchCredential(databasePath, managed.RuntimeID)
	if err != nil {
		t.Fatal(err)
	}
	if profile.RuntimeID != managed.RuntimeID || credential != "proxy-secret" {
		t.Fatalf("managed import = %#v credential length %d", profile, len(credential))
	}
}

func TestDetectCCSwitchManagedProxyFailsClosedWhenLiveConfigDisagrees(t *testing.T) {
	databasePath := createCCSwitchFixture(t)
	enableCCSwitchCodexProxy(t, databasePath, 15721)
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	config := `model_provider = "custom"
model = "gpt-live"
[model_providers.custom]
base_url = "http://127.0.0.1:19999/v1"
wire_api = "responses"
experimental_bearer_token = "proxy-secret"
`
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	profiles, err := DetectCCSwitchProviders(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	managed := profiles[0]
	if managed.Route != CCSwitchRouteManagedProxy || managed.Importable || !strings.Contains(managed.Reason, "does not match") {
		t.Fatalf("mismatched managed route = %#v", managed)
	}
}

func TestMergeCCSwitchProvidersKeepsSourceMetadataAndRuntimeProtocol(t *testing.T) {
	base := Seed()
	profiles, err := DetectCCSwitchProviders(createCCSwitchFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	merged := MergeCCSwitchProviders(base, profiles)
	info := merged[profiles[0].RuntimeID]
	if info.Source == nil || info.Source.Kind != CCSwitchSourceKind || !info.Source.Current {
		t.Fatalf("source metadata = %#v", info.Source)
	}
	if info.APIProtocol != "openai-responses" || info.API != "https://relay.example/v1" {
		t.Fatalf("runtime projection = %#v", info)
	}
	if _, ok := info.Models["gpt-test"]; !ok {
		t.Fatalf("explicit profile model missing: %#v", info.Models)
	}
	if _, ok := base[profiles[0].RuntimeID]; ok {
		t.Fatal("base catalog was mutated")
	}
	if models := merged[profiles[2].RuntimeID].Models; len(models) != 0 {
		t.Fatalf("OAuth-only source inherited misleading models: %#v", models)
	}
}

func TestDetectCCSwitchProvidersTreatsMissingInstallAsEmpty(t *testing.T) {
	profiles, err := DetectCCSwitchProviders(filepath.Join(t.TempDir(), "missing.db"))
	if err != nil || len(profiles) != 0 {
		t.Fatalf("profiles = %#v, err = %v", profiles, err)
	}
}

func TestResolveCCSwitchRecordKeepsMalformedSettingsNonImportable(t *testing.T) {
	resolved := resolveCCSwitchRecord(ccSwitchRecord{
		id: "raw-id", appType: "codex", name: "Broken", settings: json.RawMessage(`{"auth":`), current: true,
	})
	if resolved.profile.Importable || resolved.credential != "" || !strings.Contains(resolved.profile.Reason, "invalid") {
		t.Fatalf("malformed profile = %#v", resolved)
	}
}

func createCCSwitchFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cc-switch.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE providers (
		id TEXT NOT NULL,
		app_type TEXT NOT NULL,
		name TEXT NOT NULL,
		settings_config TEXT NOT NULL,
		is_current BOOLEAN NOT NULL DEFAULT 0,
		sort_index INTEGER,
		PRIMARY KEY (id, app_type)
	)`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id, appType, name, settings string
		current                     bool
		sort                        int
	}{
		{
			id: "raw-codex-id", appType: "codex", name: "Relay", current: true, sort: 0,
			settings: `{"auth":{"OPENAI_API_KEY":"codex-secret"},"config":"model = \"gpt-test\"\nmodel_provider = \"custom\"\n[model_providers.custom]\nbase_url = \"https://relay.example/v1/\"\nwire_api = \"responses\"\n"}`,
		},
		{
			id: "raw-claude-id", appType: "claude", name: "Claude Relay", sort: 1,
			settings: `{"env":{"ANTHROPIC_BASE_URL":"https://claude.example/v1/","ANTHROPIC_AUTH_TOKEN":"claude-secret","ANTHROPIC_MODEL":"claude-test"}}`,
		},
		{
			id: "official", appType: "gemini", name: "Google Official", sort: 2,
			settings: `{"env":{},"config":{}}`,
		},
	}
	for _, row := range rows {
		if _, err := db.Exec(`INSERT INTO providers (id, app_type, name, settings_config, is_current, sort_index) VALUES (?, ?, ?, ?, ?, ?)`, row.id, row.appType, row.name, row.settings, row.current, row.sort); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func enableCCSwitchCodexProxy(t *testing.T, databasePath string, port int) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE proxy_config (
		app_type TEXT PRIMARY KEY,
		proxy_enabled INTEGER NOT NULL,
		listen_address TEXT NOT NULL,
		listen_port INTEGER NOT NULL,
		enabled INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO proxy_config (app_type, proxy_enabled, listen_address, listen_port, enabled) VALUES ('codex', 1, '127.0.0.1', ?, 1)`, port); err != nil {
		t.Fatal(err)
	}
}
