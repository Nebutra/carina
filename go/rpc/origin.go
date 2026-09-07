package rpc

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// NormalizeGatewayOrigin validates browser origins accepted by a local
// Harness Gateway. Packaged Tauri and loopback HTTP dev origins are allowed;
// credentials, paths, wildcards, and public hosts are rejected.
func NormalizeGatewayOrigin(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "*\r\n") {
		return "", fmt.Errorf("gateway origin is required and must not contain wildcards")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid gateway origin %q", raw)
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if scheme == "tauri" && host == "localhost" && u.Port() == "" {
		return "tauri://localhost", nil
	}
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("unsupported gateway origin scheme %q", u.Scheme)
	}
	if host == "tauri.localhost" {
		return scheme + "://tauri.localhost" + formatOriginPort(u), nil
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return "", fmt.Errorf("gateway origin must use a loopback host")
		}
	}
	hostValue := host
	if strings.Contains(host, ":") {
		hostValue = "[" + host + "]"
	}
	return scheme + "://" + hostValue + formatOriginPort(u), nil
}

func formatOriginPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return ":" + p
	}
	return ""
}
