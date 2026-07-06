package egress

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// echoAuth is an upstream that echoes back the Authorization header it received.
func echoAuth() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "auth="+r.Header.Get("Authorization"))
	}))
}

// proxyClient returns an http.Client whose requests go through the proxy.
func proxyClient(t *testing.T, proxyURL string) *http.Client {
	t.Helper()
	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

func getVia(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestEgressInjectsCredentialForAllowlistedHost(t *testing.T) {
	up := echoAuth()
	defer up.Close()
	host := hostOnly(strings.TrimPrefix(up.URL, "http://"))

	rules := map[string]InjectionRule{
		host: {Header: "Authorization", ValuePrefix: "Bearer ", SecretName: "TOK"},
	}
	resolve := func(name string) (string, bool) {
		if name == "TOK" {
			return "s3cr3t", true
		}
		return "", false
	}
	p := NewWithInjector(Allowlist([]string{host}), NewInjector(rules, resolve))
	proxyURL, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	code, body := getVia(t, proxyClient(t, proxyURL), up.URL)
	if code != 200 {
		t.Fatalf("expected 200, got %d", code)
	}
	if body != "auth=Bearer s3cr3t" {
		t.Fatalf("credential not injected at the boundary, upstream saw: %q", body)
	}
}

func TestEgressNoInjectionWithoutRule(t *testing.T) {
	up := echoAuth()
	defer up.Close()
	host := hostOnly(strings.TrimPrefix(up.URL, "http://"))

	// Host is allowlisted but has no injection rule.
	p := NewWithInjector(Allowlist([]string{host}), NewInjector(map[string]InjectionRule{}, func(string) (string, bool) { return "", false }))
	proxyURL, _ := p.Start()
	defer p.Close()

	_, body := getVia(t, proxyClient(t, proxyURL), up.URL)
	if body != "auth=" {
		t.Fatalf("host without a rule must not be authenticated, upstream saw: %q", body)
	}
}

func TestEgressMissingSecretForwardsUnauthenticated(t *testing.T) {
	up := echoAuth()
	defer up.Close()
	host := hostOnly(strings.TrimPrefix(up.URL, "http://"))

	rules := map[string]InjectionRule{host: {SecretName: "TOK"}}
	// Resolver never finds the secret.
	p := NewWithInjector(Allowlist([]string{host}), NewInjector(rules, func(string) (string, bool) { return "", false }))
	proxyURL, _ := p.Start()
	defer p.Close()

	code, body := getVia(t, proxyClient(t, proxyURL), up.URL)
	if code != 200 || body != "auth=" {
		t.Fatalf("missing secret should forward unauthenticated (200, no header), got %d %q", code, body)
	}
}

func TestEgressDeniesUnlistedHostWithoutLeakingSecret(t *testing.T) {
	up := echoAuth()
	defer up.Close()
	host := hostOnly(strings.TrimPrefix(up.URL, "http://"))

	// The upstream host is NOT on the allowlist; a rule exists but must never fire.
	rules := map[string]InjectionRule{host: {SecretName: "TOK"}}
	p := NewWithInjector(Allowlist(nil), NewInjector(rules, func(string) (string, bool) { return "s3cr3t", true }))
	proxyURL, _ := p.Start()
	defer p.Close()

	code, body := getVia(t, proxyClient(t, proxyURL), up.URL)
	if code != http.StatusForbidden {
		t.Fatalf("unlisted host must be denied, got %d", code)
	}
	if strings.Contains(body, "s3cr3t") {
		t.Fatal("denial response must never contain the secret value")
	}
}
