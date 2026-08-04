package provider

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// claudeCodeOAuthLookup resolves a Claude Code subscription OAuth access
// token when a CC Switch Claude profile has no reusable API key. Tests
// override this to avoid depending on the operator keychain.
var claudeCodeOAuthLookup = lookupClaudeCodeOAuthToken

// lookupClaudeCodeOAuthToken reads Claude Code's macOS keychain item (and a
// small set of file fallbacks). The access token is an Anthropic OAuth
// bearer (sk-ant-oat01-…) accepted by api.anthropic.com with Authorization:
// Bearer. It is never logged.
func lookupClaudeCodeOAuthToken() (string, bool) {
	if tok, ok := lookupClaudeCodeOAuthFromKeychain(); ok {
		return tok, true
	}
	if tok, ok := lookupClaudeCodeOAuthFromFiles(); ok {
		return tok, true
	}
	return "", false
}

func lookupClaudeCodeOAuthFromKeychain() (string, bool) {
	if runtime.GOOS != "darwin" {
		return "", false
	}
	// Claude Code stores OAuth under this generic-password service name.
	for _, service := range []string{"Claude Code-credentials", "Claude Code"} {
		out, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
		if err != nil {
			continue
		}
		if tok, ok := parseClaudeCodeOAuthPayload(out); ok {
			return tok, true
		}
	}
	return "", false
}

func lookupClaudeCodeOAuthFromFiles() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	candidates := []string{
		filepath.Join(home, ".claude", ".credentials.json"),
		filepath.Join(home, ".claude", "credentials.json"),
		filepath.Join(home, ".config", "claude", "credentials.json"),
	}
	for _, path := range candidates {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		if tok, ok := parseClaudeCodeOAuthPayload(raw); ok {
			return tok, true
		}
	}
	return "", false
}

func parseClaudeCodeOAuthPayload(raw []byte) (string, bool) {
	raw = []byte(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return "", false
	}
	// Some dumps are a bare token string.
	if !json.Valid(raw) {
		tok := strings.TrimSpace(string(raw))
		if isClaudeCodeOAuthAccessToken(tok) {
			return tok, true
		}
		return "", false
	}
	var payload struct {
		ClaudeAiOauth *struct {
			AccessToken string `json:"accessToken"`
			ExpiresAt   int64  `json:"expiresAt"`
		} `json:"claudeAiOauth"`
		// Alternate shapes seen in file exports.
		AccessToken string `json:"accessToken"`
		Token       string `json:"token"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", false
	}
	if payload.ClaudeAiOauth != nil {
		tok := strings.TrimSpace(payload.ClaudeAiOauth.AccessToken)
		if isClaudeCodeOAuthAccessToken(tok) && !claudeOAuthExpired(payload.ClaudeAiOauth.ExpiresAt) {
			return tok, true
		}
	}
	for _, tok := range []string{payload.AccessToken, payload.Token} {
		tok = strings.TrimSpace(tok)
		if isClaudeCodeOAuthAccessToken(tok) {
			return tok, true
		}
	}
	return "", false
}

func isClaudeCodeOAuthAccessToken(tok string) bool {
	// Claude Code subscription OAuth access tokens use this prefix.
	return strings.HasPrefix(tok, "sk-ant-oat")
}

func claudeOAuthExpired(expiresAt int64) bool {
	if expiresAt <= 0 {
		return false
	}
	// Claude stores expiresAt in unix milliseconds.
	if expiresAt > 1_000_000_000_000 {
		return time.Now().UnixMilli() >= expiresAt
	}
	return time.Now().Unix() >= expiresAt
}

func isLoopbackBaseURL(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return false
	}
	// Strip scheme.
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
	}
	host := raw
	if i := strings.IndexAny(host, "/:"); i >= 0 {
		host = host[:i]
	}
	switch host {
	case "127.0.0.1", "localhost", "0.0.0.0", "::1", "[::1]":
		return true
	default:
		return strings.HasPrefix(host, "127.")
	}
}
