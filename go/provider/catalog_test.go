package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFallsBackToSeed(t *testing.T) {
	cat, err := Load(Options{CachePath: filepath.Join(t.TempDir(), "missing.json")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat["anthropic"]; !ok {
		t.Fatal("seed should include anthropic")
	}
	if _, ok := cat["openai"]; !ok {
		t.Fatal("seed should include openai")
	}
}

func TestRefreshFetchesAndCachesCatalog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api.json" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{
		  "zai": {"id":"zai","name":"Z.AI","env":["ZAI_API_KEY"],"models":{"glm": {"id":"glm","name":"GLM","limit":{"context":128000,"output":8192}}}},
		  "anthropic": {"id":"anthropic","name":"Anthropic","env":["ANTHROPIC_API_KEY"],"models":{}}
		}`))
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "models.json")
	cat, err := Refresh(context.Background(), Options{CachePath: path, ModelsURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if cat["zai"].Name != "Z.AI" || cat["zai"].Models["glm"].Limit.Context != 128000 {
		t.Fatalf("catalog decode wrong: %+v", cat["zai"])
	}
	cached, err := Load(Options{CachePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if cached["zai"].Env[0] != "ZAI_API_KEY" {
		t.Fatalf("cached catalog wrong: %+v", cached["zai"])
	}
	env, err := readCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if env.Version != cacheVersion || env.FetchedAt.IsZero() {
		t.Fatalf("cache envelope missing metadata: %+v", env)
	}
}

func TestFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if err := write(path, Seed()); err != nil {
		t.Fatal(err)
	}
	if !Fresh(path, time.Now) {
		t.Fatal("new cache should be fresh")
	}
	if Fresh(filepath.Join(t.TempDir(), "missing.json"), time.Now) {
		t.Fatal("missing cache cannot be fresh")
	}
}

func TestLoadReadsLegacyPlainCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	raw, err := json.Marshal(Catalog{
		"legacy": {ID: "legacy", Name: "Legacy", Env: []string{"LEGACY_KEY"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cat, err := Load(Options{CachePath: path})
	if err != nil {
		t.Fatal(err)
	}
	if cat["legacy"].Name != "Legacy" {
		t.Fatalf("legacy catalog not loaded: %+v", cat)
	}
}

func TestLoadWithStrategyUsesFreshCacheWithoutNetwork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	if err := writeCache(path, Catalog{"cached": {ID: "cached", Name: "Cached"}}, "etag-1", now); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
		t.Fatal("fresh cache should avoid network")
	}))
	defer srv.Close()

	cat, err := LoadWithStrategy(context.Background(), Options{
		CachePath: path,
		ModelsURL: srv.URL,
		Now:       func() time.Time { return now.Add(time.Minute) },
	}, RefreshOnlineIfUncached)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || cat["cached"].Name != "Cached" {
		t.Fatalf("unexpected catalog/calls: calls=%d cat=%+v", calls, cat)
	}
}

func TestRefreshUsesETagAndRenewsCacheOnNotModified(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	now := time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("ETag", "etag-1")
			w.Header().Set("content-type", "application/json")
			w.Write([]byte(`{"zai":{"id":"zai","name":"Z.AI","models":{}}}`))
			return
		}
		if got := r.Header.Get("If-None-Match"); got != "etag-1" {
			t.Fatalf("If-None-Match = %q, want etag-1", got)
		}
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	opts := Options{CachePath: path, ModelsURL: srv.URL, Now: func() time.Time { return now }}
	if _, err := Refresh(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	cat, err := Refresh(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if cat["zai"].Name != "Z.AI" {
		t.Fatalf("304 should return cached catalog: %+v", cat)
	}
	env, err := readCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if !env.FetchedAt.Equal(now) {
		t.Fatalf("304 should renew fetched_at: %s want %s", env.FetchedAt, now)
	}
}

func TestSortedFillsID(t *testing.T) {
	rows := Sorted(Catalog{
		"b": {Name: "Beta"},
		"a": {Name: "Alpha"},
	})
	if len(rows) != 2 || rows[0].ID != "a" || rows[1].ID != "b" {
		t.Fatalf("sorted rows wrong: %+v", rows)
	}
}
