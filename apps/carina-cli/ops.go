package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const daemonOwnershipMarker = "carina-cli/v1"

type daemonOwnershipRecord struct {
	Owner      string    `json:"owner"`
	PID        int       `json:"pid"`
	Socket     string    `json:"socket"`
	Executable string    `json:"executable,omitempty"`
	StartedAt  time.Time `json:"started_at"`
}

type daemonStatus struct {
	PID int `json:"pid"`
}

var daemonStatusHook = func(socket string) (daemonStatus, error) {
	c, err := dialSocketHook(socket)
	if err != nil {
		return daemonStatus{}, err
	}
	defer c.Close()
	var status daemonStatus
	if err := c.Call("daemon.status", map[string]any{}, &status); err != nil {
		return daemonStatus{}, err
	}
	return status, nil
}

var signalDaemonHook = func(pid int, signal os.Signal) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(signal)
}

func cmdFork(c *rpcClient, args []string) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return fmt.Errorf("usage: carina fork <session_id>")
	}
	return call(c, "session.fork", map[string]any{"session_id": strings.TrimSpace(args[0])})
}

type usageCostTotals struct {
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

type usageCostProvider struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	usageCostTotals
	PricingKnown bool `json:"pricing_known"`
}

type usageCostResult struct {
	Providers []usageCostProvider `json:"providers"`
	Totals    usageCostTotals     `json:"totals"`
	Estimated bool                `json:"estimated"`
}

func cmdCost(c *rpcClient, args []string) error {
	params := map[string]any{}
	jsonOutput := false
	for _, arg := range args {
		switch arg {
		case "--json":
			jsonOutput = true
		default:
			if strings.TrimSpace(arg) == "" || params["session_id"] != nil {
				return fmt.Errorf("usage: carina cost [session_id] [--json]")
			}
			params["session_id"] = strings.TrimSpace(arg)
		}
	}

	var raw json.RawMessage
	if err := c.Call("usage.cost", params, &raw); err != nil {
		return err
	}
	if jsonOutput {
		return printJSON(raw)
	}
	var result usageCostResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("usage.cost returned invalid data: %w", err)
	}
	renderUsageCost(os.Stdout, result)
	return nil
}

func renderUsageCost(w io.Writer, result usageCostResult) {
	fmt.Fprintf(w, "estimated: %t\n", result.Estimated)
	fmt.Fprintf(w, "totals: input=%d output=%d cache_read=%d cache_write=%d cost_usd=%.6f\n",
		result.Totals.InputTokens,
		result.Totals.OutputTokens,
		result.Totals.CacheReadTokens,
		result.Totals.CacheWriteTokens,
		result.Totals.CostUSD,
	)
	if len(result.Providers) == 0 {
		fmt.Fprintln(w, "providers: none")
		return
	}
	fmt.Fprintln(w, "providers:")
	for _, provider := range result.Providers {
		pricing := "unknown"
		if provider.PricingKnown {
			pricing = "known"
		}
		fmt.Fprintf(w, "  %s/%s input=%d output=%d cache_read=%d cache_write=%d cost_usd=%.6f pricing=%s\n",
			provider.Provider,
			provider.Model,
			provider.InputTokens,
			provider.OutputTokens,
			provider.CacheReadTokens,
			provider.CacheWriteTokens,
			provider.CostUSD,
			pricing,
		)
	}
}

func cmdWorker(c *rpcClient, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: carina worker <list|register|heartbeat|revoke> ...\n  carina worker register <name> [remote|ci] [--pool <tag>]...")
	}
	switch args[0] {
	case "list", "ls":
		if len(args) != 1 {
			return fmt.Errorf("usage: carina worker list")
		}
		return call(c, "worker.list", map[string]any{})
	case "register":
		return cmdWorkerRegister(c, args[1:])
	case "heartbeat":
		return workerCredentialCall(c, args, "heartbeat", "worker.heartbeat")
	case "revoke":
		return workerCredentialCall(c, args, "revoke", "worker.revoke")
	default:
		return fmt.Errorf("unknown worker subcommand %q", args[0])
	}
}

// cmdWorkerRegister parses `carina worker register <name> [remote|ci] [--pool
// <tag>]...` — --pool is repeatable and matches a streaming workflow step's
// `"affinity":{"worker_pool":"<tag>"}` (go/daemon/workflow_remote.go); the
// daemon (handleWorkerRegister) is the authoritative validator for tag
// charset/length/count, this is just argument parsing.
func cmdWorkerRegister(c *rpcClient, args []string) error {
	var name, kind string
	var pools []string
	positional := make([]string, 0, 2)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--pool":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return fmt.Errorf("--pool requires a tag")
			}
			pools = append(pools, strings.TrimSpace(args[i+1]))
			i++
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) < 1 || len(positional) > 2 || strings.TrimSpace(positional[0]) == "" {
		return fmt.Errorf("usage: carina worker register <name> [remote|ci] [--pool <tag>]...")
	}
	name = strings.TrimSpace(positional[0])
	kind = "remote"
	if len(positional) == 2 {
		kind = strings.ToLower(strings.TrimSpace(positional[1]))
	}
	if kind != "remote" && kind != "ci" {
		return fmt.Errorf("worker kind must be remote or ci")
	}
	params := map[string]any{"name": name, "kind": kind}
	if len(pools) > 0 {
		params["pools"] = pools
	}
	return call(c, "worker.register", params)
}

func workerCredentialCall(c *rpcClient, args []string, subcommand, method string) error {
	if len(args) < 2 || len(args) > 3 || strings.TrimSpace(args[1]) == "" {
		return fmt.Errorf("usage: carina worker %s <worker_id> [worker_credential]", subcommand)
	}
	credential := strings.TrimSpace(os.Getenv("CARINA_WORKER_CREDENTIAL"))
	if len(args) == 3 {
		credential = strings.TrimSpace(args[2])
	}
	if credential == "" {
		return fmt.Errorf("worker credential is required (argument or CARINA_WORKER_CREDENTIAL)")
	}
	return call(c, method, map[string]any{
		"worker_id":         strings.TrimSpace(args[1]),
		"worker_credential": credential,
	})
}

func cmdDaemon(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: carina daemon <start|status|stop|logs>")
	}
	socket, err := defaultSocketPath()
	if err != nil {
		return err
	}
	switch args[0] {
	case "start":
		c, err := ensureDaemonReachable(socket)
		if err != nil {
			return err
		}
		defer c.Close()
		return call(c, "daemon.status", map[string]any{})
	case "status":
		c, err := dialSocketHook(socket)
		if err != nil {
			return err
		}
		defer c.Close()
		return call(c, "daemon.status", map[string]any{})
	case "stop":
		return stopOwnedDaemon(socket)
	case "logs":
		return printDaemonLogs(socket, os.Stdout)
	default:
		return fmt.Errorf("unknown daemon subcommand %q", args[0])
	}
}

func stopOwnedDaemon(socket string) error {
	record, err := loadDaemonOwnership(socket)
	if err != nil {
		return err
	}
	status, err := daemonStatusHook(socket)
	if err != nil {
		return fmt.Errorf("refusing to signal pid %d: daemon endpoint is not reachable: %w", record.PID, err)
	}
	if status.PID != record.PID {
		return fmt.Errorf("refusing to signal pid %d: live daemon reports pid %d", record.PID, status.PID)
	}
	if err := signalDaemonHook(record.PID, syscall.SIGTERM); err != nil {
		return fmt.Errorf("stop daemon pid %d: %w", record.PID, err)
	}
	fmt.Printf("stop requested for CLI-owned daemon pid %d\n", record.PID)
	return nil
}

func daemonOwnershipPath(socket string) string {
	return filepath.Join(filepath.Dir(socket), "daemon.pid.json")
}

func daemonLogPath(socket string) string {
	return filepath.Join(filepath.Dir(socket), "daemon.log")
}

func loadDaemonOwnership(socket string) (daemonOwnershipRecord, error) {
	path := daemonOwnershipPath(socket)
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return daemonOwnershipRecord{}, fmt.Errorf("no CLI ownership record at %s; refusing to stop a manually started daemon", path)
		}
		return daemonOwnershipRecord{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return daemonOwnershipRecord{}, fmt.Errorf("unsafe daemon ownership record %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return daemonOwnershipRecord{}, fmt.Errorf("daemon ownership record %s must not be group/world accessible", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return daemonOwnershipRecord{}, err
	}
	var record daemonOwnershipRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return daemonOwnershipRecord{}, fmt.Errorf("parse daemon ownership record: %w", err)
	}
	if record.Owner != daemonOwnershipMarker || record.PID <= 1 || record.Socket != socket {
		return daemonOwnershipRecord{}, fmt.Errorf("invalid daemon ownership record %s", path)
	}
	return record, nil
}

func writePrivateFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".carina-owned-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func printDaemonLogs(socket string, w io.Writer) error {
	path := daemonLogPath(socket)
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read daemon logs %s: %w", path, err)
	}
	defer f.Close()
	const maxLogBytes = 256 << 10
	info, err := f.Stat()
	if err != nil {
		return err
	}
	start := info.Size() - maxLogBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if start > 0 {
		if newline := strings.IndexByte(string(data), '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	_, err = w.Write(data)
	return err
}

var completionSubcommands = map[string][]string{
	"audit":     {"verify", "last"},
	"auth":      {"login", "list", "logout"},
	"context":   {"status", "doctor", "stats", "compress", "retrieve"},
	"daemon":    {"start", "status", "stop", "logs"},
	"gateway":   {"hello", "methods", "ws-probe"},
	"memory":    {"status", "list", "context", "search", "write", "projection-authorize", "projection-retry", "projection-reseed"},
	"patch":     {"list", "show", "propose", "apply", "rollback"},
	"providers": {"list"},
	"schedule":  {"list", "create", "pause", "resume", "delete"},
	"worker":    {"list", "register", "heartbeat", "revoke"},
	"workflow":  {"run", "list", "status", "pause", "resume", "stop", "restart"},
}

var completionRootCommands = []string{
	"agents", "answer", "approve", "ask", "audit", "auth", "backpressure", "close", "commands",
	"completion", "context", "cost", "daemon", "debug", "diff", "doctor", "exec", "export",
	"fork", "gateway", "grep", "help", "init", "items", "memory", "metrics", "patch",
	"patch-native", "plugin", "profile", "providers", "pty", "replay", "report", "resume",
	"run", "run-native", "scan", "schedule", "search", "secret", "sessions", "status", "steer",
	"update", "verify", "version", "watch", "worker", "workers", "workflow", "workflows",
}

func cmdCompletion(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: carina completion <bash|zsh|fish>")
	}
	script, err := completionScript(args[0])
	if err != nil {
		return err
	}
	fmt.Print(script)
	return nil
}

func completionScript(shell string) (string, error) {
	roots := append([]string(nil), completionRootCommands...)
	sort.Strings(roots)
	rootWords := strings.Join(roots, " ")
	switch shell {
	case "bash":
		return fmt.Sprintf(`_carina_completion() {
  local cur command choices
  cur="${COMP_WORDS[COMP_CWORD]}"
  command="${COMP_WORDS[1]}"
  choices=%q
  case "$command" in
%s  esac
  COMPREPLY=( $(compgen -W "$choices" -- "$cur") )
}
complete -F _carina_completion carina
`, rootWords, bashCompletionCases()), nil
	case "zsh":
		return fmt.Sprintf(`#compdef carina
_carina() {
  local -a commands
  commands=(%s)
  if (( CURRENT == 2 )); then
    _describe 'command' commands
    return
  fi
  case "$words[2]" in
%s  esac
}
compdef _carina carina
`, rootWords, zshCompletionCases()), nil
	case "fish":
		var b strings.Builder
		fmt.Fprintf(&b, "complete -c carina -f -n '__fish_use_subcommand' -a '%s'\n", rootWords)
		keys := sortedCompletionKeys()
		for _, command := range keys {
			fmt.Fprintf(&b, "complete -c carina -f -n '__fish_seen_subcommand_from %s' -a '%s'\n", command, strings.Join(completionSubcommands[command], " "))
		}
		return b.String(), nil
	default:
		return "", fmt.Errorf("unsupported shell %q; expected bash, zsh, or fish", shell)
	}
}

func sortedCompletionKeys() []string {
	keys := make([]string, 0, len(completionSubcommands))
	for command := range completionSubcommands {
		keys = append(keys, command)
	}
	sort.Strings(keys)
	return keys
}

func bashCompletionCases() string {
	var b strings.Builder
	for _, command := range sortedCompletionKeys() {
		fmt.Fprintf(&b, "    %s) choices=%q ;;\n", command, strings.Join(completionSubcommands[command], " "))
	}
	return b.String()
}

func zshCompletionCases() string {
	var b strings.Builder
	for _, command := range sortedCompletionKeys() {
		fmt.Fprintf(&b, "    %s) _values '%s command' %s ;;\n", command, command, strings.Join(completionSubcommands[command], " "))
	}
	return b.String()
}
