package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const AuthContentEnv = "CARINA_AUTH_CONTENT"

// StoredCredential is a user-managed provider credential. Values are sensitive.
type StoredCredential struct {
	Type     Kind              `json:"type"`
	Key      string            `json:"key,omitempty"`
	Access   string            `json:"access,omitempty"`
	Refresh  string            `json:"refresh,omitempty"`
	Expires  int64             `json:"expires,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// SafeCredential is safe to print: it never contains the secret value.
type SafeCredential struct {
	Provider string            `json:"provider"`
	Type     Kind              `json:"type"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Store persists user BYOK credentials under ~/.carina/auth.json.
type Store struct {
	Path string
}

func DefaultStorePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".carina", "auth.json"), nil
}

func NewStore(path string) (*Store, error) {
	if path == "" {
		var err error
		path, err = DefaultStorePath()
		if err != nil {
			return nil, err
		}
	}
	return &Store{Path: path}, nil
}

func (s *Store) All() (map[string]StoredCredential, error) {
	if raw := strings.TrimSpace(os.Getenv(AuthContentEnv)); raw != "" {
		return decodeStore([]byte(raw))
	}
	b, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return map[string]StoredCredential{}, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeStore(b)
}

func (s *Store) Get(provider string) (StoredCredential, bool, error) {
	all, err := s.All()
	if err != nil {
		return StoredCredential{}, false, err
	}
	c, ok := all[normalizeProvider(provider)]
	return c, ok, nil
}

func (s *Store) Set(provider string, cred StoredCredential) error {
	provider = normalizeProvider(provider)
	if provider == "" {
		return fmt.Errorf("auth store: provider required")
	}
	all, err := s.All()
	if err != nil {
		return err
	}
	all[provider] = cred
	return s.write(all)
}

func (s *Store) SetAPIKey(provider, key string, metadata map[string]string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("auth store: api key required")
	}
	return s.Set(provider, StoredCredential{Type: APIKey, Key: key, Metadata: metadata})
}

func (s *Store) Remove(provider string) error {
	all, err := s.All()
	if err != nil {
		return err
	}
	delete(all, normalizeProvider(provider))
	return s.write(all)
}

func (s *Store) ListSafe() ([]SafeCredential, error) {
	all, err := s.All()
	if err != nil {
		return nil, err
	}
	out := make([]SafeCredential, 0, len(all))
	for provider, cred := range all {
		out = append(out, SafeCredential{Provider: provider, Type: cred.Type, Metadata: cred.Metadata})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out, nil
}

func (s *Store) write(all map[string]StoredCredential) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", s.Path, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.Path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func decodeStore(b []byte) (map[string]StoredCredential, error) {
	var all map[string]StoredCredential
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	if all == nil {
		all = map[string]StoredCredential{}
	}
	normalized := make(map[string]StoredCredential, len(all))
	for provider, cred := range all {
		normalized[normalizeProvider(provider)] = cred
	}
	return normalized, nil
}

func normalizeProvider(provider string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(provider)), "/")
}

// StoreKey resolves a provider key from the user auth store.
type StoreKey struct {
	Store    *Store
	Provider string
}

func (s StoreKey) Name() string { return "auth:" + normalizeProvider(s.Provider) }

func (s StoreKey) Resolve() (Credential, bool) {
	if s.Store == nil {
		return Credential{}, false
	}
	cred, ok, err := s.Store.Get(s.Provider)
	if err != nil || !ok {
		return Credential{}, false
	}
	switch cred.Type {
	case OAuth:
		if strings.TrimSpace(cred.Access) == "" {
			return Credential{}, false
		}
		return Credential{Kind: OAuth, Value: strings.TrimSpace(cred.Access), Source: s.Name()}, true
	default:
		if strings.TrimSpace(cred.Key) == "" {
			return Credential{}, false
		}
		return Credential{Kind: APIKey, Value: strings.TrimSpace(cred.Key), Source: s.Name()}, true
	}
}
