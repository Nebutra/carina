package auth

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestBYOKKeyWinsOverOAuth(t *testing.T) {
	t.Setenv("CARINA_TEST_KEY", "sk-byok-123")
	oauthCalled := false
	chain := NewChain(
		EnvKey{Var: "CARINA_TEST_KEY"},
		NebutraOAuth{Token: func() (string, error) { oauthCalled = true; return "oauth-tok", nil }},
	)
	cred, ok := chain.Resolve()
	if !ok || cred.Kind != APIKey || cred.Value != "sk-byok-123" {
		t.Fatalf("BYOK key must win: %+v ok=%v", cred, ok)
	}
	if cred.Source != "env:CARINA_TEST_KEY" {
		t.Fatalf("source label wrong: %q", cred.Source)
	}
	if oauthCalled {
		t.Fatal("OAuth must not be consulted when a BYOK key is present")
	}
}

func TestOAuthFallbackWhenNoKey(t *testing.T) {
	os.Unsetenv("CARINA_TEST_KEY_ABSENT")
	chain := DefaultChain([]string{"CARINA_TEST_KEY_ABSENT"}, func() (string, error) {
		return "nebutra-access-token", nil
	})
	cred, ok := chain.Resolve()
	if !ok || cred.Kind != OAuth || cred.Value != "nebutra-access-token" {
		t.Fatalf("should fall back to Nebutra OAuth: %+v ok=%v", cred, ok)
	}
	if cred.Source != "nebutra-oauth" {
		t.Fatalf("oauth source label wrong: %q", cred.Source)
	}
}

func TestNoCredentialResolves(t *testing.T) {
	chain := DefaultChain([]string{"CARINA_DEFINITELY_UNSET_XYZ"}, nil)
	if _, ok := chain.Resolve(); ok {
		t.Fatal("with no key and no oauth, nothing should resolve")
	}
	if chain.ResolvedSource() != "" {
		t.Fatal("ResolvedSource must be empty when unresolved")
	}
}

func TestFileKeyReadsFirstLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	os.WriteFile(path, []byte("sk-from-file\nextra-ignored\n"), 0o600)
	cred, ok := FileKey{Path: path}.Resolve()
	if !ok || cred.Value != "sk-from-file" || cred.Kind != APIKey {
		t.Fatalf("file key first-line wrong: %+v", cred)
	}
	if _, ok := (FileKey{Path: filepath.Join(dir, "nope")}).Resolve(); ok {
		t.Fatal("missing file must not resolve")
	}
}

func TestApplySetsCorrectHeader(t *testing.T) {
	h := http.Header{}
	Credential{Kind: APIKey, Value: "sk-1"}.Apply(h)
	if h.Get("x-api-key") != "sk-1" || h.Get("Authorization") != "" {
		t.Fatalf("api_key must set x-api-key only: %v", h)
	}
	h2 := http.Header{}
	Credential{Kind: OAuth, Value: "tok"}.Apply(h2)
	if h2.Get("Authorization") != "Bearer tok" || h2.Get("x-api-key") != "" {
		t.Fatalf("oauth must set Authorization: Bearer only: %v", h2)
	}
}

func TestSourcesNeverLeakValues(t *testing.T) {
	t.Setenv("CARINA_SECRET_KEY", "sk-super-secret-xyz")
	chain := DefaultChain([]string{"CARINA_SECRET_KEY"}, func() (string, error) { return "tok", nil })
	// Source names and ResolvedSource must never contain the secret value.
	all := fmt.Sprint(chain.Sources()) + chain.ResolvedSource()
	if want := "sk-super-secret-xyz"; contains(all, want) {
		t.Fatal("source labels must never contain the secret value")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
