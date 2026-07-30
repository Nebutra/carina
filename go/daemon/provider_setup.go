package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/provider"
)

const providerValidationTimeout = 15 * time.Second

const providerValidationContract = "protocol-v2"

// ProviderSetupService validates user credentials against the same catalog,
// protocol, endpoint, and provider compatibility rules used by the daemon.
// Secrets are accepted only as transient arguments and are persisted after a
// successful validation response.
type ProviderSetupService struct {
	Store   *auth.Store
	Catalog provider.Catalog
	HTTP    *http.Client
}

type ProviderSetupResult struct {
	Provider string
	Source   string
}

func NewProviderSetupService(store *auth.Store, catalog provider.Catalog, client *http.Client) ProviderSetupService {
	return ProviderSetupService{Store: store, Catalog: catalog, HTTP: client}
}

func DefaultProviderSetupService() (ProviderSetupService, error) {
	store, err := auth.NewStore("")
	if err != nil {
		return ProviderSetupService{}, err
	}
	cachePath, err := provider.DefaultCachePath()
	if err != nil {
		return ProviderSetupService{}, err
	}
	catalog, err := provider.Load(provider.Options{CachePath: cachePath})
	if err != nil {
		return ProviderSetupService{}, err
	}
	return NewProviderSetupService(store, catalog, nil), nil
}

func (s ProviderSetupService) ValidateAndStoreAPIKey(ctx context.Context, providerID, key string) (ProviderSetupResult, error) {
	return s.ValidateAndStoreAPIKeyWithMetadata(ctx, providerID, key, nil)
}

func (s ProviderSetupService) ValidateAndStoreAPIKeyWithMetadata(ctx context.Context, providerID, key string, metadata map[string]string) (ProviderSetupResult, error) {
	return s.ValidateAndStoreCredentialWithMetadata(ctx, providerID, auth.APIKey, key, metadata)
}

func (s ProviderSetupService) ValidateAndStoreCredentialWithMetadata(ctx context.Context, providerID string, kind auth.Kind, secret string, metadata map[string]string) (ProviderSetupResult, error) {
	providerID = normalizeProviderID(providerID)
	secret = strings.TrimSpace(secret)
	if providerID == "" || secret == "" {
		return ProviderSetupResult{}, fmt.Errorf("provider setup: provider and credential are required")
	}
	if kind != auth.APIKey && kind != auth.Bearer {
		return ProviderSetupResult{}, fmt.Errorf("provider setup: unsupported credential type %q", kind)
	}
	info, ok := s.Catalog[providerID]
	if !ok {
		return ProviderSetupResult{}, fmt.Errorf("provider setup: unsupported provider %q", providerID)
	}
	info.ID = providerID
	if err := s.validateCredential(ctx, info, auth.Credential{Kind: kind, Value: secret}); err != nil {
		return ProviderSetupResult{}, err
	}
	if s.Store == nil {
		return ProviderSetupResult{}, fmt.Errorf("provider setup: credential store is unavailable")
	}
	metadata = cloneCredentialMetadata(metadata)
	metadata["validation"] = providerValidationContract
	var err error
	if kind == auth.Bearer {
		err = s.Store.SetBearerToken(providerID, secret, metadata)
	} else {
		err = s.Store.SetAPIKey(providerID, secret, metadata)
	}
	if err != nil {
		return ProviderSetupResult{}, fmt.Errorf("provider setup: persist %s credential: %w", providerID, err)
	}
	return ProviderSetupResult{Provider: providerID, Source: "auth:" + providerID}, nil
}

func cloneCredentialMetadata(metadata map[string]string) map[string]string {
	cloned := make(map[string]string, len(metadata)+1)
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}

func (s ProviderSetupService) validateCredential(parent context.Context, info provider.Info, credential auth.Credential) error {
	errorName := runtimeProviderErrorName(info)
	protocol := detectRuntimeProtocol(info)
	if protocol == protocolUnsupported {
		return fmt.Errorf("provider setup: %s is not supported by the runtime", errorName)
	}
	baseURL, ok := runtimeBaseURL(info)
	if !ok || strings.TrimSpace(baseURL) == "" {
		return fmt.Errorf("provider setup: %s has no usable endpoint", errorName)
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	if protocol == protocolAnthropic {
		endpoint = anthropicEndpoint(baseURL, "models") + "?limit=1"
	} else if protocol == protocolGemini {
		endpoint += "?pageSize=1"
	}
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return fmt.Errorf("provider setup: %s endpoint is invalid", errorName)
	}
	ctx, cancel := context.WithTimeout(parent, providerValidationTimeout)
	defer cancel()
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: providerValidationTimeout}
	}
	if protocol == protocolAnthropic {
		return s.validateAnthropicMessage(ctx, info, baseURL, credential, client)
	}
	if protocol == protocolOpenAIResponses {
		return s.validateOpenAIResponse(ctx, info, baseURL, credential, client)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("provider setup: prepare %s validation: %w", info.ID, err)
	}
	applyProviderValidationCredential(req, protocol, credential)
	resp, err := client.Do(req)
	if err != nil {
		return providerValidationTransportError(info, baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		var payload any
		if err := decodeProviderJSON(errorName, resp, &payload); err != nil {
			return fmt.Errorf("provider setup: %w", err)
		}
		return nil
	}
	return fmt.Errorf("provider setup: %w", statusError(errorName, resp))
}

func (s ProviderSetupService) validateOpenAIResponse(ctx context.Context, info provider.Info, baseURL string, credential auth.Credential, client *http.Client) error {
	errorName := runtimeProviderErrorName(info)
	body, _ := json.Marshal(map[string]any{
		"model": runtimeDefaultModel(info), "input": "Reply OK", "max_output_tokens": 16, "stream": false,
	})
	endpoint := strings.TrimRight(baseURL, "/") + "/responses"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("provider setup: prepare %s validation: %w", errorName, err)
	}
	req.Header.Set("content-type", "application/json")
	applyProviderValidationCredential(req, protocolOpenAIResponses, credential)
	resp, err := client.Do(req)
	if err != nil {
		return providerValidationTransportError(info, baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("provider setup: %w", statusError(errorName, resp))
	}
	var payload any
	if err := decodeProviderJSON(errorName, resp, &payload); err != nil {
		return fmt.Errorf("provider setup: %w", err)
	}
	return nil
}

func applyProviderValidationCredential(req *http.Request, protocol runtimeProtocol, credential auth.Credential) {
	switch protocol {
	case protocolAnthropic:
		credential.Apply(req.Header)
		req.Header.Set("anthropic-version", "2023-06-01")
	case protocolGemini:
		req.Header.Set("x-goog-api-key", credential.Value)
	default:
		req.Header.Set("Authorization", "Bearer "+credential.Value)
	}
}

func (s ProviderSetupService) validateAnthropicMessage(ctx context.Context, info provider.Info, baseURL string, credential auth.Credential, client *http.Client) error {
	errorName := runtimeProviderErrorName(info)
	body, _ := json.Marshal(map[string]any{
		"model": runtimeDefaultModel(info), "max_tokens": 1,
		"messages": []map[string]string{{"role": "user", "content": "Reply OK"}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint(baseURL, "messages"), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("provider setup: prepare %s validation: %w", errorName, err)
	}
	req.Header.Set("content-type", "application/json")
	applyProviderValidationCredential(req, protocolAnthropic, credential)
	resp, err := client.Do(req)
	if err != nil {
		return providerValidationTransportError(info, baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("provider setup: %w", statusError(errorName, resp))
	}
	var payload any
	if err := decodeProviderJSON(errorName, resp, &payload); err != nil {
		return fmt.Errorf("provider setup: %w", err)
	}
	return nil
}

func providerValidationTransportError(info provider.Info, baseURL string, err error) error {
	errorName := runtimeProviderErrorName(info)
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("provider setup: %s validation canceled", errorName)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("provider setup: %s validation timed out", errorName)
	}
	host := providerValidationHost(baseURL)
	var dnsError *net.DNSError
	if errors.As(err, &dnsError) {
		if host == "" {
			host = strings.TrimSpace(dnsError.Name)
		}
		if host != "" {
			return fmt.Errorf(
				"provider setup: %s endpoint host %q could not be resolved; %s",
				errorName,
				host,
				providerEndpointRecovery(info),
			)
		}
		return fmt.Errorf("provider setup: %s endpoint host could not be resolved; %s", errorName, providerEndpointRecovery(info))
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("provider setup: %s endpoint refused the connection; %s", errorName, providerEndpointRecovery(info))
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return fmt.Errorf("provider setup: %s endpoint has no reachable network route; %s", errorName, providerEndpointRecovery(info))
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return fmt.Errorf("provider setup: %s validation timed out", errorName)
	}
	return fmt.Errorf("provider setup: %s validation unavailable; %s", errorName, providerEndpointRecovery(info))
}

func providerValidationHost(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func providerEndpointRecovery(info provider.Info) string {
	if info.Source != nil && info.Source.Kind == provider.CCSwitchSourceKind {
		if info.Source.Route == provider.CCSwitchRouteManagedProxy {
			return "start CC Switch or repair its active Codex proxy route"
		}
		return "check DNS/network or update the CC Switch profile endpoint"
	}
	return "check DNS/network and the provider endpoint configuration"
}
