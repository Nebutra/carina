package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Nebutra/carina/go/provider"
)

const usageCostScale = 1_000_000

// ModelUsage is the provider-neutral token accounting contract. InputTokens
// excludes cache hits; cache reads and writes are kept separate so costs are
// not double-counted across provider-specific response formats.
type ModelUsage struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	Estimated        bool   `json:"estimated"`
}

func (u ModelUsage) totalTokens() int {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

type usageAggregate struct {
	SessionID string `json:"session_id"`
	TaskID    string `json:"task_id"`
	ModelUsage
	Requests int `json:"requests"`
}

type usageStore struct {
	mu      sync.Mutex
	path    string
	records map[string]*usageAggregate
}

type usageEnvelope struct {
	Version int               `json:"version"`
	Records []*usageAggregate `json:"records"`
}

func newUsageStore(stateDir string) *usageStore {
	s := &usageStore{path: filepath.Join(stateDir, "model-usage.json"), records: map[string]*usageAggregate{}}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return s
	}
	var env usageEnvelope
	if json.Unmarshal(raw, &env) != nil || env.Version != 1 {
		return s
	}
	for _, record := range env.Records {
		if record != nil {
			s.records[usageKey(record.SessionID, record.TaskID, record.Provider, record.Model)] = record
		}
	}
	return s
}

func (s *usageStore) record(sessionID, taskID string, usage ModelUsage) error {
	if s == nil {
		return nil
	}
	usage.Provider = strings.TrimSpace(usage.Provider)
	usage.Model = strings.TrimSpace(usage.Model)
	if usage.Provider == "" {
		usage.Provider = "unknown"
	}
	if usage.Model == "" {
		usage.Model = "default"
	}
	usage.InputTokens = max(0, usage.InputTokens)
	usage.OutputTokens = max(0, usage.OutputTokens)
	usage.CacheReadTokens = max(0, usage.CacheReadTokens)
	usage.CacheWriteTokens = max(0, usage.CacheWriteTokens)

	s.mu.Lock()
	defer s.mu.Unlock()
	key := usageKey(sessionID, taskID, usage.Provider, usage.Model)
	record := s.records[key]
	if record == nil {
		record = &usageAggregate{SessionID: sessionID, TaskID: taskID, ModelUsage: ModelUsage{Provider: usage.Provider, Model: usage.Model}}
		s.records[key] = record
	}
	record.Requests++
	record.InputTokens += usage.InputTokens
	record.OutputTokens += usage.OutputTokens
	record.CacheReadTokens += usage.CacheReadTokens
	record.CacheWriteTokens += usage.CacheWriteTokens
	record.Estimated = record.Estimated || usage.Estimated
	return s.persistLocked()
}

func usageKey(sessionID, taskID, providerID, model string) string {
	return strings.Join([]string{sessionID, taskID, providerID, model}, "\x00")
}

func (s *usageStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	records := make([]*usageAggregate, 0, len(s.records))
	for _, record := range s.records {
		copy := *record
		records = append(records, &copy)
	}
	sort.Slice(records, func(i, j int) bool {
		return usageKey(records[i].SessionID, records[i].TaskID, records[i].Provider, records[i].Model) <
			usageKey(records[j].SessionID, records[j].TaskID, records[j].Provider, records[j].Model)
	})
	raw, err := json.MarshalIndent(usageEnvelope{Version: 1, Records: records}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

type usageCostRow struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Requests         int     `json:"requests"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	PricingKnown     bool    `json:"pricing_known"`
	Estimated        bool    `json:"estimated"`
}

type usageCostTotals struct {
	Requests         int     `json:"requests"`
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	CacheReadTokens  int     `json:"cache_read_tokens"`
	CacheWriteTokens int     `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	PricingKnown     bool    `json:"pricing_known"`
}

type usageCostResponse struct {
	Providers []usageCostRow  `json:"providers"`
	Totals    usageCostTotals `json:"totals"`
	Estimated bool            `json:"estimated"`
}

func (s *usageStore) costs(sessionID, taskID string, catalog provider.Catalog) usageCostResponse {
	grouped := map[string]*usageCostRow{}
	if s != nil {
		s.mu.Lock()
		for _, record := range s.records {
			if sessionID != "" && record.SessionID != sessionID || taskID != "" && record.TaskID != taskID {
				continue
			}
			key := usageKey("", "", record.Provider, record.Model)
			row := grouped[key]
			if row == nil {
				row = &usageCostRow{Provider: record.Provider, Model: record.Model, PricingKnown: true}
				grouped[key] = row
			}
			row.Requests += record.Requests
			row.InputTokens += record.InputTokens
			row.OutputTokens += record.OutputTokens
			row.CacheReadTokens += record.CacheReadTokens
			row.CacheWriteTokens += record.CacheWriteTokens
			row.Estimated = row.Estimated || record.Estimated
		}
		s.mu.Unlock()
	}

	response := usageCostResponse{Providers: make([]usageCostRow, 0, len(grouped))}
	response.Totals.PricingKnown = true
	for _, row := range grouped {
		cost, ok := modelCost(catalog, row.Provider, row.Model)
		row.PricingKnown = ok
		if ok {
			row.CostUSD = tokenCost(row.InputTokens, cost.Input) + tokenCost(row.OutputTokens, cost.Output) +
				tokenCost(row.CacheReadTokens, cost.CacheRead) + tokenCost(row.CacheWriteTokens, cost.CacheWrite)
		}
		response.Providers = append(response.Providers, *row)
		response.Totals.Requests += row.Requests
		response.Totals.InputTokens += row.InputTokens
		response.Totals.OutputTokens += row.OutputTokens
		response.Totals.CacheReadTokens += row.CacheReadTokens
		response.Totals.CacheWriteTokens += row.CacheWriteTokens
		response.Totals.CostUSD += row.CostUSD
		response.Totals.PricingKnown = response.Totals.PricingKnown && row.PricingKnown
		response.Estimated = response.Estimated || row.Estimated
	}
	sort.Slice(response.Providers, func(i, j int) bool {
		if response.Providers[i].Provider == response.Providers[j].Provider {
			return response.Providers[i].Model < response.Providers[j].Model
		}
		return response.Providers[i].Provider < response.Providers[j].Provider
	})
	return response
}

func modelCost(catalog provider.Catalog, providerID, modelID string) (provider.ModelCost, bool) {
	info, ok := catalog[providerID]
	if !ok {
		return provider.ModelCost{}, false
	}
	modelID = strings.TrimPrefix(modelID, providerID+"/")
	if model, ok := info.Models[modelID]; ok && model.Cost != nil {
		return *model.Cost, true
	}
	for baseID, model := range info.Models {
		for modeName, mode := range model.ExperimentalModes() {
			if modelID != baseID+"-"+modeName {
				continue
			}
			if mode.Cost != nil {
				return *mode.Cost, true
			}
			if model.Cost != nil {
				return *model.Cost, true
			}
		}
	}
	return provider.ModelCost{}, false
}

func tokenCost(tokens int, pricePerMillion float64) float64 {
	return float64(tokens) * pricePerMillion / usageCostScale
}

func (d *Daemon) handleUsageCost(params json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		TaskID    string `json:"task_id"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	if p.SessionID != "" {
		if _, ok := d.store.Get(p.SessionID); !ok {
			return nil, fmt.Errorf("unknown session %s", p.SessionID)
		}
	}
	if p.TaskID != "" {
		task, ok := d.sched.Get(p.TaskID)
		if !ok {
			return nil, fmt.Errorf("unknown task %s", p.TaskID)
		}
		if p.SessionID != "" && task.SessionID != p.SessionID {
			return nil, fmt.Errorf("task %s does not belong to session %s", p.TaskID, p.SessionID)
		}
	}
	return d.usage.costs(p.SessionID, p.TaskID, d.providerCatalog), nil
}
