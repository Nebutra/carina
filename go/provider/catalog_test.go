package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func TestSortedFillsID(t *testing.T) {
	rows := Sorted(Catalog{
		"b": {Name: "Beta"},
		"a": {Name: "Alpha"},
	})
	if len(rows) != 2 || rows[0].ID != "a" || rows[1].ID != "b" {
		t.Fatalf("sorted rows wrong: %+v", rows)
	}
}
