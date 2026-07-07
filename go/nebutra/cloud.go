// Package nebutra contains integration boundary definitions for Nebutra-owned
// product services. Carina remains the local runtime; identity and sync belong
// behind this boundary.
package nebutra

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	DefaultCloudEndpoint = "https://nebutra.com"

	SyncModeOff = "off"
)

// NormalizeCloudEndpoint validates the Nebutra Cloud endpoint. Production
// endpoints must use HTTPS; localhost HTTP is allowed for connector development.
func NormalizeCloudEndpoint(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	if endpoint == "" {
		endpoint = DefaultCloudEndpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("nebutra_cloud_endpoint must be an absolute URL")
	}
	if u.Scheme != "https" && !isLocalHTTP(u) {
		return "", fmt.Errorf("nebutra_cloud_endpoint must use https outside localhost")
	}
	return endpoint, nil
}

// NormalizeSyncMode keeps multi-endpoint sync explicitly off in the source-first
// runtime. Future Nebutra connectors can add metadata/audit modes here without
// changing Carina's local authority model.
func NormalizeSyncMode(raw string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(raw))
	if mode == "" {
		mode = SyncModeOff
	}
	if mode != SyncModeOff {
		return "", fmt.Errorf("nebutra_sync_mode currently supports only %q", SyncModeOff)
	}
	return mode, nil
}

func isLocalHTTP(u *url.URL) bool {
	if u.Scheme != "http" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
