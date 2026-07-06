package egress

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestEgressGate: an allowlisted host is proxied through; a non-allowlisted host
// is refused with 403 before any connection is made.
func TestEgressGate(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello-upstream")
	}))
	defer up.Close()
	upHost := hostOnly(up.Listener.Addr().String())

	p := New(Allowlist([]string{upHost}))
	addr, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	proxyURL, _ := url.Parse(addr)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}

	// Allowlisted host is proxied through.
	resp, err := client.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "hello-upstream") {
		t.Fatalf("allowlisted host should pass, got %q", body)
	}

	// Non-allowlisted host is refused.
	resp2, err := client.Get("http://denied.invalid/")
	if err != nil {
		t.Fatalf("proxy should respond (not error), got %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("denied host should be 403, got %d", resp2.StatusCode)
	}
}
