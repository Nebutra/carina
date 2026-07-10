// Package contextengine defines Carina's native context-compression boundary.
//
// The first implementation is deliberately conservative: it validates and
// reports the configured Headroom integration, while model-facing compression
// remains a no-op until the sidecar protocol is wired into the agent loop.
package contextengine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

const (
	ModeAuto     = "auto"
	ModeOff      = "off"
	ModeHeadroom = "headroom"
	ModeNoop     = "noop"

	HeadroomModeManagedMCP = "managed_mcp"
	HeadroomModeSidecar    = "sidecar"
	HeadroomModeProxy      = "proxy"

	PhaseDiscovery  = "discovery"
	PhaseManagedMCP = "managed_mcp"
	PhaseFailed     = "failed"

	ManagedMCPServerName = "carina_headroom"
)

type Config struct {
	ContextEngine       string
	HeadroomBin         string
	HeadroomStateDir    string
	HeadroomMode        string
	HeadroomProxyPort   int
	HeadroomTokenBudget int
	CarinaStateDir      string
}

type CompressRequest struct {
	SessionID string
	TaskID    string
	Turn      int
	Kind      string
	Tool      string
	Content   string
	Pinned    bool
}

type CompressResponse struct {
	Content          string   `json:"content"`
	OriginalRef      string   `json:"original_ref,omitempty"`
	OriginalSHA256   string   `json:"original_sha256,omitempty"`
	OriginalBytes    int      `json:"original_bytes"`
	CompressedBytes  int      `json:"compressed_bytes"`
	Ratio            float64  `json:"ratio"`
	Engine           string   `json:"engine"`
	OriginalTokens   int      `json:"original_tokens,omitempty"`
	CompressedTokens int      `json:"compressed_tokens,omitempty"`
	SavingsPercent   float64  `json:"savings_percent,omitempty"`
	Transforms       []string `json:"transforms,omitempty"`
}

type RetrieveResponse struct {
	Ref           string `json:"ref"`
	Content       string `json:"content,omitempty"`
	OriginalBytes int    `json:"original_bytes"`
	SHA256        string `json:"sha256,omitempty"`
	Engine        string `json:"engine,omitempty"`
	Source        string `json:"source,omitempty"`
	Results       any    `json:"results,omitempty"`
}

type Stats struct {
	Engine           string `json:"engine"`
	Phase            string `json:"phase"`
	CompressionCalls int64  `json:"compression_calls"`
	RetrievalCalls   int64  `json:"retrieval_calls"`
	FallbackCalls    int64  `json:"fallback_calls,omitempty"`
	Headroom         any    `json:"headroom,omitempty"`
	HeadroomError    string `json:"headroom_error,omitempty"`
}

type Status struct {
	ConfiguredEngine    string `json:"configured_engine"`
	EffectiveEngine     string `json:"effective_engine"`
	Phase               string `json:"phase"`
	HeadroomMode        string `json:"headroom_mode,omitempty"`
	HeadroomBin         string `json:"headroom_bin,omitempty"`
	HeadroomSource      string `json:"headroom_source,omitempty"`
	HeadroomAvailable   bool   `json:"headroom_available"`
	HeadroomStateDir    string `json:"headroom_state_dir,omitempty"`
	HeadroomProxyPort   int    `json:"headroom_proxy_port,omitempty"`
	HeadroomTokenBudget int    `json:"headroom_token_budget,omitempty"`
	ManagedMCPConnected bool   `json:"managed_mcp_connected,omitempty"`
	ManagedMCPServer    string `json:"managed_mcp_server,omitempty"`
	AdapterReady        bool   `json:"adapter_ready,omitempty"`
	CompressAvailable   bool   `json:"compress_available,omitempty"`
	RetrieveAvailable   bool   `json:"retrieve_available,omitempty"`
	StatsAvailable      bool   `json:"stats_available,omitempty"`
	Degraded            bool   `json:"degraded,omitempty"`
	LastError           string `json:"last_error,omitempty"`
	Reason              string `json:"reason,omitempty"`
}

type MCPServer struct {
	Command string
	Args    []string
	Env     map[string]string
}

type Engine interface {
	Compress(context.Context, CompressRequest) (CompressResponse, error)
	Retrieve(context.Context, string) (RetrieveResponse, error)
	Stats(context.Context) (Stats, error)
	Status() Status
	Doctor() map[string]any
	Close() error
}

// ManagedMCPAdapter is the narrow private bridge from the context engine to
// Carina's MCP manager. ToolSchemas must reflect the actual tools/list response;
// the engine refuses to guess names or argument shapes.
type ManagedMCPAdapter interface {
	ToolSchemas(server string) (map[string]json.RawMessage, error)
	CallContext(context.Context, string, string, map[string]any) (string, error)
}

type Manager struct {
	mu               sync.Mutex
	cfg              Config
	status           Status
	compressionCalls int64
	retrievalCalls   int64
	fallbackCalls    int64
	adapter          ManagedMCPAdapter
}

func DefaultConfig(stateDir string) Config {
	return Config{
		ContextEngine:       ModeAuto,
		HeadroomMode:        HeadroomModeManagedMCP,
		HeadroomStateDir:    filepath.Join(stateDir, "headroom"),
		HeadroomTokenBudget: 4000,
		CarinaStateDir:      stateDir,
	}
}

func New(cfg Config) (*Manager, error) {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	status := buildStatus(normalized)
	if normalized.ContextEngine == ModeHeadroom && !status.HeadroomAvailable {
		return nil, fmt.Errorf("contextengine: headroom requested but unavailable: %s", status.Reason)
	}
	if status.EffectiveEngine == ModeHeadroom {
		if err := os.MkdirAll(normalized.HeadroomStateDir, 0o700); err != nil {
			return nil, fmt.Errorf("contextengine: prepare headroom state dir: %w", err)
		}
	}
	return &Manager{cfg: normalized, status: status}, nil
}

func NormalizeConfig(cfg Config) (Config, error) {
	def := DefaultConfig(cfg.CarinaStateDir)
	if strings.TrimSpace(def.CarinaStateDir) == "" {
		def.CarinaStateDir = ".carina-state"
		def.HeadroomStateDir = filepath.Join(def.CarinaStateDir, "headroom")
	}
	if strings.TrimSpace(cfg.ContextEngine) == "" {
		cfg.ContextEngine = def.ContextEngine
	}
	cfg.ContextEngine = strings.ToLower(strings.TrimSpace(cfg.ContextEngine))
	switch cfg.ContextEngine {
	case ModeAuto, ModeOff, ModeHeadroom, ModeNoop:
	default:
		return cfg, fmt.Errorf("context_engine must be one of auto, off, headroom, noop")
	}
	if strings.TrimSpace(cfg.HeadroomMode) == "" {
		cfg.HeadroomMode = def.HeadroomMode
	}
	cfg.HeadroomMode = strings.ToLower(strings.TrimSpace(cfg.HeadroomMode))
	switch cfg.HeadroomMode {
	case HeadroomModeManagedMCP, HeadroomModeSidecar, HeadroomModeProxy:
	default:
		return cfg, fmt.Errorf("headroom_mode must be one of managed_mcp, sidecar, proxy")
	}
	if strings.TrimSpace(cfg.HeadroomStateDir) == "" {
		cfg.HeadroomStateDir = def.HeadroomStateDir
	}
	if cfg.HeadroomProxyPort < 0 {
		return cfg, fmt.Errorf("headroom_proxy_port must be >= 0")
	}
	if cfg.HeadroomTokenBudget < 0 {
		return cfg, fmt.Errorf("headroom_token_budget must be >= 0")
	}
	if cfg.HeadroomTokenBudget == 0 {
		cfg.HeadroomTokenBudget = def.HeadroomTokenBudget
	}
	return cfg, nil
}

func buildStatus(cfg Config) Status {
	st := Status{
		ConfiguredEngine:    cfg.ContextEngine,
		EffectiveEngine:     ModeNoop,
		Phase:               PhaseDiscovery,
		HeadroomMode:        cfg.HeadroomMode,
		HeadroomStateDir:    cfg.HeadroomStateDir,
		HeadroomProxyPort:   cfg.HeadroomProxyPort,
		HeadroomTokenBudget: cfg.HeadroomTokenBudget,
	}
	switch cfg.ContextEngine {
	case ModeOff:
		st.Reason = "context engine disabled"
		return st
	case ModeNoop:
		st.Reason = "noop context engine selected"
		return st
	}
	bin, source, err := resolveHeadroomBinWithSource(cfg.HeadroomBin)
	if err != nil {
		st.Reason = err.Error()
		return st
	}
	st.HeadroomBin = bin
	st.HeadroomSource = source
	st.HeadroomAvailable = true
	if cfg.ContextEngine == ModeHeadroom || cfg.ContextEngine == ModeAuto && source != "path" {
		st.EffectiveEngine = ModeHeadroom
		if cfg.ContextEngine == ModeAuto {
			st.Reason = "bundled/configured headroom available; managed integration enabled"
		} else {
			st.Reason = "headroom available; managed integration enabled"
		}
		return st
	}
	if cfg.ContextEngine == ModeAuto {
		st.Reason = "headroom found on PATH only; auto mode requires bundled or configured headroom"
		return st
	}
	st.Reason = "headroom available; noop context engine selected"
	return st
}

func ResolveHeadroomBin(configured string) (string, error) {
	p, _, err := resolveHeadroomBinWithSource(configured)
	return p, err
}

func resolveHeadroomBinWithSource(configured string) (string, string, error) {
	if strings.TrimSpace(configured) != "" {
		p, err := resolveExecutable(configured)
		if err != nil {
			return "", "", err
		}
		return p, "configured", nil
	}
	candidates := bundledCandidates()
	for _, c := range candidates {
		if p, err := resolveExecutable(c); err == nil {
			return p, "bundled", nil
		}
	}
	if p, err := exec.LookPath("headroom"); err == nil {
		return p, "path", nil
	}
	return "", "", fmt.Errorf("headroom binary not found")
}

func resolveExecutable(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("empty headroom binary path")
	}
	if !strings.ContainsRune(path, os.PathSeparator) {
		if p, err := exec.LookPath(path); err == nil {
			return p, nil
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory", abs)
	}
	if info.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not executable", abs)
	}
	return abs, nil
}

func bundledCandidates() []string {
	var out []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		out = append(out,
			filepath.Join(dir, "headroom"),
			filepath.Join(dir, "vendor", "headroom", "headroom"),
			filepath.Join(dir, "..", "vendor", "headroom", "headroom"),
		)
	}
	return out
}

func (m *Manager) Compress(ctx context.Context, req CompressRequest) (CompressResponse, error) {
	m.mu.Lock()
	m.compressionCalls++
	status := m.status
	adapter := m.adapter
	m.mu.Unlock()
	if req.Pinned || status.EffectiveEngine != ModeHeadroom {
		return noopCompressResponse(req.Content, status.EffectiveEngine), nil
	}
	if !status.AdapterReady || !status.CompressAvailable || adapter == nil {
		return m.compressionFailure(req.Content, fmt.Errorf("managed Headroom compression adapter is unavailable"))
	}
	raw, err := adapter.CallContext(ctx, ManagedMCPServerName, "headroom_compress", map[string]any{"content": req.Content})
	if err != nil {
		return m.compressionFailure(req.Content, err)
	}
	res, err := parseHeadroomCompress(req.Content, raw)
	if err != nil {
		return m.compressionFailure(req.Content, err)
	}
	return res, nil
}

func (m *Manager) Retrieve(ctx context.Context, ref string) (RetrieveResponse, error) {
	m.mu.Lock()
	m.retrievalCalls++
	status := m.status
	adapter := m.adapter
	m.mu.Unlock()
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return RetrieveResponse{}, fmt.Errorf("contextengine: retrieve ref is required")
	}
	if status.EffectiveEngine != ModeHeadroom || !status.AdapterReady || !status.RetrieveAvailable || adapter == nil {
		return RetrieveResponse{}, fmt.Errorf("contextengine: retrieve %q unavailable: managed Headroom did not advertise headroom_retrieve", ref)
	}
	raw, err := adapter.CallContext(ctx, ManagedMCPServerName, "headroom_retrieve", map[string]any{"hash": ref})
	if err != nil {
		return RetrieveResponse{}, fmt.Errorf("contextengine: headroom retrieve: %w", err)
	}
	return parseHeadroomRetrieve(ref, raw)
}

func (m *Manager) Stats(ctx context.Context) (Stats, error) {
	m.mu.Lock()
	stats := Stats{
		Engine:           m.status.EffectiveEngine,
		Phase:            m.status.Phase,
		CompressionCalls: m.compressionCalls,
		RetrievalCalls:   m.retrievalCalls,
		FallbackCalls:    m.fallbackCalls,
	}
	adapter := m.adapter
	available := m.status.EffectiveEngine == ModeHeadroom && m.status.AdapterReady && m.status.StatsAvailable
	m.mu.Unlock()
	if !available || adapter == nil {
		return stats, nil
	}
	raw, err := adapter.CallContext(ctx, ManagedMCPServerName, "headroom_stats", nil)
	if err != nil {
		stats.HeadroomError = err.Error()
		return stats, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		stats.HeadroomError = "invalid headroom_stats JSON: " + err.Error()
		return stats, nil
	}
	stats.Headroom = decoded
	return stats, nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) Doctor() map[string]any {
	st := m.Status()
	ok := st.ConfiguredEngine != ModeHeadroom || (st.HeadroomAvailable && st.ManagedMCPConnected && st.AdapterReady && st.CompressAvailable && st.Phase != PhaseFailed && st.LastError == "")
	return map[string]any{
		"ok":       ok,
		"degraded": st.Degraded,
		"status":   st,
	}
}

func (m *Manager) Close() error { return nil }

func (m *Manager) ManagedMCPServer() (string, MCPServer, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status.EffectiveEngine != ModeHeadroom || m.cfg.HeadroomMode != HeadroomModeManagedMCP || m.status.HeadroomBin == "" {
		return "", MCPServer{}, false
	}
	args := []string{"mcp", "serve"}
	if m.cfg.HeadroomProxyPort > 0 {
		proxyURL := url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", m.cfg.HeadroomProxyPort)}
		args = append(args, "--proxy-url", proxyURL.String())
	}
	return ManagedMCPServerName, MCPServer{
		Command: m.status.HeadroomBin,
		Args:    args,
		Env:     m.headroomEnvLocked(),
	}, true
}

func (m *Manager) MarkManagedMCPConnected(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.ManagedMCPServer = "headroom"
	if err != nil {
		m.status.Phase = PhaseFailed
		m.status.ManagedMCPConnected = false
		m.status.AdapterReady = false
		m.status.LastError = err.Error()
		if m.cfg.ContextEngine == ModeAuto {
			m.fallbackCalls++
			m.status.EffectiveEngine = ModeNoop
			m.status.Degraded = true
			m.status.Reason = "managed Headroom MCP failed; auto mode degraded to noop: " + err.Error()
		} else {
			m.status.Reason = "managed Headroom MCP failed: " + err.Error()
		}
		return
	}
	m.status.Phase = PhaseManagedMCP
	m.status.ManagedMCPConnected = true
	m.status.LastError = ""
	m.status.Degraded = false
	if m.cfg.ContextEngine == ModeAuto && m.status.HeadroomAvailable {
		m.status.EffectiveEngine = ModeHeadroom
	}
	if m.status.AdapterReady && m.status.CompressAvailable {
		m.status.Reason = "managed Headroom MCP connected; compression adapter active"
	} else {
		m.status.Reason = "managed Headroom MCP connected; compression tool schema not attached"
	}
}

func (m *Manager) headroomEnvLocked() map[string]string {
	env := map[string]string{
		"HEADROOM_WORKSPACE_DIR":    m.cfg.HeadroomStateDir,
		"HEADROOM_CCR_BACKEND":      "sqlite",
		"HEADROOM_CCR_SQLITE_PATH":  filepath.Join(m.cfg.HeadroomStateDir, "ccr_store.db"),
		"HEADROOM_MCP_READ":         "0",
		"CARINA_CONTEXT_ENGINE":     m.cfg.ContextEngine,
		"CARINA_HEADROOM_MODE":      m.cfg.HeadroomMode,
		"CARINA_HEADROOM_STATE_DIR": m.cfg.HeadroomStateDir,
	}
	if m.cfg.HeadroomProxyPort > 0 {
		proxyURL := url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", m.cfg.HeadroomProxyPort)}
		env["HEADROOM_PROXY_URL"] = proxyURL.String()
	}
	return env
}
