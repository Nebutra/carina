package daemon

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/provider"
)

type providerSetupRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn providerSetupRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestProviderSetupValidatesBeforePersistingWithoutLeakingSecret(t *testing.T) {
	const secret = "sk-provider-secret"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatalf("validation request method=%q path=%q auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_validation","output":[]}`))
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
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"models":[]}`))
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
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
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
	close(release)
	if err == nil || !strings.Contains(err.Error(), "validation canceled") || strings.Contains(err.Error(), secret) {
		t.Fatalf("cancellation error = %v", err)
	}
	if _, ok, getErr := store.Get("openai"); getErr != nil || ok {
		t.Fatalf("canceled credential persisted: ok=%v err=%v", ok, getErr)
	}
}

func TestProviderSetupPersistsSafeDiscoveryMetadataAfterValidation(t *testing.T) {
	const secret = "cc-switch-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+secret {
			t.Fatalf("validation request method=%q path=%q auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_validation","output":[]}`))
	}))
	defer server.Close()
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	const providerID = "ccswitch-codex-safe"
	service := NewProviderSetupService(store, provider.Catalog{
		providerID: {ID: providerID, API: server.URL + "/v1", APIProtocol: "openai-responses"},
	}, server.Client())
	metadata := map[string]string{"source": provider.CCSwitchSourceKind}
	if _, err := service.ValidateAndStoreAPIKeyWithMetadata(context.Background(), providerID, secret, metadata); err != nil {
		t.Fatal(err)
	}
	credential, ok, err := store.Get(providerID)
	if err != nil || !ok || credential.Key != secret || credential.Metadata["source"] != provider.CCSwitchSourceKind {
		t.Fatalf("stored credential = %#v, ok=%v err=%v", credential, ok, err)
	}
}

func TestProviderSetupAnthropicBearerUsesVersionedEndpointAndPersistsAfterProtocolValidation(t *testing.T) {
	const secret = "cc-switch-bearer-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("x-api-key") != "" {
			t.Fatalf("Anthropic bearer headers auth=%q api-key=%q", r.Header.Get("Authorization"), r.Header.Get("x-api-key"))
		}
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected validation path %q", r.URL.Path)
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"OK"}]}`))
	}))
	defer server.Close()
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	const providerID = "ccswitch-claude-safe"
	service := NewProviderSetupService(store, provider.Catalog{
		providerID: {
			ID: providerID, API: server.URL, APIProtocol: "anthropic",
			Models: map[string]provider.Model{"claude-test": {ID: "claude-test"}},
		},
	}, server.Client())
	metadata := map[string]string{"source": provider.CCSwitchSourceKind}
	if _, err := service.ValidateAndStoreCredentialWithMetadata(context.Background(), providerID, auth.Bearer, secret, metadata); err != nil {
		t.Fatal(err)
	}
	stored, ok, err := store.Get(providerID)
	if err != nil || !ok || stored.Type != auth.Bearer || stored.Access != secret || stored.Metadata["validation"] != providerValidationContract {
		t.Fatalf("stored bearer = %#v, ok=%v err=%v", stored, ok, err)
	}
}

func TestProviderSetupRejectsEndpointRestrictedToAnotherClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected validation path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"No available accounts: this group only allows another CLI clients"}}`))
	}))
	defer server.Close()
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	const providerID = "ccswitch-claude-restricted"
	service := NewProviderSetupService(store, provider.Catalog{
		providerID: {
			ID: providerID, Name: "Restricted Relay", API: server.URL, APIProtocol: "anthropic",
			Source: &provider.Source{Kind: provider.CCSwitchSourceKind, Label: provider.CCSwitchSourceLabel},
			Models: map[string]provider.Model{"claude-test": {ID: "claude-test"}},
		},
	}, server.Client())
	_, err := service.ValidateAndStoreCredentialWithMetadata(context.Background(), providerID, auth.Bearer, "secret", map[string]string{"source": provider.CCSwitchSourceKind})
	if err == nil || !strings.Contains(err.Error(), "Restricted Relay") || !strings.Contains(err.Error(), "rejects this client type") || strings.Contains(err.Error(), providerID) {
		t.Fatalf("restricted client error = %v", err)
	}
	if _, ok, getErr := store.Get(providerID); getErr != nil || ok {
		t.Fatalf("restricted credential persisted: ok=%v err=%v", ok, getErr)
	}
}

func TestProviderSetupExplainsCCSwitchDNSFailureWithoutLeakingRuntimeIDOrSecret(t *testing.T) {
	const providerID = "ccswitch-codex-private-id"
	const secret = "dns-secret"
	store, _ := auth.NewStore(filepath.Join(t.TempDir(), "auth.json"))
	client := &http.Client{Transport: providerSetupRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &net.DNSError{Err: "no such host", Name: "aigw.mox.ktvsky.com", IsNotFound: true}
	})}
	service := NewProviderSetupService(store, provider.Catalog{
		providerID: {
			ID: providerID, Name: "Mox", API: "https://aigw.mox.ktvsky.com", APIProtocol: "openai-responses",
			Source: &provider.Source{Kind: provider.CCSwitchSourceKind, Label: provider.CCSwitchSourceLabel},
		},
	}, client)

	_, err := service.ValidateAndStoreAPIKeyWithMetadata(context.Background(), providerID, secret, map[string]string{"source": provider.CCSwitchSourceKind})
	if err == nil || !strings.Contains(err.Error(), "endpoint host \"aigw.mox.ktvsky.com\" could not be resolved") || !strings.Contains(err.Error(), "check DNS/network or update the CC Switch profile endpoint") {
		t.Fatalf("DNS error = %v", err)
	}
	if strings.Contains(err.Error(), providerID) || strings.Contains(err.Error(), secret) {
		t.Fatalf("DNS error leaked private data: %v", err)
	}
	if _, ok, getErr := store.Get(providerID); getErr != nil || ok {
		t.Fatalf("DNS-failed credential persisted: ok=%v err=%v", ok, getErr)
	}
}
