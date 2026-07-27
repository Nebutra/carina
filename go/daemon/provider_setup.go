package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/provider"
)

const providerValidationTimeout = 15 * time.Second

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
	providerID = normalizeProviderID(providerID)
	key = strings.TrimSpace(key)
	if providerID == "" || key == "" {
		return ProviderSetupResult{}, fmt.Errorf("provider setup: provider and API key are required")
	}
	info, ok := s.Catalog[providerID]
	if !ok {
		return ProviderSetupResult{}, fmt.Errorf("provider setup: unsupported provider %q", providerID)
	}
	info.ID = providerID
	if err := s.validateAPIKey(ctx, info, key); err != nil {
		return ProviderSetupResult{}, err
	}
	if s.Store == nil {
		return ProviderSetupResult{}, fmt.Errorf("provider setup: credential store is unavailable")
	}
	if err := s.Store.SetAPIKey(providerID, key, nil); err != nil {
		return ProviderSetupResult{}, fmt.Errorf("provider setup: persist %s credential: %w", providerID, err)
	}
	return ProviderSetupResult{Provider: providerID, Source: "auth:" + providerID}, nil
}

func (s ProviderSetupService) validateAPIKey(parent context.Context, info provider.Info, key string) error {
	protocol := detectRuntimeProtocol(info)
	if protocol == protocolUnsupported {
		return fmt.Errorf("provider setup: %s is not supported by the runtime", info.ID)
	}
	baseURL, ok := runtimeBaseURL(info)
	if !ok || strings.TrimSpace(baseURL) == "" {
		return fmt.Errorf("provider setup: %s has no usable endpoint", info.ID)
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/models"
	if protocol == protocolAnthropic {
		endpoint += "?limit=1"
	} else if protocol == protocolGemini {
		endpoint += "?pageSize=1"
	}
	if _, err := url.ParseRequestURI(endpoint); err != nil {
		return fmt.Errorf("provider setup: %s endpoint is invalid", info.ID)
	}
	ctx, cancel := context.WithTimeout(parent, providerValidationTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("provider setup: prepare %s validation: %w", info.ID, err)
	}
	switch protocol {
	case protocolAnthropic:
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	case protocolGemini:
		req.Header.Set("x-goog-api-key", key)
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := s.HTTP
	if client == nil {
		client = &http.Client{Timeout: providerValidationTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return fmt.Errorf("provider setup: %s validation canceled", info.ID)
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("provider setup: %s validation timed out", info.ID)
		default:
			return fmt.Errorf("provider setup: %s validation unavailable", info.ID)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return nil
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("provider setup: %s rejected the credential (status %d)", info.ID, resp.StatusCode)
	default:
		return fmt.Errorf("provider setup: %s validation unavailable (status %d)", info.ID, resp.StatusCode)
	}
}
