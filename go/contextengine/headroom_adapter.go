package contextengine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	headroomCompressTool = "headroom_compress"
	headroomRetrieveTool = "headroom_retrieve"
	headroomStatsTool    = "headroom_stats"
)

// AttachManagedMCP discovers and validates the actual managed server schema.
// Compression is mandatory for a Headroom engine; retrieval and stats remain
// optional capabilities and are reported honestly in Status.
func (m *Manager) AttachManagedMCP(adapter ManagedMCPAdapter) error {
	if adapter == nil {
		return fmt.Errorf("contextengine: managed MCP adapter is nil")
	}
	schemas, err := adapter.ToolSchemas(ManagedMCPServerName)
	if err != nil {
		return fmt.Errorf("contextengine: discover Headroom tools: %w", err)
	}
	compressOK := schemaRequiresString(schemas[headroomCompressTool], "content")
	retrieveOK := schemaRequiresString(schemas[headroomRetrieveTool], "hash")
	statsOK := schemaIsObject(schemas[headroomStatsTool])

	m.mu.Lock()
	m.adapter = adapter
	m.status.CompressAvailable = compressOK
	m.status.RetrieveAvailable = retrieveOK
	m.status.StatsAvailable = statsOK
	m.status.AdapterReady = compressOK
	m.mu.Unlock()
	if !compressOK {
		return fmt.Errorf("contextengine: Headroom tools/list did not advertise %s with required string field content", headroomCompressTool)
	}
	return nil
}

func schemaIsObject(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var schema struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &schema) == nil && schema.Type == "object"
}

func schemaRequiresString(raw json.RawMessage, field string) bool {
	if len(raw) == 0 {
		return false
	}
	var schema struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if json.Unmarshal(raw, &schema) != nil || schema.Type != "object" || schema.Properties[field].Type != "string" {
		return false
	}
	for _, required := range schema.Required {
		if required == field {
			return true
		}
	}
	return false
}

func noopCompressResponse(content, engine string) CompressResponse {
	if engine == "" {
		engine = ModeNoop
	}
	sum := sha256.Sum256([]byte(content))
	return CompressResponse{
		Content:         content,
		OriginalSHA256:  hex.EncodeToString(sum[:]),
		OriginalBytes:   len(content),
		CompressedBytes: len(content),
		Ratio:           1,
		Engine:          engine,
	}
}

func (m *Manager) compressionFailure(original string, cause error) (CompressResponse, error) {
	err := fmt.Errorf("contextengine: headroom compress: %w", cause)
	m.mu.Lock()
	m.status.LastError = err.Error()
	m.status.Phase = PhaseFailed
	configured := m.cfg.ContextEngine
	if configured == ModeAuto {
		m.fallbackCalls++
		m.status.EffectiveEngine = ModeNoop
		m.status.Degraded = true
		m.status.AdapterReady = false
		m.status.Reason = "Headroom compression failed; auto mode degraded to noop: " + cause.Error()
	} else {
		m.status.Reason = "Headroom compression failed: " + cause.Error()
	}
	m.mu.Unlock()
	if configured == ModeAuto {
		return noopCompressResponse(original, ModeNoop), nil
	}
	return CompressResponse{}, err
}

func parseHeadroomCompress(original, raw string) (CompressResponse, error) {
	var payload struct {
		Compressed       json.RawMessage `json:"compressed"`
		Hash             string          `json:"hash"`
		OriginalTokens   int             `json:"original_tokens"`
		CompressedTokens int             `json:"compressed_tokens"`
		SavingsPercent   float64         `json:"savings_percent"`
		Transforms       []string        `json:"transforms"`
		Error            string          `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return CompressResponse{}, fmt.Errorf("invalid %s JSON: %w", headroomCompressTool, err)
	}
	if strings.TrimSpace(payload.Error) != "" {
		return CompressResponse{}, fmt.Errorf("%s: %s", headroomCompressTool, payload.Error)
	}
	content, err := headroomContent(payload.Compressed)
	if err != nil {
		return CompressResponse{}, fmt.Errorf("%s compressed field: %w", headroomCompressTool, err)
	}
	if strings.TrimSpace(payload.Hash) == "" {
		return CompressResponse{}, fmt.Errorf("%s response omitted retrieval hash", headroomCompressTool)
	}
	sum := sha256.Sum256([]byte(original))
	ratio := byteRatio(len(original), len(content))
	if payload.OriginalTokens > 0 {
		ratio = float64(payload.CompressedTokens) / float64(payload.OriginalTokens)
	}
	savings := payload.SavingsPercent
	if savings == 0 && payload.OriginalTokens > 0 && payload.CompressedTokens < payload.OriginalTokens {
		savings = (1 - ratio) * 100
	}
	return CompressResponse{
		Content:          content,
		OriginalRef:      payload.Hash,
		OriginalSHA256:   hex.EncodeToString(sum[:]),
		OriginalBytes:    len(original),
		CompressedBytes:  len(content),
		Ratio:            ratio,
		Engine:           ModeHeadroom,
		OriginalTokens:   payload.OriginalTokens,
		CompressedTokens: payload.CompressedTokens,
		SavingsPercent:   savings,
		Transforms:       append([]string(nil), payload.Transforms...),
	}, nil
}

func parseHeadroomRetrieve(ref, raw string) (RetrieveResponse, error) {
	var payload struct {
		Hash            string          `json:"hash"`
		Source          string          `json:"source"`
		OriginalContent json.RawMessage `json:"original_content"`
		Results         any             `json:"results"`
		Error           string          `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return RetrieveResponse{}, fmt.Errorf("invalid %s JSON: %w", headroomRetrieveTool, err)
	}
	if strings.TrimSpace(payload.Error) != "" {
		return RetrieveResponse{}, fmt.Errorf("%s: %s", headroomRetrieveTool, payload.Error)
	}
	content, err := headroomContent(payload.OriginalContent)
	if err != nil {
		return RetrieveResponse{}, fmt.Errorf("%s original_content field: %w", headroomRetrieveTool, err)
	}
	if content == "" && payload.Results != nil {
		encoded, marshalErr := json.Marshal(payload.Results)
		if marshalErr != nil {
			return RetrieveResponse{}, fmt.Errorf("%s results: %w", headroomRetrieveTool, marshalErr)
		}
		content = string(encoded)
	}
	if content == "" {
		return RetrieveResponse{}, fmt.Errorf("%s response omitted original_content", headroomRetrieveTool)
	}
	sum := sha256.Sum256([]byte(content))
	if payload.Hash != "" {
		ref = payload.Hash
	}
	return RetrieveResponse{
		Ref:           ref,
		Content:       content,
		OriginalBytes: len(content),
		SHA256:        hex.EncodeToString(sum[:]),
		Engine:        ModeHeadroom,
		Source:        payload.Source,
		Results:       payload.Results,
	}, nil
}

func headroomContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", fmt.Errorf("missing value")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func byteRatio(original, compressed int) float64 {
	if original <= 0 {
		return 1
	}
	return float64(compressed) / float64(original)
}
