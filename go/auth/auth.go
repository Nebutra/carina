// Package auth resolves provider credentials from an ordered chain of sources.
// BYOK (bring-your-own-key) API-key sources take precedence; a Nebutra-ecosystem
// OAuth token is the fallback. The first source that yields a credential wins, so
// a user-supplied key always overrides the managed OAuth path.
//
// Credential values are sensitive: only source NAMES (env:VAR, config,
// nebutra-oauth) are safe to log — never the value.
package auth

import (
	"net/http"
	"os"
	"strings"
)

// Kind distinguishes how a credential authenticates a request.
type Kind string

const (
	APIKey Kind = "api_key" // BYOK -> x-api-key header
	OAuth  Kind = "oauth"   // Nebutra ecosystem -> Authorization: Bearer
)

// Credential is a resolved auth secret. Value is sensitive; Source is a safe-to-
// log provenance label.
type Credential struct {
	Kind   Kind
	Value  string
	Source string
}

// Apply sets the appropriate auth header for the credential. It never logs the
// value.
func (c Credential) Apply(h http.Header) {
	switch c.Kind {
	case OAuth:
		h.Set("Authorization", "Bearer "+c.Value)
	default: // APIKey
		h.Set("x-api-key", c.Value)
	}
}

// Source resolves a credential; ok=false when it has none.
type Source interface {
	Resolve() (Credential, bool)
	Name() string
}

// Chain resolves from an ordered list of sources: the first non-empty wins.
type Chain struct {
	sources []Source
}

// NewChain builds a resolver over sources in priority order.
func NewChain(sources ...Source) *Chain { return &Chain{sources: sources} }

// DefaultChain builds Carina's standard resolver: BYOK API keys first (the given
// env vars, in order), then the Nebutra-ecosystem OAuth fallback (a nil token
// func is skipped). This encodes the policy "if the user brings a key, use it;
// otherwise fall back to the managed Nebutra identity".
func DefaultChain(envVars []string, oauth TokenFunc) *Chain {
	sources := make([]Source, 0, len(envVars)+1)
	for _, v := range envVars {
		sources = append(sources, EnvKey{Var: v})
	}
	if oauth != nil {
		sources = append(sources, NebutraOAuth{Token: oauth})
	}
	return NewChain(sources...)
}

// Resolve returns the first credential a source yields.
func (c *Chain) Resolve() (Credential, bool) {
	for _, s := range c.sources {
		if cred, ok := s.Resolve(); ok && cred.Value != "" {
			return cred, true
		}
	}
	return Credential{}, false
}

// Sources lists the source names in priority order (safe to log — no secrets).
func (c *Chain) Sources() []string {
	names := make([]string, len(c.sources))
	for i, s := range c.sources {
		names[i] = s.Name()
	}
	return names
}

// ResolvedSource returns the name of the source that currently resolves (or ""),
// for observability without exposing the value.
func (c *Chain) ResolvedSource() string {
	if cred, ok := c.Resolve(); ok {
		return cred.Source
	}
	return ""
}

// EnvKey is a BYOK API key from an environment variable.
type EnvKey struct{ Var string }

func (e EnvKey) Name() string { return "env:" + e.Var }
func (e EnvKey) Resolve() (Credential, bool) {
	v := strings.TrimSpace(os.Getenv(e.Var))
	if v == "" {
		return Credential{}, false
	}
	return Credential{Kind: APIKey, Value: v, Source: e.Name()}, true
}

// StaticKey is a BYOK API key supplied inline (e.g. from a config file).
type StaticKey struct {
	Value string
	From  string // provenance label, e.g. "config"
}

func (s StaticKey) Name() string {
	if s.From != "" {
		return s.From
	}
	return "static"
}
func (s StaticKey) Resolve() (Credential, bool) {
	v := strings.TrimSpace(s.Value)
	if v == "" {
		return Credential{}, false
	}
	return Credential{Kind: APIKey, Value: v, Source: s.Name()}, true
}

// FileKey is a BYOK API key read from the first line of a file (e.g. a secret
// mount).
type FileKey struct{ Path string }

func (f FileKey) Name() string { return "file:" + f.Path }
func (f FileKey) Resolve() (Credential, bool) {
	b, err := os.ReadFile(f.Path)
	if err != nil {
		return Credential{}, false
	}
	v := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
	if v == "" {
		return Credential{}, false
	}
	return Credential{Kind: APIKey, Value: v, Source: f.Name()}, true
}

// TokenFunc supplies an OAuth access token (refresh handled by the provider).
// Returning ("", nil) or an error means unauthenticated.
type TokenFunc func() (string, error)

// NebutraOAuth is the Nebutra-ecosystem OAuth source: it yields a Bearer token
// from the identity service. It is the lowest-priority fallback, so an explicit
// BYOK key always wins.
type NebutraOAuth struct{ Token TokenFunc }

func (n NebutraOAuth) Name() string { return "nebutra-oauth" }
func (n NebutraOAuth) Resolve() (Credential, bool) {
	if n.Token == nil {
		return Credential{}, false
	}
	tok, err := n.Token()
	if err != nil || strings.TrimSpace(tok) == "" {
		return Credential{}, false
	}
	return Credential{Kind: OAuth, Value: tok, Source: n.Name()}, true
}
