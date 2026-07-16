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
	Event     string   `json:"event"`   // PreToolUse | PostToolUse | SessionStart | SessionEnd | Stop
	Matcher   string   `json:"matcher"` // tool name, or "" / "*" for all tools
	Command   []string `json:"command"` // argv; receives the action JSON on stdin
	TimeoutMS int      `json:"timeout_ms,omitempty"`
	Source    string   `json:"-"`
}

type hookOutcome struct {
	Event      string `json:"event"`
	Matcher    string `json:"matcher,omitempty"`
	Source     string `json:"source"`
	OK         bool   `json:"ok"`
	ExitCode   int    `json:"exit_code"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Error      string `json:"error,omitempty"`
	LastRunAt  string `json:"last_run_at"`
	DurationMS int64  `json:"duration_ms"`
}

func (h HookSpec) matches(event, tool string) bool {
	return h.Event == event && (h.Matcher == "" || h.Matcher == "*" || h.Matcher == tool)
}

func loadHooks(ws string) []HookSpec {
	var out []HookSpec
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, hooksWithSource(readHooks(filepath.Join(home, ".carina", "hooks.json")), "user")...)
	}
	if ws != "" {
		out = append(out, hooksWithSource(readHooks(filepath.Join(ws, ".carina", "hooks.json")), "project")...)
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
	for _, h := range hs {
		if !validHookEvent(h.Event) || len(h.Command) == 0 || strings.TrimSpace(h.Command[0]) == "" || h.TimeoutMS < 0 || h.TimeoutMS > 60000 {
			return nil
		}
	}
	return hs
}

func hooksWithSource(hooks []HookSpec, source string) []HookSpec {
	for i := range hooks {
		hooks[i].Source = source
	}
	return hooks
}

func validHookEvent(event string) bool {
	switch event {
	case "PreToolUse", "PostToolUse", "SessionStart", "SessionEnd", "Stop":
		return true
	}
	return false
}

func (h HookSpec) timeout() time.Duration {
	if h.TimeoutMS == 0 {
		return 10 * time.Second
	}
	return time.Duration(h.TimeoutMS) * time.Millisecond
}

func hookKey(h HookSpec) string { return h.Source + "\x00" + h.Event + "\x00" + h.Matcher }

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
		code, stderr, timedOut, elapsed := runHookCommand(h.Command, payload, h.timeout())
		d.recordHookOutcome(h, code, stderr, timedOut, elapsed)
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
		code, stderr, timedOut, elapsed := runHookCommand(h.Command, payload, h.timeout())
		d.recordHookOutcome(h, code, stderr, timedOut, elapsed)
	}
}

func (d *Daemon) runLifecycleHooks(ws, event string, payload map[string]any) {
	if d.safeMode {
		return
	}
	raw, _ := json.Marshal(payload)
	for _, h := range loadHooks(ws) {
		if !h.matches(event, "") {
			continue
		}
		code, stderr, timedOut, elapsed := runHookCommand(h.Command, raw, h.timeout())
		d.recordHookOutcome(h, code, stderr, timedOut, elapsed)
	}
}

func (d *Daemon) recordHookOutcome(h HookSpec, code int, stderr string, timedOut bool, elapsed time.Duration) {
	outcome := hookOutcome{Event: h.Event, Matcher: h.Matcher, Source: h.Source, OK: code == 0, ExitCode: code, TimedOut: timedOut, LastRunAt: time.Now().UTC().Format(time.RFC3339Nano), DurationMS: elapsed.Milliseconds()}
	if code != 0 {
		outcome.Error = strings.TrimSpace(stderr)
	}
	d.hookOutcomeMu.Lock()
	d.hookOutcomes[hookKey(h)] = outcome
	d.hookOutcomeMu.Unlock()
}

func (d *Daemon) hookOutcome(h HookSpec) (hookOutcome, bool) {
	d.hookOutcomeMu.Lock()
	defer d.hookOutcomeMu.Unlock()
	o, ok := d.hookOutcomes[hookKey(h)]
	return o, ok
}

func terminalHookTaskStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "degraded", "denied":
		return true
	}
	return false
}

func runHookCommand(argv []string, stdin []byte, timeout time.Duration) (int, string, bool, time.Duration) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		timedOut := ctx.Err() == context.DeadlineExceeded
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), stderr.String(), timedOut, time.Since(started)
		}
		return -1, err.Error(), timedOut, time.Since(started)
	}
	return 0, stderr.String(), false, time.Since(started)
}
