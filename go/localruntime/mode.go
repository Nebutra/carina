package localruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const runtimeModeEnv = "CARINA_RUNTIME_MODE"

var ErrModeDecisionRequired = errors.New("localruntime: runtime mode decision required")

type modeDecision struct {
	Version   int       `json:"version"`
	Mode      Mode      `json:"mode"`
	UpdatedAt time.Time `json:"updated_at"`
}

func modeDecisionPath(home string) string {
	return filepath.Join(home, ".carina", "runtime-mode.json")
}

// ResolveMode applies the explicit environment override, then the persisted
// user decision, and otherwise defaults fresh behavior to workspace mode.
func ResolveMode(home string) (Mode, error) {
	if raw := strings.TrimSpace(os.Getenv(runtimeModeEnv)); raw != "" {
		mode := Mode(strings.ToLower(raw))
		if mode != ModeWorkspace && mode != ModeLegacy {
			return "", fmt.Errorf("localruntime: %s must be workspace or legacy", runtimeModeEnv)
		}
		return mode, nil
	}
	var decision modeDecision
	if err := readPrivateJSON(modeDecisionPath(home), &decision); err == nil {
		if decision.Version != 1 || (decision.Mode != ModeWorkspace && decision.Mode != ModeLegacy) {
			return "", fmt.Errorf("localruntime: invalid runtime mode decision")
		}
		return decision.Mode, nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if LegacyStatePresent(home) {
		return "", fmt.Errorf("%w: legacy global state exists; choose `carina runtime mode workspace` for isolated workspace state or `carina runtime mode legacy` to keep using it", ErrModeDecisionRequired)
	}
	return ModeWorkspace, nil
}

// WriteMode persists the reversible workspace/legacy compatibility choice.
func WriteMode(home string, mode Mode) error {
	if mode != ModeWorkspace && mode != ModeLegacy {
		return fmt.Errorf("localruntime: runtime mode must be workspace or legacy")
	}
	return writePrivateJSONAtomic(modeDecisionPath(home), modeDecision{
		Version: 1, Mode: mode, UpdatedAt: time.Now().UTC(),
	})
}

// LegacyStatePresent reports legacy global runtime artifacts without mutating
// or opening them.
func LegacyStatePresent(home string) bool {
	base := filepath.Join(home, ".carina")
	for _, path := range []string{
		filepath.Join(base, "state"), filepath.Join(base, "daemon.sock"),
		filepath.Join(base, "daemon.pid.json"),
	} {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return false
}

// MarshalModeStatus provides a stable diagnostic shape for CLI output.
func MarshalModeStatus(home string, mode Mode) ([]byte, error) {
	return json.Marshal(map[string]any{"mode": mode, "legacy_state_present": LegacyStatePresent(home)})
}
