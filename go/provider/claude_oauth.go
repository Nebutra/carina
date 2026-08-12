package provider

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const claudeCodeKeychainProbeTimeout = 2 * time.Second

// claudeCodeOAuthSessionLookup reports whether Claude Code owns a renewable
// local OAuth session even when its current access token needs refresh. The
// owning CLI performs refresh; Carina uses this only for route readiness.
var claudeCodeOAuthSessionLookup = lookupClaudeCodeOAuthSession

func lookupClaudeCodeOAuthSession() bool {
	if runtime.GOOS == "darwin" {
		ctx, cancel := context.WithTimeout(context.Background(), claudeCodeKeychainProbeTimeout)
		defer cancel()
		if lookupClaudeCodeKeychainSession(ctx, "security") {
			return true
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, path := range []string{
		filepath.Join(home, ".claude", ".credentials.json"),
		filepath.Join(home, ".claude", "credentials.json"),
		filepath.Join(home, ".config", "claude", "credentials.json"),
	} {
		raw, err := os.ReadFile(path)
		if err == nil && parseClaudeCodeOAuthSession(raw) {
			return true
		}
	}
	return false
}

func lookupClaudeCodeKeychainSession(ctx context.Context, securityBin string) bool {
	for _, service := range []string{"Claude Code-credentials", "Claude Code"} {
		// Presence is sufficient. Never pass -w: Carina delegates authentication
		// to Claude Code and does not need to read the keychain secret itself.
		if exec.CommandContext(ctx, securityBin, "find-generic-password", "-s", service).Run() == nil {
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
	return false
}

func parseClaudeCodeOAuthSession(raw []byte) bool {
	var payload struct {
		ClaudeAiOauth *struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
		} `json:"claudeAiOauth"`
		AccessToken string `json:"accessToken"`
		Token       string `json:"token"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &payload) != nil {
		return false
	}
	if payload.ClaudeAiOauth != nil {
		return isClaudeCodeOAuthAccessToken(strings.TrimSpace(payload.ClaudeAiOauth.AccessToken)) ||
			strings.TrimSpace(payload.ClaudeAiOauth.RefreshToken) != ""
	}
	return isClaudeCodeOAuthAccessToken(strings.TrimSpace(payload.AccessToken)) ||
		isClaudeCodeOAuthAccessToken(strings.TrimSpace(payload.Token))
}

func isClaudeCodeOAuthAccessToken(tok string) bool {
	// Claude Code subscription OAuth access tokens use this prefix.
	return strings.HasPrefix(tok, "sk-ant-oat")
}
