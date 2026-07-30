package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreAPIKeyRoundTripAndSafeList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAPIKey("Anthropic", "sk-secret", map[string]string{"resource": "default"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("auth store must be 0600, got %o", st.Mode().Perm())
	}
	cred, ok, err := store.Get("anthropic")
	if err != nil || !ok {
		t.Fatalf("get: %+v ok=%v err=%v", cred, ok, err)
	}
	if cred.Key != "sk-secret" || cred.Type != APIKey {
		t.Fatalf("stored credential wrong: %+v", cred)
	}
	safe, err := store.ListSafe()
	if err != nil {
		t.Fatal(err)
	}
	if len(safe) != 1 || safe[0].Provider != "anthropic" || safe[0].Type != APIKey {
		t.Fatalf("safe list wrong: %+v", safe)
	}
	if contains(mustRead(t, path), "sk-secret") == false {
		t.Fatal("test setup expected secret on disk")
	}
	if contains(safe[0].Provider+string(safe[0].Type), "sk-secret") {
		t.Fatal("safe credential must not expose secret")
	}
}

func TestStoreKeySource(t *testing.T) {
	store, _ := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err := store.SetAPIKey("anthropic", "sk-store", nil); err != nil {
		t.Fatal(err)
	}
	cred, ok := StoreKey{Store: store, Provider: "anthropic"}.Resolve()
	if !ok || cred.Value != "sk-store" || cred.Source != "auth:anthropic" {
		t.Fatalf("store source wrong: %+v ok=%v", cred, ok)
	}
	if _, ok := (StoreKey{Store: store, Provider: "openai"}).Resolve(); ok {
		t.Fatal("missing provider should not resolve")
	}
}

func TestStoreBearerTokenRoundTrip(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetBearerToken("relay", "bearer-secret", map[string]string{"source": "test"}); err != nil {
		t.Fatal(err)
	}
	cred, ok := StoreKey{Store: store, Provider: "relay"}.Resolve()
	if !ok || cred.Kind != Bearer || cred.Value != "bearer-secret" || cred.Source != "auth:relay" {
		t.Fatalf("resolved bearer credential = %+v, ok=%v", cred, ok)
	}
	listed, err := store.ListSafe()
	if err != nil || len(listed) != 1 || listed[0].Type != Bearer {
		t.Fatalf("safe bearer listing = %+v, err=%v", listed, err)
	}
}

func TestAuthContentEnv(t *testing.T) {
	t.Setenv(AuthContentEnv, `{"openai":{"type":"api_key","key":"sk-env-json"}}`)
	store, _ := NewStore(filepath.Join(t.TempDir(), "auth.json"))
	cred, ok, err := store.Get("openai")
	if err != nil || !ok || cred.Key != "sk-env-json" {
		t.Fatalf("env auth content not loaded: %+v ok=%v err=%v", cred, ok, err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
