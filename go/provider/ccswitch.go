package provider

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	_ "modernc.org/sqlite"
)

const (
	CCSwitchSourceKind         = "cc-switch"
	CCSwitchSourceLabel        = "CC Switch"
	CCSwitchCredentialAPIKey   = "api_key"
	CCSwitchCredentialBearer   = "bearer_token"
	CCSwitchCredentialCLIOAuth = "cli_oauth"

	CCSwitchCredentialOwnerProfile    = "cc-switch"
	CCSwitchCredentialOwnerClaudeCode = "claude-code"

	CCSwitchRouteManagedProxy = "managed_proxy"
	CCSwitchRouteSavedProfile = "saved_profile"

	CCSwitchActionUseActiveRoute     = "use_active_route"
	CCSwitchActionImportSaved        = "import_saved_profile"
	CCSwitchActionExplainUnavailable = "explain_unavailable"
)

// CCSwitchProfile is the non-sensitive projection of one compatible CC Switch
// provider row. Raw source IDs and credentials are intentionally excluded.
type CCSwitchProfile struct {
	RuntimeID       string
	Name            string
	AppType         string
	BaseURL         string
	Protocol        string
	CredentialKind  string
	CredentialOwner string
	Route           string
	Action          string
	Revision        string
	Rank            int
	Model           string
	Current         bool
	Importable      bool
	Reason          string
}

type ccSwitchRecord struct {
	id       string
	appType  string
	name     string
	settings json.RawMessage
	current  bool
}

type ccSwitchSettings struct {
	Env    map[string]json.RawMessage `json:"env"`
	Auth   map[string]json.RawMessage `json:"auth"`
	Config json.RawMessage            `json:"config"`
	Model  string                     `json:"model"`
}

type ccSwitchResolved struct {
	profile    CCSwitchProfile
	credential string
}

type ccSwitchProxyConfig struct {
	appType string
	address string
	port    int
}

// DefaultCCSwitchDatabasePath returns CC Switch's cross-platform user database
// location. CC Switch uses the same ~/.cc-switch path on supported platforms.
func DefaultCCSwitchDatabasePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cc-switch", "cc-switch.db"), nil
}

// DetectCCSwitchProviders reads CC Switch in query-only mode. Missing installs
// are not errors; malformed or incompatible records are represented safely so
// the product can explain why they cannot be imported.
func DetectCCSwitchProviders(databasePath string) ([]CCSwitchProfile, error) {
	resolved, err := readCCSwitch(databasePath)
	if err != nil {
		return nil, err
	}
	out := make([]CCSwitchProfile, 0, len(resolved))
	for _, item := range resolved {
		out = append(out, item.profile)
	}
	return out, nil
}

// ImportCCSwitchCredential resolves a profile only after explicit user action.
// The returned credential must remain transient and must never be logged.
func ImportCCSwitchCredential(databasePath, runtimeID string) (CCSwitchProfile, string, error) {
	resolved, err := loadCCSwitchResolved(databasePath)
	if err != nil {
		return CCSwitchProfile{}, "", err
	}
	for _, item := range resolved {
		if item.profile.RuntimeID != runtimeID {
			continue
		}
		credential, ok := currentCCSwitchCredential(item)
		if !item.profile.Importable || !ok {
			return item.profile, "", fmt.Errorf("cc switch profile %q has no reusable API credential", item.profile.Name)
		}
		return item.profile, credential, nil
	}
	return CCSwitchProfile{}, "", fmt.Errorf("cc switch profile %q was not found", runtimeID)
}

// LookupCCSwitchCredential is a non-mutating read of a reusable CC Switch
// credential for runtime inventory and execution. Results are cached briefly so
// model.list can expand live model lists without opening SQLite per provider.
// Secrets are never logged.
func LookupCCSwitchCredential(runtimeID string) (CCSwitchProfile, string, bool) {
	runtimeID = strings.TrimSpace(runtimeID)
	if runtimeID == "" {
		return CCSwitchProfile{}, "", false
	}
	resolved, err := loadCCSwitchResolved("")
	if err != nil {
		return CCSwitchProfile{}, "", false
	}
	for _, item := range resolved {
		if item.profile.RuntimeID != runtimeID {
			continue
		}
		credential, ok := currentCCSwitchCredential(item)
		if !item.profile.Importable || !ok {
			return item.profile, "", false
		}
		return item.profile, credential, true
	}
	return CCSwitchProfile{}, "", false
}

func currentCCSwitchCredential(item ccSwitchResolved) (string, bool) {
	if item.profile.CredentialOwner == CCSwitchCredentialOwnerClaudeCode {
		return "", false
	}
	credential := strings.TrimSpace(item.credential)
	return credential, credential != ""
}

const ccSwitchResolvedCacheTTL = 30 * time.Second

var (
	ccSwitchResolvedMu    sync.Mutex
	ccSwitchResolvedAt    time.Time
	ccSwitchResolvedCache []ccSwitchResolved
	ccSwitchResolvedPath  string
	ccSwitchResolvedErr   error
)

func loadCCSwitchResolved(databasePath string) ([]ccSwitchResolved, error) {
	ccSwitchResolvedMu.Lock()
	defer ccSwitchResolvedMu.Unlock()
	path := strings.TrimSpace(databasePath)
	if path == "" {
		var err error
		path, err = DefaultCCSwitchDatabasePath()
		if err != nil {
			return nil, err
		}
	}
	if ccSwitchResolvedCache != nil &&
		ccSwitchResolvedPath == path &&
		ccSwitchResolvedErr == nil &&
		time.Since(ccSwitchResolvedAt) < ccSwitchResolvedCacheTTL {
		return ccSwitchResolvedCache, nil
	}
	resolved, err := readCCSwitch(path)
	ccSwitchResolvedPath = path
	ccSwitchResolvedAt = time.Now()
	ccSwitchResolvedErr = err
	if err != nil {
		ccSwitchResolvedCache = nil
		return nil, err
	}
	ccSwitchResolvedCache = resolved
	return resolved, nil
}

// InvalidateCCSwitchCredentialCache drops the short-lived credential snapshot.
// Tests call this after mutating fixtures.
func InvalidateCCSwitchCredentialCache() {
	ccSwitchResolvedMu.Lock()
	defer ccSwitchResolvedMu.Unlock()
	ccSwitchResolvedCache = nil
	ccSwitchResolvedAt = time.Time{}
	ccSwitchResolvedPath = ""
	ccSwitchResolvedErr = nil
}

// MergeCCSwitchProviders adds safe runtime projections without mutating the
// caller's catalog. Detection failures leave the regular catalog intact.
func MergeCCSwitchProviders(base Catalog, profiles []CCSwitchProfile) Catalog {
	merged := make(Catalog, len(base)+len(profiles))
	for id, info := range base {
		merged[id] = info
	}
	for _, profile := range profiles {
		if profile.RuntimeID == "" || profile.Protocol == "" || profile.BaseURL == "" {
			continue
		}
		models := map[string]Model{}
		if !profile.Importable {
			// OAuth-only and invalid source records are explanatory entries, not
			// executable model inventories.
		} else if profile.Model != "" {
			if model, ok := sourceModelForProtocol(base, profile.Protocol, profile.Model); ok {
				// A proxy route does not change the selected model's capabilities.
				// Preserve the canonical modalities, limits, tools, and reasoning
				// metadata so every downstream consumer sees the same contract.
				models[profile.Model] = model
			} else {
				// Unknown proxy model IDs remain conservative for capabilities that
				// require affirmative catalog evidence, including image input.
				models[profile.Model] = Model{ID: profile.Model, Name: profile.Model, Reasoning: true, ToolCall: true}
			}
		} else {
			for id, model := range sourceModelsForProtocol(base, profile.Protocol) {
				models[id] = model
			}
		}
		merged[profile.RuntimeID] = Info{
			ID:          profile.RuntimeID,
			Name:        profile.Name,
			API:         profile.BaseURL,
			APIProtocol: profile.Protocol,
			Source: &Source{
				Kind:            CCSwitchSourceKind,
				Label:           CCSwitchSourceLabel,
				App:             profile.AppType,
				Route:           profile.Route,
				AuthMode:        profile.CredentialKind,
				CredentialOwner: profile.CredentialOwner,
				Action:          profile.Action,
				Revision:        profile.Revision,
				Rank:            profile.Rank,
				Current:         profile.Current,
				Importable:      profile.Importable,
				Reason:          profile.Reason,
			},
			Models: models,
		}
	}
	return merged
}

func sourceModelsForProtocol(base Catalog, protocol string) map[string]Model {
	sourceID := map[string]string{
		"anthropic":        "anthropic",
		"gemini":           "google",
		"openai-chat":      "openai",
		"openai-responses": "openai",
	}[protocol]
	if sourceID == "" {
		return nil
	}
	return base[sourceID].Models
}

func sourceModelForProtocol(base Catalog, protocol, modelID string) (Model, bool) {
	models := sourceModelsForProtocol(base, protocol)
	if model, ok := models[modelID]; ok {
		return model, true
	}
	for _, model := range models {
		if model.ID == modelID {
			return model, true
		}
	}
	return Model{}, false
}

func readCCSwitch(databasePath string) ([]ccSwitchResolved, error) {
	if databasePath == "" {
		var err error
		databasePath, err = DefaultCCSwitchDatabasePath()
		if err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(databasePath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("cc switch database: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: databasePath, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open cc switch database: %w", err)
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT id, app_type, name, settings_config, is_current
		FROM providers
		WHERE app_type IN ('claude', 'codex', 'gemini')
		ORDER BY is_current DESC, COALESCE(sort_index, 999999), name, id`)
	if err != nil {
		return nil, fmt.Errorf("read cc switch providers: %w", err)
	}
	defer rows.Close()
	var records []ccSwitchRecord
	for rows.Next() {
		var record ccSwitchRecord
		var settings string
		if err := rows.Scan(&record.id, &record.appType, &record.name, &settings, &record.current); err != nil {
			return nil, fmt.Errorf("decode cc switch provider row: %w", err)
		}
		record.settings = json.RawMessage(settings)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cc switch providers: %w", err)
	}
	resolved := make([]ccSwitchResolved, 0, len(records)+1)
	for index, record := range records {
		item := resolveCCSwitchRecord(record)
		item.profile.Route = CCSwitchRouteSavedProfile
		item.profile.Action = CCSwitchActionExplainUnavailable
		if item.profile.Importable {
			if item.profile.CredentialOwner == CCSwitchCredentialOwnerClaudeCode {
				item.profile.Action = CCSwitchActionUseActiveRoute
			} else {
				item.profile.Action = CCSwitchActionImportSaved
			}
		}
		item.profile.Rank = 100 + index
		item.profile.Revision = ccSwitchProfileRevision(item.profile)
		resolved = append(resolved, item)
	}
	proxyConfigs, err := readCCSwitchProxyConfigs(db)
	if err != nil {
		return nil, err
	}
	if managed := resolveManagedCodexRoute(proxyConfigs, records); managed != nil {
		resolved = append([]ccSwitchResolved{*managed}, resolved...)
	}
	return resolved, nil
}

func readCCSwitchProxyConfigs(db *sql.DB) ([]ccSwitchProxyConfig, error) {
	rows, err := db.Query(`
		SELECT app_type, listen_address, listen_port
		FROM proxy_config
		WHERE proxy_enabled = 1 AND enabled = 1
		ORDER BY app_type`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("read cc switch proxy config: %w", err)
	}
	defer rows.Close()
	var configs []ccSwitchProxyConfig
	for rows.Next() {
		var config ccSwitchProxyConfig
		if err := rows.Scan(&config.appType, &config.address, &config.port); err != nil {
			return nil, fmt.Errorf("decode cc switch proxy config: %w", err)
		}
		configs = append(configs, config)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cc switch proxy config: %w", err)
	}
	return configs, nil
}

func resolveManagedCodexRoute(configs []ccSwitchProxyConfig, records []ccSwitchRecord) *ccSwitchResolved {
	var proxy *ccSwitchProxyConfig
	for index := range configs {
		if configs[index].appType == "codex" {
			proxy = &configs[index]
			break
		}
	}
	if proxy == nil {
		return nil
	}
	name := "CC Switch Proxy"
	currentFound := false
	for _, record := range records {
		if record.appType == "codex" && record.current {
			currentFound = true
			if candidate := strings.TrimSpace(record.name); candidate != "" {
				// Distinguish the managed proxy row from the saved profile that
				// shares the same CC Switch display name (e.g. both "TDS").
				name = candidate + " · Proxy"
			}
			break
		}
	}
	profile := CCSwitchProfile{
		RuntimeID:       "ccswitch-codex-managed-proxy",
		Name:            name,
		AppType:         "codex",
		Protocol:        "openai-responses",
		CredentialKind:  CCSwitchCredentialBearer,
		CredentialOwner: CCSwitchCredentialOwnerProfile,
		Route:           CCSwitchRouteManagedProxy,
		Action:          CCSwitchActionExplainUnavailable,
		Current:         true,
		Rank:            0,
	}
	if !currentFound {
		profile.Reason = "CC Switch proxy is enabled, but no current Codex profile is selected"
		profile.Revision = ccSwitchProfileRevision(profile)
		return &ccSwitchResolved{profile: profile}
	}
	configPath, err := defaultCodexConfigPath()
	if err != nil {
		profile.Reason = "The active Codex configuration could not be located"
		profile.Revision = ccSwitchProfileRevision(profile)
		return &ccSwitchResolved{profile: profile}
	}
	configText, err := os.ReadFile(configPath)
	if err != nil {
		profile.Reason = "CC Switch proxy is enabled, but the active Codex configuration is unavailable"
		profile.Revision = ccSwitchProfileRevision(profile)
		return &ccSwitchResolved{profile: profile}
	}
	credential := ""
	resolveCodexConfig(json.RawMessage(strconv.Quote(string(configText))), &profile, &credential)
	if !codexConfigMatchesProxy(profile.BaseURL, *proxy) {
		profile.Reason = "The active Codex route does not match the enabled CC Switch proxy"
		profile.Revision = ccSwitchProfileRevision(profile)
		return &ccSwitchResolved{profile: profile}
	}
	if strings.TrimSpace(credential) == "" {
		profile.Reason = "The active CC Switch Codex route has no reusable proxy access token"
		profile.CredentialKind = CCSwitchCredentialCLIOAuth
		profile.Revision = ccSwitchProfileRevision(profile)
		return &ccSwitchResolved{profile: profile}
	}
	profile.Importable = true
	profile.Action = CCSwitchActionUseActiveRoute
	profile.Revision = ccSwitchProfileRevision(profile)
	return &ccSwitchResolved{profile: profile, credential: strings.TrimSpace(credential)}
}

func defaultCodexConfigPath() (string, error) {
	if root := strings.TrimSpace(os.Getenv("CODEX_HOME")); root != "" {
		return filepath.Join(root, "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

func codexConfigMatchesProxy(baseURL string, proxy ccSwitchProxyConfig) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname()) {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port != proxy.port {
		return false
	}
	address := strings.TrimSpace(proxy.address)
	return address == "" || address == "0.0.0.0" || address == "::" || isLoopbackHost(address)
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	return strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func ccSwitchProfileRevision(profile CCSwitchProfile) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{
		profile.AppType, profile.Route, profile.BaseURL, profile.Protocol, profile.Model,
	}, "\x00")))
	return hex.EncodeToString(digest[:8])
}

func resolveCCSwitchRecord(record ccSwitchRecord) ccSwitchResolved {
	profile := CCSwitchProfile{
		RuntimeID:       ccSwitchRuntimeID(record.appType, record.id),
		Name:            strings.TrimSpace(record.name),
		AppType:         record.appType,
		CredentialOwner: CCSwitchCredentialOwnerProfile,
		Current:         record.current,
	}
	if profile.Name == "" {
		profile.Name = "CC Switch provider"
	}
	var settings ccSwitchSettings
	if err := json.Unmarshal(record.settings, &settings); err != nil {
		profile.Reason = "Provider settings are invalid"
		return ccSwitchResolved{profile: profile}
	}
	var credential string
	switch record.appType {
	case "claude":
		profile.Protocol = "anthropic"
		profile.BaseURL = firstString(settings.Env, "ANTHROPIC_BASE_URL")
		if profile.BaseURL == "" {
			profile.BaseURL = "https://api.anthropic.com/v1"
		}
		switch {
		case firstString(settings.Env, "ANTHROPIC_AUTH_TOKEN") != "":
			credential = firstString(settings.Env, "ANTHROPIC_AUTH_TOKEN")
			profile.CredentialKind = CCSwitchCredentialBearer
		case firstString(settings.Env, "ANTHROPIC_API_KEY") != "":
			credential = firstString(settings.Env, "ANTHROPIC_API_KEY")
			profile.CredentialKind = CCSwitchCredentialAPIKey
		case firstString(settings.Env, "OPENROUTER_API_KEY") != "":
			credential = firstString(settings.Env, "OPENROUTER_API_KEY")
			profile.CredentialKind = CCSwitchCredentialBearer
		}
		// Official Claude Code / Claude Max login leaves no API key in the CC
		// Switch row. Claude Code remains the credential owner and Carina delegates
		// inference to the CLI instead of exporting its access or refresh tokens.
		if strings.TrimSpace(credential) == "" {
			profile.CredentialOwner = CCSwitchCredentialOwnerClaudeCode
			if claudeCodeOAuthSessionLookup() {
				profile.CredentialKind = CCSwitchCredentialCLIOAuth
				profile.Importable = true
			}
		}
		profile.Model = firstString(settings.Env, "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL")
		if profile.Model == "" {
			profile.Model = strings.TrimSpace(settings.Model)
		}
	case "codex":
		profile.Protocol = "openai-responses"
		profile.CredentialKind = CCSwitchCredentialAPIKey
		credential = firstString(settings.Auth, "OPENAI_API_KEY")
		usedBearer := resolveCodexConfig(settings.Config, &profile, &credential)
		if usedBearer {
			profile.CredentialKind = CCSwitchCredentialBearer
		}
		if profile.BaseURL == "" {
			profile.BaseURL = "https://api.openai.com/v1"
		}
	case "gemini":
		profile.Protocol = "gemini"
		profile.CredentialKind = CCSwitchCredentialAPIKey
		profile.BaseURL = firstString(settings.Env, "GOOGLE_GEMINI_BASE_URL", "GEMINI_BASE_URL")
		if profile.BaseURL == "" {
			profile.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
		}
		credential = firstString(settings.Env, "GEMINI_API_KEY", "GOOGLE_API_KEY")
		profile.Model = firstString(settings.Env, "GEMINI_MODEL", "GOOGLE_MODEL")
	default:
		profile.Reason = "This CC Switch app type is not supported"
		return ccSwitchResolved{profile: profile}
	}
	profile.BaseURL = strings.TrimRight(strings.TrimSpace(profile.BaseURL), "/")
	profile.Model = strings.TrimSpace(profile.Model)
	credential = strings.TrimSpace(credential)
	if profile.BaseURL == "" {
		profile.Reason = "No usable endpoint was found"
	} else if credential == "" && !profile.Importable {
		profile.CredentialKind = CCSwitchCredentialCLIOAuth
		profile.Reason = "No reusable API credential; OAuth sessions stay with their owning CLI"
	} else {
		profile.Importable = true
	}
	return ccSwitchResolved{profile: profile, credential: credential}
}

func resolveCodexConfig(raw json.RawMessage, profile *CCSwitchProfile, credential *string) bool {
	var configText string
	if err := json.Unmarshal(raw, &configText); err != nil || strings.TrimSpace(configText) == "" {
		return false
	}
	var document map[string]any
	if err := toml.Unmarshal([]byte(configText), &document); err != nil {
		return false
	}
	usedBearer := false
	profile.Model = stringValue(document["model"])
	providerID := stringValue(document["model_provider"])
	if providers, ok := document["model_providers"].(map[string]any); ok {
		if selected, ok := providers[providerID].(map[string]any); ok {
			profile.BaseURL = stringValue(selected["base_url"])
			switch strings.ToLower(stringValue(selected["wire_api"])) {
			case "chat", "chat_completions", "chat-completions":
				profile.Protocol = "openai-chat"
			case "responses":
				profile.Protocol = "openai-responses"
			}
			if *credential == "" {
				*credential = stringValue(selected["experimental_bearer_token"])
				usedBearer = *credential != ""
			}
		}
	}
	if profile.BaseURL == "" {
		profile.BaseURL = stringValue(document["base_url"])
	}
	return usedBearer
}

func firstString(values map[string]json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var value string
		if raw, ok := values[key]; ok && json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func ccSwitchRuntimeID(appType, sourceID string) string {
	digest := sha256.Sum256([]byte(appType + "\x00" + sourceID))
	return "ccswitch-" + appType + "-" + hex.EncodeToString(digest[:6])
}
