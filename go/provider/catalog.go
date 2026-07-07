// Package provider loads the provider/model catalog used by Carina's BYOK
// management surface. The catalog format follows the public models.dev API.
package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	DefaultModelsURL = "https://models.dev"
	cacheTTL         = 5 * time.Minute
	cacheVersion     = "1"
)

// Catalog maps provider id to provider metadata.
type Catalog map[string]Info

type RefreshStrategy string

const (
	RefreshOnline           RefreshStrategy = "online"
	RefreshOffline          RefreshStrategy = "offline"
	RefreshOnlineIfUncached RefreshStrategy = "online_if_uncached"
)

func ParseRefreshStrategy(raw string) (RefreshStrategy, error) {
	switch strings.TrimSpace(raw) {
	case "", string(RefreshOnlineIfUncached):
		return RefreshOnlineIfUncached, nil
	case string(RefreshOnline):
		return RefreshOnline, nil
	case string(RefreshOffline):
		return RefreshOffline, nil
	default:
		return "", fmt.Errorf("unknown provider refresh strategy %q", raw)
	}
}

type cacheEnvelope struct {
	Version   string    `json:"version"`
	FetchedAt time.Time `json:"fetched_at"`
	ETag      string    `json:"etag,omitempty"`
	Catalog   Catalog   `json:"catalog"`
}

// Info is the provider subset Carina needs for enumeration and auth discovery.
type Info struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Doc    string           `json:"doc,omitempty"`
	API    string           `json:"api,omitempty"`
	Env    []string         `json:"env,omitempty"`
	NPM    string           `json:"npm,omitempty"`
	Models map[string]Model `json:"models,omitempty"`
}

// Model is a models.dev model entry. Only fields useful to Carina's public
// listing are modeled here; unknown provider-specific fields are ignored.
type Model struct {
	ID               string             `json:"id"`
	Name             string             `json:"name"`
	Description      string             `json:"description,omitempty"`
	Family           string             `json:"family,omitempty"`
	ReleaseDate      string             `json:"release_date,omitempty"`
	LastUpdated      string             `json:"last_updated,omitempty"`
	Knowledge        string             `json:"knowledge,omitempty"`
	Limit            ModelLimit         `json:"limit"`
	Cost             *ModelCost         `json:"cost,omitempty"`
	Modalities       *Modalities        `json:"modalities,omitempty"`
	Status           string             `json:"status,omitempty"`
	Provider         *ModelAPI          `json:"provider,omitempty"`
	Experimental     *ModelExperimental `json:"experimental,omitempty"`
	Reasoning        bool               `json:"reasoning,omitempty"`
	ReasoningOptions []json.RawMessage  `json:"reasoning_options,omitempty"`
	ToolCall         bool               `json:"tool_call,omitempty"`
	Attachment       bool               `json:"attachment,omitempty"`
	Temperature      bool               `json:"temperature,omitempty"`
	OpenWeights      bool               `json:"open_weights,omitempty"`
}

func (m Model) ExperimentalModes() map[string]ModelMode {
	if m.Experimental == nil {
		return nil
	}
	return m.Experimental.Modes
}

type ModelLimit struct {
	Context int `json:"context"`
	Input   int `json:"input,omitempty"`
	Output  int `json:"output"`
}

type ModelCost struct {
	Input           float64         `json:"input"`
	Output          float64         `json:"output"`
	CacheRead       float64         `json:"cache_read,omitempty"`
	CacheWrite      float64         `json:"cache_write,omitempty"`
	Tiers           []ModelCostTier `json:"tiers,omitempty"`
	ContextOver200K *ModelCostBase  `json:"context_over_200k,omitempty"`
}

type ModelCostBase struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

type ModelCostTier struct {
	Input      float64       `json:"input"`
	Output     float64       `json:"output"`
	CacheRead  float64       `json:"cache_read,omitempty"`
	CacheWrite float64       `json:"cache_write,omitempty"`
	Tier       ModelCostBand `json:"tier"`
}

type ModelCostBand struct {
	Type string `json:"type"`
	Size int    `json:"size"`
}

type Modalities struct {
	Input  []string `json:"input,omitempty"`
	Output []string `json:"output,omitempty"`
}

type ModelAPI struct {
	NPM string `json:"npm,omitempty"`
	API string `json:"api,omitempty"`
}

type ModelExperimental struct {
	Modes map[string]ModelMode `json:"modes,omitempty"`
}

type ModelMode struct {
	Cost     *ModelCost             `json:"cost,omitempty"`
	Provider *ModelProviderOverride `json:"provider,omitempty"`
}

type ModelProviderOverride struct {
	Body    map[string]json.RawMessage `json:"body,omitempty"`
	Headers map[string]string          `json:"headers,omitempty"`
}

// Options controls catalog loading and refreshing.
type Options struct {
	CachePath string
	ModelsURL string
	HTTP      *http.Client
	Now       func() time.Time
}

// DefaultCachePath returns ~/.carina/cache/models.json.
func DefaultCachePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".carina", "cache", "models.json"), nil
}

// Load returns a cached catalog when present, otherwise the bundled seed.
func Load(opts Options) (Catalog, error) {
	if path := opts.CachePath; path != "" {
		if cat, err := read(path); err == nil && len(cat) > 0 {
			return cat, nil
		}
	}
	return Seed(), nil
}

func LoadWithStrategy(ctx context.Context, opts Options, strategy RefreshStrategy) (Catalog, error) {
	switch strategy {
	case RefreshOnline:
		return Refresh(ctx, opts)
	case RefreshOffline:
		return Load(opts)
	case RefreshOnlineIfUncached, "":
		path, err := cachePath(opts)
		if err != nil {
			return nil, err
		}
		if env, err := readCache(path); err == nil && len(env.Catalog) > 0 && freshEnvelope(env, now(opts)) {
			return env.Catalog, nil
		}
		if cat, err := Refresh(ctx, opts); err == nil {
			return cat, nil
		}
		return Load(opts)
	default:
		return nil, fmt.Errorf("unknown provider refresh strategy %q", strategy)
	}
}

// Refresh fetches the latest catalog and writes it to the cache atomically.
func Refresh(ctx context.Context, opts Options) (Catalog, error) {
	url := strings.TrimRight(opts.ModelsURL, "/")
	if url == "" {
		url = DefaultModelsURL
	}
	path, err := cachePath(opts)
	if err != nil {
		return nil, err
	}
	client := opts.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api.json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "carina/provider-catalog")
	if env, err := readCache(path); err == nil && env.ETag != "" {
		req.Header.Set("If-None-Match", env.ETag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		env, err := readCache(path)
		if err != nil || len(env.Catalog) == 0 {
			return nil, fmt.Errorf("provider catalog: 304 without usable cache")
		}
		env.FetchedAt = now(opts)
		if err := writeEnvelope(path, env); err != nil {
			return nil, err
		}
		return env.Catalog, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("provider catalog: models.dev status %d", resp.StatusCode)
	}
	var cat Catalog
	if err := json.NewDecoder(resp.Body).Decode(&cat); err != nil {
		return nil, err
	}
	if len(cat) == 0 {
		return nil, fmt.Errorf("provider catalog: empty response")
	}
	if err := writeCache(path, cat, resp.Header.Get("ETag"), now(opts)); err != nil {
		return nil, err
	}
	return cat, nil
}

// Fresh reports whether the cache is recent enough to use without refresh.
func Fresh(path string, now func() time.Time) bool {
	if now == nil {
		now = time.Now
	}
	if env, err := readCache(path); err == nil && len(env.Catalog) > 0 {
		return freshEnvelope(env, now())
	}
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return now().Sub(st.ModTime()) < cacheTTL
}

// Sorted returns providers ordered by display name, then id.
func Sorted(cat Catalog) []Info {
	out := make([]Info, 0, len(cat))
	for id, p := range cat {
		if p.ID == "" {
			p.ID = id
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		li := strings.ToLower(out[i].Name)
		lj := strings.ToLower(out[j].Name)
		if li == lj {
			return out[i].ID < out[j].ID
		}
		return li < lj
	})
	return out
}

func read(path string) (Catalog, error) {
	env, err := readCache(path)
	if err == nil {
		return env.Catalog, nil
	}
	return nil, err
}

func readCache(path string) (cacheEnvelope, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cacheEnvelope{}, err
	}
	var env cacheEnvelope
	if err := json.Unmarshal(b, &env); err == nil && env.Catalog != nil {
		if env.FetchedAt.IsZero() {
			if st, statErr := os.Stat(path); statErr == nil {
				env.FetchedAt = st.ModTime()
			}
		}
		return env, nil
	}
	var cat Catalog
	if err := json.Unmarshal(b, &cat); err != nil {
		return cacheEnvelope{}, err
	}
	fetchedAt := time.Time{}
	if st, statErr := os.Stat(path); statErr == nil {
		fetchedAt = st.ModTime()
	}
	return cacheEnvelope{Version: "legacy", FetchedAt: fetchedAt, Catalog: cat}, nil
}

func write(path string, cat Catalog) error {
	return writeCache(path, cat, "", time.Now())
}

func writeCache(path string, cat Catalog, etag string, fetchedAt time.Time) error {
	return writeEnvelope(path, cacheEnvelope{
		Version:   cacheVersion,
		FetchedAt: fetchedAt.UTC(),
		ETag:      etag,
		Catalog:   cat,
	})
}

func writeEnvelope(path string, env cacheEnvelope) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if env.Version == "" {
		env.Version = cacheVersion
	}
	if env.FetchedAt.IsZero() {
		env.FetchedAt = time.Now().UTC()
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func cachePath(opts Options) (string, error) {
	if opts.CachePath != "" {
		return opts.CachePath, nil
	}
	return DefaultCachePath()
}

func freshEnvelope(env cacheEnvelope, t time.Time) bool {
	if t.IsZero() {
		t = time.Now()
	}
	if env.FetchedAt.IsZero() {
		return false
	}
	return t.Sub(env.FetchedAt) < cacheTTL
}

func now(opts Options) time.Time {
	if opts.Now != nil {
		return opts.Now()
	}
	return time.Now()
}
