// Package contextengine defines Carina's context-compression boundary.
//
// Carina currently ships no external compression engine. Auto mode therefore
// resolves deterministically to the local no-op implementation.
package contextengine

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const (
	ModeAuto = "auto"
	ModeOff  = "off"
	ModeNoop = "noop"

	PhaseReady = "ready"

	// NoopIdentityReason is the operator-facing sentence for identity
	// compress. Transcript.compact is the product compressor.
	NoopIdentityReason = "no bytes were transformed; Transcript.compact is the product compressor"
)

type Config struct {
	ContextEngine string
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
	Transformed      bool     `json:"transformed"`
	Reason           string   `json:"reason,omitempty"`
}

type Stats struct {
	Engine           string `json:"engine"`
	Phase            string `json:"phase"`
	CompressionCalls int64  `json:"compression_calls"`
}

type Status struct {
	ConfiguredEngine string `json:"configured_engine"`
	EffectiveEngine  string `json:"effective_engine"`
	Phase            string `json:"phase"`
	Reason           string `json:"reason,omitempty"`
}

type Engine interface {
	Compress(context.Context, CompressRequest) (CompressResponse, error)
	Stats(context.Context) (Stats, error)
	Status() Status
	Doctor() map[string]any
	Close() error
}

type Manager struct {
	mu               sync.Mutex
	status           Status
	compressionCalls int64
}

func DefaultConfig(_ string) Config {
	return Config{ContextEngine: ModeAuto}
}

func New(cfg Config) (*Manager, error) {
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Manager{status: buildStatus(normalized)}, nil
}

func NormalizeConfig(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.ContextEngine) == "" {
		cfg.ContextEngine = ModeAuto
	}
	cfg.ContextEngine = strings.ToLower(strings.TrimSpace(cfg.ContextEngine))
	switch cfg.ContextEngine {
	case ModeAuto, ModeOff, ModeNoop:
		return cfg, nil
	default:
		return cfg, fmt.Errorf("context_engine must be one of auto, off, noop")
	}
}

func buildStatus(cfg Config) Status {
	st := Status{
		ConfiguredEngine: cfg.ContextEngine,
		EffectiveEngine:  ModeNoop,
		Phase:            PhaseReady,
	}
	switch cfg.ContextEngine {
	case ModeOff:
		st.Reason = "context engine disabled; " + NoopIdentityReason
	case ModeNoop:
		st.Reason = "local no-op context engine selected; " + NoopIdentityReason
	default:
		st.Reason = "no external context engine is bundled; auto uses the local no-op engine; " + NoopIdentityReason
	}
	return st
}

func (m *Manager) Compress(_ context.Context, req CompressRequest) (CompressResponse, error) {
	m.mu.Lock()
	m.compressionCalls++
	engine := m.status.EffectiveEngine
	m.mu.Unlock()
	return noopCompressResponse(req.Content, engine), nil
}

func (m *Manager) Stats(_ context.Context) (Stats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Stats{
		Engine:           m.status.EffectiveEngine,
		Phase:            m.status.Phase,
		CompressionCalls: m.compressionCalls,
	}, nil
}

func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

func (m *Manager) Doctor() map[string]any {
	st := m.Status()
	return map[string]any{
		"ok":          true,
		"engine":      st.EffectiveEngine,
		"transformed": false,
		"reason":      st.Reason,
		"status":      st,
	}
}

func (m *Manager) Close() error { return nil }

func noopCompressResponse(content, engine string) CompressResponse {
	return CompressResponse{
		Content:         content,
		OriginalBytes:   len(content),
		CompressedBytes: len(content),
		Ratio:           1,
		Engine:          engine,
		Transformed:     false,
		Reason:          NoopIdentityReason,
	}
}
