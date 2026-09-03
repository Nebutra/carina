package netguard

import (
	"context"
	"net"
	"strings"
	"testing"
)

type staticResolver []net.IP

func (r staticResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return append([]net.IP(nil), r...), nil
}

func TestNormalizePublicHTTPSURL(t *testing.T) {
	target, err := NormalizePublicHTTPSURL(" HTTPS://Example.COM./path?q=1 ")
	if err != nil {
		t.Fatal(err)
	}
	if target.String() != "https://example.com/path?q=1" {
		t.Fatalf("normalized URL = %q", target)
	}
	for _, raw := range []string{
		"http://example.com", "https://127.0.0.1", "https://localhost",
		"https://example.com:444", "https://user@example.com", "https://example.com/#fragment",
	} {
		if _, err := NormalizePublicHTTPSURL(raw); err == nil {
			t.Fatalf("unsafe URL accepted: %s", raw)
		}
	}
}

func TestPublicIPRejectsSpecialRangesAndMappedAddresses(t *testing.T) {
	for _, raw := range []string{
		"127.0.0.1", "10.0.0.1", "100.64.0.1", "169.254.1.1", "192.0.2.1",
		"198.18.0.1", "203.0.113.1", "::1", "fc00::1", "fe80::1", "2001:db8::1",
		"::ffff:127.0.0.1",
	} {
		if PublicIP(net.ParseIP(raw)) {
			t.Fatalf("non-public address accepted: %s", raw)
		}
	}
	if !PublicIP(net.ParseIP("8.8.8.8")) || !PublicIP(net.ParseIP("2606:4700:4700::1111")) {
		t.Fatal("public addresses were rejected")
	}
}

func TestPinnedDialRejectsDNSRebindingToPrivateIP(t *testing.T) {
	_, err := DialPublicContextWithResolver(context.Background(), "tcp", "example.com:443", staticResolver{net.ParseIP("127.0.0.1")})
	if err == nil || !strings.Contains(err.Error(), "local or private") {
		t.Fatalf("private resolution was not blocked: %v", err)
	}
}
