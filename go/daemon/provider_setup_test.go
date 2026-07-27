package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/provider"
)

func TestProviderSetupValidatesBeforePersistingWithoutLeakingSecret(t *testing.T) {
	const secret = "sk-provider-secret"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatalf("validation request path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL+"/v1")

	store, err := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewProviderSetupService(store, provider.Catalog{
		"openai": {ID: "openai", Name: "OpenAI", API: server.URL + "/v1", NPM: "@ai-sdk/openai"},
	}, server.Client())
	result, err := service.ValidateAndStoreAPIKey(context.Background(), "openai", secret)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || result.Source != "auth:openai" {
		t.Fatalf("requests=%d result=%+v", requests, result)
	}
	credential, ok, err := store.Get("openai")
	if err != nil || !ok || credential.Key != secret {
		t.Fatalf("credential persisted=%v value=%q err=%v", ok, credential.Key, err)
	}
}

func TestProviderSetupRejectsInvalidCredentialBeforePersisting(t *testing.T) {
	const secret = "sk-invalid-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL+"/v1")
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	service := NewProviderSetupService(store, provider.Catalog{
		"openai": {ID: "openai", API: server.URL + "/v1", NPM: "@ai-sdk/openai"},
	}, server.Client())
	_, err := service.ValidateAndStoreAPIKey(context.Background(), "openai", secret)
	if err == nil {
		t.Fatal("invalid credential was accepted")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("secret leaked in error: %v", err)
	}
	if _, ok, getErr := store.Get("openai"); getErr != nil || ok {
		t.Fatalf("invalid credential persisted: ok=%v err=%v", ok, getErr)
	}
}

func TestProviderSetupUsesHeaderForGeminiSecret(t *testing.T) {
	const secret = "gemini-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), secret) || r.Header.Get("x-goog-api-key") != secret {
			t.Fatalf("Gemini secret placement url=%q header=%q", r.URL.String(), r.Header.Get("x-goog-api-key"))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	service := NewProviderSetupService(store, provider.Catalog{
		"google": {ID: "google", API: server.URL + "/v1beta", NPM: "@ai-sdk/google"},
	}, server.Client())
	if _, err := service.ValidateAndStoreAPIKey(context.Background(), "google", secret); err != nil {
		t.Fatal(err)
	}
}

func TestProviderSetupCancellationDoesNotPersistCredential(t *testing.T) {
	const secret = "sk-canceled-secret"
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL+"/v1")
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	service := NewProviderSetupService(store, provider.Catalog{
		"openai": {ID: "openai", API: server.URL + "/v1", NPM: "@ai-sdk/openai"},
	}, server.Client())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := service.ValidateAndStoreAPIKey(ctx, "openai", secret)
		done <- err
	}()
	<-started
	cancel()
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "validation canceled") || strings.Contains(err.Error(), secret) {
		t.Fatalf("cancellation error = %v", err)
	}
	if _, ok, getErr := store.Get("openai"); getErr != nil || ok {
		t.Fatalf("canceled credential persisted: ok=%v err=%v", ok, getErr)
	}
}
