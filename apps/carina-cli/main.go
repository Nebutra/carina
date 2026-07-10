// carina is the Carina command-line client (PRD §11). It is a thin JSON-RPC
// client of carina-daemon — the CLI is not the runtime.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/provider"
	"github.com/Nebutra/carina/go/ttyutil"
	"github.com/Nebutra/carina/go/tui"
)

const cliVersion = "0.6.0"

const usage = `carina — command-line client for the Carina Agent Runtime

Usage:
  carina <command> [arguments]

Start and run:
  carina init                                      create ~/.carina and print daemon hint
  carina status                                    show daemon health and counters
  carina doctor [--json]                           pass/warn/fail diagnostics with copy-paste fixes
  carina run [--agent name] [--model provider/model] "<prompt>" [--background]
                                                   create a safe-edit session in cwd, submit a task, and
                                                   wait for it to finish (exits with its governance
                                                   outcome); --background returns as soon as it is queued
  carina ask [--agent name] [--model provider/model] "<prompt>" [--background]
                                                   alias for run
  carina agents list                               list available agent modes
  carina commands list                             list slash commands

Inspect sessions:
  carina sessions                                  list sessions
  carina resume <session_id> [prompt|-]            continue or inspect a session
  carina watch <session_id> [--json]                stream live events (--json emits typed control_request frames)
  carina items <session_id>                        replay normalized thread/turn/item events
  carina search <session_id> <text>                search the workspace through the daemon

Memory:
  carina memory status <session_id>                 show local memory scope, provider, and sync boundary
  carina memory list <session_id> <memory|user>      list governed memory entries
  carina memory context <session_id>                 render the recalled-memory prompt block
  carina memory search [--semantic|--auto] <session_id> <query>
                                                   search curated local memory entries
  carina memory write <session_id> <memory|user> add <content|->
                                                   request a memory add
  carina memory write <session_id> <memory|user> replace <old_text> <content|->
                                                   request a memory replacement
  carina memory write <session_id> <memory|user> remove <old_text>
                                                   request a memory removal

Context engine:
  carina context status                             show native context engine and Headroom availability
  carina context doctor                             diagnose context engine health
  carina context stats                              show local and Headroom context-engine counters
  carina context compress <content|->               compress content through the native context engine
  carina context retrieve <hash> [query]             retrieve original context from Headroom CCR

Schedules:
  carina schedule list                               list persistent schedules
  carina schedule create <session_id> <at|every|cron> <expression> <prompt>
  carina schedule pause|resume|delete <schedule_id>  manage a persistent schedule

Audit and rollback:
  carina audit <session_id>                        replay the raw session event stream
  carina audit verify <session_id>                 verify the tamper-evident hash chain
  carina audit last                                summarize the most recent session
  carina report <session_id>                       summarize violations, files, and commands
  carina export <session_id>                       export the full audit bundle
  carina patch list <session_id>                   list patch transactions
  carina patch show <session_id> <patch_id>         show a patch transaction
  carina patch propose <session_id> <path>          propose file content from stdin
  carina patch apply <session_id> <patch_id>        apply a proposed patch
  carina patch rollback <session_id> <patch_id>     roll back an applied patch

Approvals, secrets, and plugins:
  carina approve <session_id> <decision_id> [role]  approve a pending decision
  carina deny <session_id> <decision_id> [reason]   deny a pending decision
  carina profile <session_id>                       describe the active permission profile
  carina secret grant <session_id> <name> <value>   register a secret and return a handle
  carina secret request <session_id> <name>         request a secret handle
  carina plugin inspect <manifest.toml>             show declared plugin permissions
  carina plugin run <session_id> <manifest> <wasm>  run a WASM plugin through policy

Providers and BYOK:
  carina auth login <provider> [api_key|-]          store a local BYOK credential
  carina auth list                                  list local credential sources
  carina auth logout <provider>                     remove a local credential
  carina providers list [--refresh] [--offline]     list provider catalog entries

Gateway and RPC:
  carina gateway hello [role]                       negotiate Gateway role/scope discovery
  carina gateway methods                            list RPC methods with scope/exposure metadata
  carina gateway ws-probe <ws-url> [role]           probe Gateway WebSocket handshake and hello
  carina backpressure status                         show worker pressure reports and throttle directives
  carina debug snapshot [limit]                      show local-only diagnostic trace events
  carina debug trace <correlation_id> [limit]        search diagnostic trace by correlation id

Native tools, no daemon:
  carina scan [path]                                workspace file tree
  carina grep <pattern> <path>                      structured search
  carina diff <a> <b>                               structured diff
  carina run-native [opts] -- cmd...                run a command with native wrapper
  carina pty [opts] -- cmd...                       interactive pseudo-terminal
  carina patch-native <apply|dry-run|rollback>      atomic patch primitive, JSON on stdin

Daemon:
  carina-daemon &                                   start the local control-plane daemon
`

func main() {
	if len(os.Args) < 2 {
		if action := decideBareInvocation(ttyutil.IsTTY(os.Stdin), ttyutil.IsTTY(os.Stdout)); action == bareActionLaunchTUI {
			os.Exit(runBareTUI().ExitCode())
		}
		fmt.Print(usage)
		os.Exit(tui.OutcomeUsage.ExitCode())
	}
	err := run(os.Args[1], os.Args[2:])
	outcome := classifyExitCode(err)
	if err != nil {
		fmt.Fprintf(os.Stderr, "carina: %v\n", err)
	}
	os.Exit(outcome.ExitCode())
}

func run(cmd string, args []string) error {
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println("carina " + cliVersion)
		return nil
	case "init":
		return cmdInit()
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	case "auth":
		return cmdAuth(args)
	case "providers":
		return cmdProviders(args)
	// Native toolchain launchers (PRD §8.1): carina forwards straight to the
	// Zig binaries — no daemon, no business logic, just process exec.
	// run/patch use a -native suffix to avoid clashing with the agent-level
	// `carina run` and the daemon-level `carina patch`.
	case "scan", "grep", "diff", "pty":
		return execTool("carina-"+cmd, args)
	case "run-native":
		return execTool("carina-run", args)
	case "patch-native":
		return execTool("carina-patch-native", args)
	case "gateway":
		if len(args) > 0 && args[0] == "ws-probe" {
			return cmdGatewayWSProbe(args[1:])
		}
	}

	// Every governed subcommand from here down shares one init gate
	// (P1.8 startup discipline): help/version/completion and the native
	// passthrough above never reach this line.
	c, err := initGate(cmd)
	if err != nil {
		return err
	}
	if c != nil {
		defer c.Close()
	}

	switch cmd {
	case "doctor":
		// c is nil exactly when initGate's doctor kill-switch fired
		// (CARINA_DOCTOR_DISABLE set): cmdDoctor renders the same disabled
		// report the daemon itself would, without ever dialing. The returned
		// error (nil on an all-PASS report) carries the doctor-specific
		// WARN/FAIL classification for classifyExitCode.
		return cmdDoctor(c, args)
	case "status":
		return call(c, "daemon.status", map[string]any{})
	case "metrics":
		return call(c, "daemon.metrics", map[string]any{})
	case "sessions":
		return call(c, "session.list", map[string]any{})
	case "agents":
		return cmdAgents(c, args)
	case "commands":
		return cmdCommands(c, args)
	case "gateway":
		return cmdGateway(c, args)
	case "backpressure":
		return cmdBackpressure(c, args)
	case "debug":
		return cmdDebug(c, args)
	case "memory":
		return cmdMemory(c, args)
	case "context":
		return cmdContext(c, args)
	case "schedule":
		return cmdSchedule(c, args)

	case "run", "ask":
		// The task always runs in the daemon and survives CLI exit (PRD
		// §5.2/§10.2) either way; --background additionally opts the CLI
		// process itself out of waiting for the outcome (P1.5(b)): by
		// default (foreground) `carina run` blocks until the task reaches
		// a terminal state and exits with that outcome's governance-
		// distinct code, matching the plan's exit criterion — without it,
		// run() returned nil the instant task.submit's RPC round trip
		// succeeded and a genuinely failed/policy-denied run exited 0.
		background := hasFlag(args, "--background")
		args = dropFlag(args, "--background")
		prompt, model, agent, err := parseRunArgs(args)
		if err != nil {
			return fmt.Errorf(`usage: carina %s [--agent name] [--model provider/model] "<prompt>" [--background]`, cmd)
		}
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		var sess struct {
			SessionID string `json:"session_id"`
		}
		if err := c.Call("session.create", map[string]any{"workspace_root": cwd, "profile": "safe-edit"}, &sess); err != nil {
			return err
		}
		fmt.Printf("session: %s (safe-edit, %s)\n", sess.SessionID, cwd)
		params := map[string]any{"session_id": sess.SessionID, "prompt": prompt}
		if model != "" {
			params["model"] = model
		}
		if agent != "" {
			params["agent"] = agent
		}
		var task map[string]any
		if err := c.Call("task.submit", params, &task); err != nil {
			return err
		}
		if err := printJSON(task); err != nil {
			return err
		}
		if background {
			printResumeHint(sess.SessionID)
			return nil
		}
		taskID, _ := task["task_id"].(string)
		if taskID == "" {
			printResumeHint(sess.SessionID)
			return nil
		}
		fmt.Println("waiting for the task to finish (ctrl-c to stop watching; the task keeps running) ...")
		return runWaitForTask(c, taskID, runWaitForTaskDefaultTimeout, nil)

	case "resume":
		return cmdResume(c, args)
	case "audit":
		return cmdAudit(c, args)
	case "replay":
		if len(args) < 1 {
			return fmt.Errorf("usage: carina replay <session_id>")
		}
		return call(c, "session.replay", map[string]any{"session_id": args[0]})
	case "items":
		if len(args) < 1 {
			return fmt.Errorf("usage: carina items <session_id>")
		}
		return call(c, "session.items", map[string]any{"session_id": args[0]})
	case "report":
		return callArg(c, "audit.report", args, "session_id")
	case "export":
		return callArg(c, "audit.export", args, "session_id")
	case "verify":
		return callArg(c, "audit.verify", args, "session_id")
	case "close":
		return callArg(c, "session.close", args, "session_id")

	case "watch":
		sessionID, jsonOut, err := parseWatchArgs(args)
		if err != nil {
			return err
		}
		return watch(c, sessionID, jsonOut)

	case "search":
		if len(args) < 2 {
			return fmt.Errorf("usage: carina search <session_id> <text>")
		}
		return call(c, "workspace.search", map[string]any{"session_id": args[0], "pattern": args[1]})

	case "exec":
		return cmdExec(c, args)

	case "approve":
		if len(args) < 2 {
			return fmt.Errorf("usage: carina approve <session_id> <decision_id> [role]")
		}
		p := map[string]any{"session_id": args[0], "decision_id": args[1]}
		if len(args) > 2 {
			p["role"] = args[2]
		}
		return call(c, "task.action.approve", p)
	case "deny":
		if len(args) < 2 {
			return fmt.Errorf("usage: carina deny <session_id> <decision_id> [reason]")
		}
		reason := "denied by user"
		if len(args) > 2 {
			reason = strings.Join(args[2:], " ")
		}
		return call(c, "task.action.deny", map[string]any{"session_id": args[0], "decision_id": args[1], "reason": reason})

	case "patch":
		return cmdPatch(c, args)

	case "profile":
		if len(args) < 1 {
			return fmt.Errorf("usage: carina profile <session_id>")
		}
		return call(c, "profile.describe", map[string]any{"session_id": args[0]})

	case "secret":
		return cmdSecret(c, args)

	case "plugin":
		return cmdPlugin(c, args)

	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func parseRunArgs(args []string) (prompt, model, agent string, err error) {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model", "-m":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", "", "", fmt.Errorf("model required")
			}
			model = strings.TrimSpace(args[i+1])
			i++
		case "--agent", "-a":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return "", "", "", fmt.Errorf("agent required")
			}
			agent = strings.TrimSpace(args[i+1])
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) < 1 || strings.TrimSpace(rest[0]) == "" {
		return "", "", "", fmt.Errorf("prompt required")
	}
	return rest[0], model, agent, nil
}

type resumeOptions struct {
	sessionID string
	prompt    string
	model     string
	agent     string
	watch     bool
	json      bool
	noInput   bool
}

type resumeSession struct {
	SessionID         string `json:"session_id"`
	WorkspaceID       string `json:"workspace_id"`
	WorkspaceRoot     string `json:"workspace_root"`
	Status            string `json:"status"`
	PermissionProfile string `json:"permission_profile"`
	ApprovalMode      string `json:"approval_mode,omitempty"`
	ParentID          string `json:"parent_id,omitempty"`
	Depth             int    `json:"depth"`
	CreatedAt         string `json:"created_at"`
}

func cmdResume(c *rpcClient, args []string) error {
	opts, err := parseResumeArgs(args)
	if err != nil {
		return err
	}
	var sess resumeSession
	if err := c.Call("session.get", map[string]any{"session_id": opts.sessionID}, &sess); err != nil {
		return err
	}

	prompt := opts.prompt
	if strings.TrimSpace(prompt) == "-" {
		prompt, err = readAllStdin()
		if err != nil {
			return err
		}
	} else if strings.TrimSpace(prompt) == "" && !opts.noInput {
		prompt, err = resumePromptFromInput(sess.SessionID)
		if err != nil {
			return err
		}
	}

	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		if opts.json {
			return printJSON(sess)
		}
		return printResumeSummary(c, sess)
	}

	if sess.Status == "paused" {
		if err := c.Call("session.resume", map[string]any{"session_id": sess.SessionID}, &sess); err != nil {
			return err
		}
	}
	if sess.Status != "active" {
		return fmt.Errorf("session %s is %s, not active", sess.SessionID, sess.Status)
	}

	params := map[string]any{"session_id": sess.SessionID, "prompt": prompt}
	if opts.model != "" {
		params["model"] = opts.model
	}
	if opts.agent != "" {
		params["agent"] = opts.agent
	}
	var task json.RawMessage
	if err := c.Call("task.submit", params, &task); err != nil {
		return err
	}
	if !opts.json {
		fmt.Printf("resuming session: %s\n", sess.SessionID)
	}
	if err := printJSON(task); err != nil {
		return err
	}
	if opts.watch {
		return watch(c, sess.SessionID, opts.json)
	}
	if !opts.json {
		printResumeHint(sess.SessionID)
	}
	return nil
}

func parseResumeArgs(args []string) (resumeOptions, error) {
	var opts resumeOptions
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model", "-m":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return opts, fmt.Errorf("model required")
			}
			opts.model = strings.TrimSpace(args[i+1])
			i++
		case "--agent", "-a":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return opts, fmt.Errorf("agent required")
			}
			opts.agent = strings.TrimSpace(args[i+1])
			i++
		case "--watch", "-w":
			opts.watch = true
		case "--json":
			opts.json = true
		case "--no-input":
			opts.noInput = true
		case "--":
			rest = append(rest, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(args[i], "-") {
				return opts, fmt.Errorf("unknown resume flag %q", args[i])
			}
			rest = append(rest, args[i])
		}
	}
	if len(rest) < 1 || strings.TrimSpace(rest[0]) == "" {
		return opts, fmt.Errorf(`usage: carina resume <session_id> [--agent name] [--model provider/model] [--watch] [prompt|-]`)
	}
	opts.sessionID = strings.TrimSpace(rest[0])
	if len(rest) > 1 {
		opts.prompt = strings.Join(rest[1:], " ")
	}
	return opts, nil
}

func resumePromptFromInput(sessionID string) (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return readAllStdin()
	}
	fmt.Fprintf(os.Stderr, "Resuming %s. Enter a follow-up prompt, or press Enter to inspect only.\n> ", sessionID)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func printResumeSummary(c *rpcClient, sess resumeSession) error {
	fmt.Printf("session: %s\n", sess.SessionID)
	fmt.Printf("status: %s\n", sess.Status)
	fmt.Printf("workspace: %s\n", sess.WorkspaceRoot)
	fmt.Printf("profile: %s\n", sess.PermissionProfile)
	if sess.ApprovalMode != "" {
		fmt.Printf("approval_mode: %s\n", sess.ApprovalMode)
	}
	if sess.ParentID != "" {
		fmt.Printf("parent: %s\n", sess.ParentID)
	}
	if sess.CreatedAt != "" {
		fmt.Printf("created_at: %s\n", sess.CreatedAt)
	}
	var items []struct {
		Type      string         `json:"type"`
		TaskID    string         `json:"task_id"`
		Timestamp string         `json:"timestamp"`
		Item      map[string]any `json:"item"`
		Details   map[string]any `json:"details"`
	}
	if err := c.Call("session.items", map[string]any{"session_id": sess.SessionID}, &items); err == nil && len(items) > 0 {
		fmt.Println("recent:")
		start := len(items) - 5
		if start < 0 {
			start = 0
		}
		for _, it := range items[start:] {
			fmt.Printf("  %s", it.Type)
			if it.TaskID != "" {
				fmt.Printf(" task=%s", it.TaskID)
			}
			if title, ok := it.Item["title"].(string); ok && title != "" {
				fmt.Printf(" %s", title)
			} else if summary, ok := it.Details["summary"].(string); ok && summary != "" {
				fmt.Printf(" %s", summary)
			}
			fmt.Println()
		}
	}
	fmt.Println("continue:")
	fmt.Printf("  carina resume %s \"<next instruction>\"\n", sess.SessionID)
	fmt.Printf("  carina watch %s\n", sess.SessionID)
	return nil
}

func printResumeHint(sessionID string) {
	fmt.Printf("To continue this session, run:\n  carina resume %s\n", sessionID)
}

// dropFlag removes a boolean flag from args if present.
func dropFlag(args []string, flag string) []string {
	out := args[:0:0]
	for _, a := range args {
		if a != flag {
			out = append(out, a)
		}
	}
	return out
}

// hasFlag reports whether a boolean flag is present in args.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func cmdInit() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".carina")
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		return err
	}
	fmt.Printf("initialized %s\nstart the runtime with:  carina-daemon &\n", dir)
	return nil
}

func cmdAuth(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: carina auth <login|list|logout> ...")
	}
	store, err := auth.NewStore("")
	if err != nil {
		return err
	}
	switch args[0] {
	case "login":
		if len(args) < 2 {
			return fmt.Errorf("usage: carina auth login <provider> [api_key|-]")
		}
		key := ""
		if len(args) > 2 && args[2] != "-" {
			key = args[2]
		} else {
			key, err = readAllStdin()
			if err != nil {
				return err
			}
		}
		if err := store.SetAPIKey(args[1], key, nil); err != nil {
			return err
		}
		fmt.Printf("stored credential for %s in %s\n", strings.ToLower(args[1]), store.Path)
		return nil
	case "list", "ls":
		list, err := store.ListSafe()
		if err != nil {
			return err
		}
		return printJSON(list)
	case "logout":
		if len(args) < 2 {
			return fmt.Errorf("usage: carina auth logout <provider>")
		}
		if err := store.Remove(args[1]); err != nil {
			return err
		}
		fmt.Printf("removed credential for %s\n", strings.ToLower(args[1]))
		return nil
	default:
		return fmt.Errorf("unknown auth subcommand %q", args[0])
	}
}

func cmdProviders(args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	if args[0] != "list" && args[0] != "ls" {
		return fmt.Errorf("usage: carina providers list [--refresh] [--offline]")
	}
	strategy := provider.RefreshOnlineIfUncached
	for _, a := range args[1:] {
		switch a {
		case "--refresh":
			if strategy == provider.RefreshOffline {
				return fmt.Errorf("--refresh and --offline cannot be combined")
			}
			strategy = provider.RefreshOnline
		case "--offline":
			if strategy == provider.RefreshOnline {
				return fmt.Errorf("--refresh and --offline cannot be combined")
			}
			strategy = provider.RefreshOffline
		default:
			return fmt.Errorf("unknown providers list flag %q", a)
		}
	}
	cachePath, err := provider.DefaultCachePath()
	if err != nil {
		return err
	}
	opts := provider.Options{CachePath: cachePath, ModelsURL: os.Getenv("CARINA_MODELS_URL")}
	cat, err := provider.LoadWithStrategy(context.Background(), opts, strategy)
	if err != nil {
		return err
	}
	type row struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Env    []string `json:"env,omitempty"`
		API    string   `json:"api,omitempty"`
		Models int      `json:"models"`
	}
	rows := []row{}
	for _, p := range provider.Sorted(cat) {
		rows = append(rows, row{ID: p.ID, Name: p.Name, Env: p.Env, API: p.API, Models: len(p.Models)})
	}
	return printJSON(rows)
}

func cmdAgents(c *rpcClient, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	if args[0] != "list" && args[0] != "ls" {
		return fmt.Errorf("usage: carina agents list")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return call(c, "agent.list", map[string]any{"workspace_root": cwd})
}

func cmdCommands(c *rpcClient, args []string) error {
	if len(args) == 0 {
		args = []string{"list"}
	}
	if args[0] != "list" && args[0] != "ls" {
		return fmt.Errorf("usage: carina commands list")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return call(c, "command.list", map[string]any{"workspace_root": cwd})
}

func cmdGateway(c *rpcClient, args []string) error {
	if len(args) == 0 {
		args = []string{"hello"}
	}
	switch args[0] {
	case "hello":
		role := "operator"
		if len(args) > 1 {
			role = args[1]
		}
		return call(c, "gateway.hello", map[string]any{"role": role})
	case "methods", "list", "ls":
		return call(c, "gateway.methods", map[string]any{})
	default:
		return fmt.Errorf("usage: carina gateway <hello|methods|ws-probe> [role]")
	}
}

func cmdBackpressure(c *rpcClient, args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("usage: carina backpressure status")
		}
		return call(c, "backpressure.status", map[string]any{})
	default:
		return fmt.Errorf("usage: carina backpressure status")
	}
}

func cmdDebug(c *rpcClient, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: carina debug <snapshot|trace> ...")
	}
	switch args[0] {
	case "snapshot":
		if len(args) > 2 {
			return fmt.Errorf("usage: carina debug snapshot [limit]")
		}
		params := map[string]any{}
		if len(args) == 2 {
			limit, err := parseLimit(args[1])
			if err != nil {
				return err
			}
			params["limit"] = limit
		}
		return call(c, "debug.snapshot", params)
	case "trace":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: carina debug trace <correlation_id> [limit]")
		}
		params := map[string]any{"correlation_id": args[1]}
		if len(args) == 3 {
			limit, err := parseLimit(args[2])
			if err != nil {
				return err
			}
			params["limit"] = limit
		}
		return call(c, "debug.correlation.search", params)
	default:
		return fmt.Errorf("usage: carina debug <snapshot|trace> ...")
	}
}

func parseLimit(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("limit must be a positive integer")
	}
	return n, nil
}

func cmdMemory(c *rpcClient, args []string) error {
	method, params, err := memoryRPC(args, readAllStdin)
	if err != nil {
		return err
	}
	return call(c, method, params)
}

func cmdContext(c *rpcClient, args []string) error {
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("usage: carina context status")
		}
		return call(c, "context.status", map[string]any{})
	case "doctor":
		if len(args) != 1 {
			return fmt.Errorf("usage: carina context doctor")
		}
		return call(c, "context.doctor", map[string]any{})
	case "stats":
		if len(args) != 1 {
			return fmt.Errorf("usage: carina context stats")
		}
		return call(c, "context.stats", map[string]any{})
	case "compress":
		if len(args) != 2 {
			return fmt.Errorf("usage: carina context compress <content|->")
		}
		content := args[1]
		if content == "-" {
			var err error
			content, err = readAllStdin()
			if err != nil {
				return err
			}
		}
		if content == "" {
			return fmt.Errorf("content is required")
		}
		return call(c, "context.compress", map[string]any{"content": content})
	case "retrieve":
		if len(args) < 2 || len(args) > 3 {
			return fmt.Errorf("usage: carina context retrieve <hash> [query]")
		}
		params := map[string]any{"hash": args[1]}
		if len(args) == 3 {
			params["query"] = args[2]
		}
		return call(c, "context.retrieve", params)
	default:
		return fmt.Errorf("usage: carina context <status|doctor|stats|compress|retrieve>")
	}
}

func memoryRPC(args []string, readInput func() (string, error)) (string, map[string]any, error) {
	if len(args) < 1 {
		return "", nil, fmt.Errorf("usage: carina memory <status|list|context|write> ...")
	}
	switch args[0] {
	case "status":
		if len(args) != 2 {
			return "", nil, fmt.Errorf("usage: carina memory status <session_id>")
		}
		return "memory.status", map[string]any{"session_id": args[1]}, nil
	case "list", "ls":
		if len(args) != 3 {
			return "", nil, fmt.Errorf("usage: carina memory list <session_id> <memory|user>")
		}
		return "memory.list", map[string]any{"session_id": args[1], "target": args[2]}, nil
	case "context":
		if len(args) != 2 {
			return "", nil, fmt.Errorf("usage: carina memory context <session_id>")
		}
		return "memory.context", map[string]any{"session_id": args[1]}, nil
	case "search":
		mode := ""
		rest := args[1:]
	searchFlags:
		for len(rest) > 0 {
			switch rest[0] {
			case "--semantic":
				mode = "semantic"
				rest = rest[1:]
			case "--auto":
				mode = "auto"
				rest = rest[1:]
			case "--mode":
				if len(rest) < 2 {
					return "", nil, fmt.Errorf("usage: carina memory search [--semantic|--auto|--mode lexical|semantic|auto] <session_id> <query>")
				}
				mode = rest[1]
				rest = rest[2:]
			default:
				break searchFlags
			}
		}
		if len(rest) < 2 {
			return "", nil, fmt.Errorf("usage: carina memory search [--semantic|--auto|--mode lexical|semantic|auto] <session_id> <query>")
		}
		params := map[string]any{"session_id": rest[0], "query": strings.Join(rest[1:], " ")}
		if mode != "" {
			params["mode"] = mode
		}
		return "memory.search", params, nil
	case "write":
		if len(args) < 4 {
			return "", nil, fmt.Errorf("usage: carina memory write <session_id> <memory|user> <add|replace|remove> ...")
		}
		sessionID, target, action := args[1], args[2], strings.ToLower(strings.TrimSpace(args[3]))
		params := map[string]any{"session_id": sessionID, "target": target, "action": action}
		switch action {
		case "add":
			content, err := memoryCLIContent(args[4:], readInput)
			if err != nil {
				return "", nil, err
			}
			params["content"] = content
		case "replace":
			if len(args) < 5 {
				return "", nil, fmt.Errorf("usage: carina memory write <session_id> <memory|user> replace <old_text> <content|->")
			}
			content, err := memoryCLIContent(args[5:], readInput)
			if err != nil {
				return "", nil, err
			}
			params["old_text"] = args[4]
			params["content"] = content
		case "remove":
			if len(args) < 5 {
				return "", nil, fmt.Errorf("usage: carina memory write <session_id> <memory|user> remove <old_text>")
			}
			params["old_text"] = strings.Join(args[4:], " ")
		default:
			return "", nil, fmt.Errorf("memory action must be add, replace, or remove")
		}
		return "memory.write", params, nil
	default:
		return "", nil, fmt.Errorf("usage: carina memory <status|list|context|write> ...")
	}
}

func cmdSchedule(c *rpcClient, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: carina schedule <list|create|pause|resume|delete> ...")
	}
	switch args[0] {
	case "list", "ls":
		if len(args) != 1 {
			return fmt.Errorf("usage: carina schedule list")
		}
		return call(c, "schedule.list", map[string]any{})
	case "create":
		if len(args) < 5 {
			return fmt.Errorf("usage: carina schedule create <session_id> <at|every|cron> <expression> <prompt>")
		}
		return call(c, "schedule.create", map[string]any{
			"session_id": args[1], "kind": args[2], "expression": args[3], "prompt": strings.Join(args[4:], " "),
		})
	case "pause", "resume", "delete":
		if len(args) != 2 {
			return fmt.Errorf("usage: carina schedule %s <schedule_id>", args[0])
		}
		return call(c, "schedule."+args[0], map[string]any{"schedule_id": args[1]})
	default:
		return fmt.Errorf("usage: carina schedule <list|create|pause|resume|delete> ...")
	}
}

func memoryCLIContent(args []string, readInput func() (string, error)) (string, error) {
	if len(args) == 0 {
		return readInput()
	}
	content := strings.Join(args, " ")
	if strings.TrimSpace(content) == "-" {
		return readInput()
	}
	return content, nil
}

func cmdGatewayWSProbe(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return fmt.Errorf("usage: carina gateway ws-probe <ws-url> [role]")
	}
	role := "operator"
	if len(args) > 1 && strings.TrimSpace(args[1]) != "" {
		role = strings.TrimSpace(args[1])
	}
	payload, err := gatewayWSProbe(args[0], role)
	if err != nil {
		return err
	}
	var out any
	if err := json.Unmarshal(payload, &out); err != nil {
		return fmt.Errorf("decode websocket response: %w", err)
	}
	return printJSON(out)
}

func gatewayWSProbe(rawURL, role string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse websocket url: %w", err)
	}
	switch u.Scheme {
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("websocket url must use ws:// or wss://")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("websocket url missing host")
	}
	if u.Path == "" {
		u.Path = "/gateway"
	}

	conn, reader, err := dialGatewayWebSocket(u, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "gateway.hello",
		"params":  map[string]any{"role": role},
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := writeClientWebSocketText(conn, b); err != nil {
		return nil, fmt.Errorf("send gateway.hello: %w", err)
	}
	resp, err := readServerWebSocketText(reader)
	if err != nil {
		return nil, fmt.Errorf("read gateway.hello response: %w", err)
	}
	return resp, nil
}

func dialGatewayWebSocket(u *url.URL, timeout time.Duration) (net.Conn, *bufio.Reader, error) {
	hostPort := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "wss" {
			port = "443"
		}
		hostPort = net.JoinHostPort(u.Hostname(), port)
	}
	dialer := &net.Dialer{Timeout: timeout}
	var (
		conn net.Conn
		err  error
	)
	if u.Scheme == "wss" {
		conn, err = tls.DialWithDialer(dialer, "tcp", hostPort, &tls.Config{ServerName: u.Hostname()})
	} else {
		conn, err = dialer.Dial("tcp", hostPort)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("dial websocket gateway: %w", err)
	}

	reader := bufio.NewReader(conn)
	key, err := newWebSocketKey()
	if err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	reqURI := u.RequestURI()
	if reqURI == "" {
		reqURI = "/gateway"
	}
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nUser-Agent: carina/%s\r\n\r\n", reqURI, u.Host, key, cliVersion); err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("send websocket handshake: %w", err)
	}
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = conn.Close()
		return nil, nil, fmt.Errorf("read websocket handshake: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		_ = conn.Close()
		msg := strings.TrimSpace(string(body))
		if msg != "" {
			return nil, nil, fmt.Errorf("websocket handshake failed: %s: %s", resp.Status, msg)
		}
		return nil, nil, fmt.Errorf("websocket handshake failed: %s", resp.Status)
	}
	if got, want := strings.TrimSpace(resp.Header.Get("Sec-WebSocket-Accept")), webSocketAccept(key); got != want {
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, nil, fmt.Errorf("websocket handshake accept mismatch")
	}
	return conn, reader, nil
}

func newWebSocketKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate websocket key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

func webSocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func writeClientWebSocketText(conn net.Conn, payload []byte) error {
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return fmt.Errorf("generate websocket mask: %w", err)
	}
	header := []byte{0x81}
	n := len(payload)
	switch {
	case n < 126:
		header = append(header, 0x80|byte(n))
	case n <= 0xFFFF:
		header = append(header, 0x80|126, byte(n>>8), byte(n))
	default:
		header = append(header, 0x80|127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(n))
		header = append(header, ext[:]...)
	}
	header = append(header, mask[:]...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ mask[i%4]
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(masked)
	return err
}

func readServerWebSocketText(r *bufio.Reader) ([]byte, error) {
	for {
		opcode, payload, err := readServerWebSocketFrame(r)
		if err != nil {
			return nil, err
		}
		switch opcode {
		case 0x1:
			return payload, nil
		case 0x8:
			return nil, io.EOF
		case 0x9, 0xA:
			continue
		default:
			return nil, fmt.Errorf("websocket: unsupported opcode %d", opcode)
		}
	}
}

func readServerWebSocketFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	if hdr[0]&0x80 == 0 {
		return 0, nil, fmt.Errorf("websocket: fragmented frames are not supported")
	}
	opcode := hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	size := uint64(hdr[1] & 0x7F)
	switch size {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		size = binary.BigEndian.Uint64(ext[:])
	}
	if size > 16*1024*1024 {
		return 0, nil, fmt.Errorf("websocket: frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, int(size))
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func cmdExec(c *rpcClient, args []string) error {
	// carina exec <session_id> -- cmd arg...
	if len(args) < 3 || args[1] != "--" {
		return fmt.Errorf("usage: carina exec <session_id> -- <command> [args...]")
	}
	return call(c, "command.exec", map[string]any{"session_id": args[0], "argv": args[2:]})
}

func cmdPatch(c *rpcClient, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: carina patch <list|show|propose|apply|rollback> <session_id> [...]")
	}
	sub, sessionID := args[0], args[1]
	switch sub {
	case "list":
		return call(c, "workspace.patch.list", map[string]any{"session_id": sessionID})
	case "show":
		if len(args) < 3 {
			return fmt.Errorf("usage: carina patch show <session_id> <patch_id>")
		}
		return call(c, "workspace.patch.show", map[string]any{"session_id": sessionID, "patch_id": args[2]})
	case "apply":
		if len(args) < 3 {
			return fmt.Errorf("usage: carina patch apply <session_id> <patch_id>")
		}
		return call(c, "workspace.patch.apply", map[string]any{"session_id": sessionID, "patch_id": args[2]})
	case "rollback":
		if len(args) < 3 {
			return fmt.Errorf("usage: carina patch rollback <session_id> <patch_id>")
		}
		return call(c, "workspace.patch.rollback", map[string]any{"session_id": sessionID, "patch_id": args[2]})
	case "propose":
		if len(args) < 3 {
			return fmt.Errorf("usage: carina patch propose <session_id> <path>  (new content on stdin)")
		}
		content, err := readAllStdin()
		if err != nil {
			return err
		}
		return call(c, "workspace.patch.propose", map[string]any{
			"session_id": sessionID,
			"reason":     "cli propose",
			"files":      []map[string]any{{"path": args[2], "new_content": content}},
		})
	default:
		return fmt.Errorf("unknown patch subcommand %q", sub)
	}
}

func cmdPlugin(c *rpcClient, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: carina plugin <inspect|run> ...")
	}
	switch args[0] {
	case "inspect":
		if len(args) < 2 {
			return fmt.Errorf("usage: carina plugin inspect <manifest.toml>")
		}
		manifest, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		return call(c, "plugin.inspect", map[string]any{"manifest_toml": string(manifest)})
	case "run":
		if len(args) < 4 {
			return fmt.Errorf("usage: carina plugin run <session_id> <manifest.toml> <module.wasm> [signature.sig]")
		}
		manifest, err := os.ReadFile(args[2])
		if err != nil {
			return err
		}
		wasm, err := os.ReadFile(args[3])
		if err != nil {
			return err
		}
		p := map[string]any{
			"session_id":    args[1],
			"manifest_toml": string(manifest),
			"wasm_base64":   base64.StdEncoding.EncodeToString(wasm),
		}
		if len(args) > 4 {
			sig, err := os.ReadFile(args[4])
			if err != nil {
				return err
			}
			p["signature_base64"] = base64.StdEncoding.EncodeToString(sig)
		}
		return call(c, "plugin.run", p)
	default:
		return fmt.Errorf("unknown plugin subcommand %q", args[0])
	}
}

// cmdAudit dispatches:
//
//	carina audit <session_id>          replay the event stream
//	carina audit verify <session_id>   verify the tamper-evident hash chain
//	carina audit last                  audit summary of the most recent session
func cmdAudit(c *rpcClient, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: carina audit <session_id> | carina audit verify <session_id> | carina audit last")
	}
	switch args[0] {
	case "verify":
		if len(args) < 2 {
			return fmt.Errorf("usage: carina audit verify <session_id>")
		}
		return call(c, "audit.verify", map[string]any{"session_id": args[1]})
	case "last":
		sid, err := latestSession(c)
		if err != nil {
			return err
		}
		fmt.Printf("latest session: %s\n", sid)
		return call(c, "audit.report", map[string]any{"session_id": sid})
	default:
		return call(c, "session.replay", map[string]any{"session_id": args[0]})
	}
}

// latestSession returns the most recently created session id.
func latestSession(c *rpcClient) (string, error) {
	var sessions []struct {
		SessionID string `json:"session_id"`
		CreatedAt string `json:"created_at"`
	}
	if err := c.Call("session.list", map[string]any{}, &sessions); err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", fmt.Errorf("no sessions yet")
	}
	latest := sessions[0]
	for _, s := range sessions[1:] {
		if s.CreatedAt > latest.CreatedAt {
			latest = s
		}
	}
	return latest.SessionID, nil
}

func cmdSecret(c *rpcClient, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: carina secret <grant|request> <session_id> [name] [value]")
	}
	sub, sessionID := args[0], args[1]
	switch sub {
	case "grant":
		if len(args) < 4 {
			return fmt.Errorf("usage: carina secret grant <session_id> <name> <value>")
		}
		return call(c, "secret.grant", map[string]any{"session_id": sessionID, "name": args[2], "value": args[3]})
	case "request":
		if len(args) < 3 {
			return fmt.Errorf("usage: carina secret request <session_id> <name>")
		}
		return call(c, "secret.request", map[string]any{"session_id": sessionID, "name": args[2]})
	default:
		return fmt.Errorf("unknown secret subcommand %q", sub)
	}
}

// parseWatchArgs splits `carina watch <session_id> [--json]`'s positional
// session_id from the --json pipe-mode flag (P1.5(c)): with --json, watch
// emits typed control_request frames via controlFrameForEvent instead of
// dumping raw event JSON, so a CI bot/wrapper script can grep stdout for
// frame=control_request per the plan's documented pipe-mode contract.
func parseWatchArgs(args []string) (sessionID string, jsonOut bool, err error) {
	var positional []string
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
			continue
		}
		positional = append(positional, a)
	}
	if len(positional) < 1 {
		return "", false, fmt.Errorf("usage: carina watch <session_id> [--json]")
	}
	return positional[0], jsonOut, nil
}

func watch(c *rpcClient, sessionID string, jsonOut bool) error {
	if err := c.Call("session.events.stream", map[string]any{"session_id": sessionID}, nil); err != nil {
		return err
	}
	if !jsonOut {
		fmt.Printf("watching %s (ctrl-c to stop)\n", sessionID)
	}
	for {
		method, params, err := c.ReadNotification()
		if err != nil {
			return err
		}
		if method != "event" {
			continue
		}
		if !jsonOut {
			fmt.Println(string(params))
			continue
		}
		var event map[string]any
		if err := json.Unmarshal(params, &event); err != nil {
			continue
		}
		if frame, ok := controlFrameForEvent(event); ok {
			out, err := json.Marshal(frame)
			if err != nil {
				continue
			}
			fmt.Println(string(out))
		}
	}
}

func call(c *rpcClient, method string, params any) error {
	var result json.RawMessage
	if err := c.Call(method, params, &result); err != nil {
		return err
	}
	pretty, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(pretty))
	return nil
}

func printJSON(v any) error {
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(pretty))
	return nil
}

func callArg(c *rpcClient, method string, args []string, key string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: carina ... <%s>", key)
	}
	return call(c, method, map[string]any{key: args[0]})
}

func defaultSocketPath() (string, error) {
	if s := os.Getenv("CARINA_SOCKET"); s != "" {
		return s, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".carina", "daemon.sock"), nil
}
