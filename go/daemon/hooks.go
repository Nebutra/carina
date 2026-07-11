package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// HookSpec is one lifecycle hook: an operator-defined command run around a tool
// action. A PreToolUse hook that exits 2 BLOCKS the action (its stderr becomes
// the feedback the agent sees); PostToolUse hooks observe the result. Hooks are
// loaded from ~/.carina/hooks.json and <workspace>/.carina/hooks.json.
type HookSpec struct {
	Event   string   `json:"event"`   // PreToolUse | PostToolUse
	Matcher string   `json:"matcher"` // tool name, or "" / "*" for all tools
	Command []string `json:"command"` // argv; receives the action JSON on stdin
}

func (h HookSpec) matches(event, tool string) bool {
	return h.Event == event && (h.Matcher == "" || h.Matcher == "*" || h.Matcher == tool)
}

func loadHooks(ws string) []HookSpec {
	var out []HookSpec
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, readHooks(filepath.Join(home, ".carina", "hooks.json"))...)
	}
	if ws != "" {
		out = append(out, readHooks(filepath.Join(ws, ".carina", "hooks.json"))...)
	}
	return out
}

func readHooks(path string) []HookSpec {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var hs []HookSpec
	if decodeStrictJSON(raw, &hs) != nil {
		return nil
	}
	return hs
}

// hookPayload serializes a tool action (and optionally its result) as the JSON
// fed to a hook command on stdin.
func hookPayload(act *action, result string) []byte {
	m := map[string]any{
		"tool":    act.Tool,
		"path":    act.Path,
		"pattern": act.Pattern,
		"command": act.Command,
	}
	if result != "" {
		m["result"] = truncate(result, 2000)
	}
	b, _ := json.Marshal(m)
	return b
}

// runPreToolHooks runs matching PreToolUse hooks; if any exits 2 the action is
// blocked and the hook's stderr is returned as the reason.
func (d *Daemon) runPreToolHooks(ws, tool string, payload []byte) (bool, string) {
	if d.safeMode {
		return false, ""
	}
	for _, h := range loadHooks(ws) {
		if !h.matches("PreToolUse", tool) || len(h.Command) == 0 {
			continue
		}
		code, stderr := runHookCommand(h.Command, payload)
		if code == 2 {
			reason := strings.TrimSpace(stderr)
			if reason == "" {
				reason = "denied by PreToolUse hook"
			}
			return true, reason
		}
	}
	return false, ""
}

// runPostToolHooks runs matching PostToolUse hooks (observe-only).
func (d *Daemon) runPostToolHooks(ws, tool string, payload []byte) {
	if d.safeMode {
		return
	}
	for _, h := range loadHooks(ws) {
		if !h.matches("PostToolUse", tool) || len(h.Command) == 0 {
			continue
		}
		_, _ = runHookCommand(h.Command, payload)
	}
}

func runHookCommand(argv []string, stdin []byte) (int, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), stderr.String()
		}
		return -1, err.Error()
	}
	return 0, stderr.String()
}
