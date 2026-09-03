// Package netguard owns public-network URL and dialing validation shared by
// bounded web tools and browser egress.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

type Resolver interface {
	LookupIP(context.Context, string, string) ([]net.IP, error)
}

type defaultResolver struct{}

func (defaultResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, network, host)
}

func NormalizePublicHTTPSURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "#") {
		return nil, fmt.Errorf("URL fragments are not allowed")
	}
	target, err := url.ParseRequestURI(raw)
	if err != nil || target == nil || !target.IsAbs() {
		return nil, fmt.Errorf("absolute HTTPS URL required")
	}
	target.Scheme = strings.ToLower(target.Scheme)
	if target.Scheme != "https" {
		return nil, fmt.Errorf("only HTTPS URLs are allowed")
	}
	if target.User != nil || target.Hostname() == "" || target.Fragment != "" {
		return nil, fmt.Errorf("URL credentials, empty hosts, and fragments are not allowed")
	}
	if port := target.Port(); port != "" && port != "443" {
		return nil, fmt.Errorf("only the default HTTPS port is allowed")
	}
	host := strings.ToLower(strings.TrimSuffix(target.Hostname(), "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil, fmt.Errorf("local network targets are not allowed")
	}
	if net.ParseIP(host) != nil {
		return nil, fmt.Errorf("IP address targets are not allowed")
	}
	if !ValidDNSName(host) {
		return nil, fmt.Errorf("a valid public DNS hostname is required")
	}
	target.Host = host
	return target, nil
}

func NormalizePublicHTTPSOrigin(raw string) (string, error) {
	target, err := NormalizePublicHTTPSURL(raw)
	if err != nil {
		return "", err
	}
	if target.Path != "" && target.Path != "/" || target.RawQuery != "" || target.RawPath != "" {
		return "", fmt.Errorf("origin must not contain a path or query")
	}
	return "https://" + target.Hostname(), nil
}

func ValidDNSName(host string) bool {
	if host == "" || len(host) > 253 || !strings.Contains(host, ".") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' {
				return false
			}
		}
	}
	return true
}

func DialPublicContext(ctx context.Context, network, address string) (net.Conn, error) {
	return DialPublicContextWithResolver(ctx, network, address, defaultResolver{})
}

func DialPublicContextWithResolver(ctx context.Context, network, address string, resolver Resolver) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("public network address: %w", err)
	}
	if resolver == nil {
		return nil, fmt.Errorf("public network resolver is required")
	}
	if !ValidDNSName(strings.ToLower(strings.TrimSuffix(host, "."))) {
		return nil, fmt.Errorf("public DNS hostname required")
	}
	// The trailing dot prevents machine-specific DNS search suffix rewriting.
	addresses, err := resolver.LookupIP(ctx, "ip", strings.TrimSuffix(host, ".")+".")
	if err != nil {
		return nil, err
	}
	dialer := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, ip := range addresses {
		if !PublicIP(ip) {
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("host resolves only to local or private addresses")
}

func PublicIP(ip net.IP) bool {
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
}
