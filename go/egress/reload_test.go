package egress

import (
	"net/http"
	"strings"
	"testing"
)

// TestProxySetGateLive proves the allowlist can be swapped live (config
// hot-reload) without restarting the listener: the same proxy address goes from
// deny-all to allowing a host after SetGate.
func TestProxySetGateLive(t *testing.T) {
	up := echoAuth()
	defer up.Close()
	host := hostOnly(strings.TrimPrefix(up.URL, "http://"))

	p := New(Allowlist(nil)) // deny everything
	proxyURL, err := p.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := proxyClient(t, proxyURL)

	if code, _ := getVia(t, c, up.URL); code != http.StatusForbidden {
		t.Fatalf("deny-all gate should 403, got %d", code)
	}

	// Swap the gate live; the listener (and proxy address) is unchanged.
	p.SetGate(Allowlist([]string{host}))
	if code, _ := getVia(t, c, up.URL); code != 200 {
		t.Fatalf("after live SetGate allow, expected 200, got %d", code)
	}
}
