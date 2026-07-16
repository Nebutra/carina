package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

func lastSessionPath(stateDir, workspaceRoot string) string {
	sum := sha256.Sum256([]byte(cleanWorkspaceRoot(workspaceRoot)))
	return filepath.Join(stateDir, "tui-sessions", hex.EncodeToString(sum[:])+".session")
}

func LastActiveSession(stateDir, workspaceRoot string) (string, error) {
	if stateDir == "" || workspaceRoot == "" {
		return "", nil
	}
	raw, err := os.ReadFile(lastSessionPath(stateDir, workspaceRoot))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func persistLastActiveSession(stateDir, workspaceRoot, sessionID string) error {
	if stateDir == "" || workspaceRoot == "" || sessionID == "" {
		return nil
	}
	path := lastSessionPath(stateDir, workspaceRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sessionID+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
