// carina is the Carina command-line client (PRD §11). It is a thin JSON-RPC
// client of carina-daemon — the CLI is not the runtime.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nebutra/carina/go/auth"
	"github.com/Nebutra/carina/go/provider"
)

const cliVersion = "0.6.0"

const usage = `carina — Carina Agent Runtime client

Usage:
  carina init                         create ~/.carina and print daemon hint
  carina status                       daemon health and counters
  carina sessions                     list sessions
  carina run [--model provider/model] "<prompt>"
                                      create a session in cwd and submit a task
  carina ask [--model provider/model] "<prompt>"
                                      alias for run
  carina agents list                  list available agent modes
  carina commands list                list slash commands
  carina resume <session_id>          show a session
  carina watch <session_id>           stream the live event feed
  carina items <session_id>           replay the normalized item stream
  carina audit <session_id>           replay the session event stream
  carina audit verify <session_id>    verify the tamper-evident hash chain
  carina audit last                   audit summary of the most recent session
  carina replay <session_id>          replay the session event stream
  carina report <session_id>          audit summary (violations, files, commands)
  carina export <session_id>          export the full audit bundle (centralized audit)
  carina search <session_id> <text>   structured workspace search (carina-grep)
  carina exec <session_id> -- cmd...  run a command through the kernel

  Native tools (run straight on the Zig toolchain, no daemon):
  carina scan [path]                  workspace file tree (ignore rules, binary/lang)
  carina grep <pattern> <path>        structured search
  carina diff <a> <b>                 structured diff
  carina run-native [opts] -- cmd...  run a command (timeout/cwd/env)
  carina pty [opts] -- cmd...         interactive pseudo-terminal
  carina patch-native <apply|dry-run|rollback>   atomic patch primitive (JSON on stdin)

  carina patch list <session_id>
  carina patch show <session_id> <patch_id>
  carina patch propose <session_id> <path> <<< "new file content"
  carina patch apply <session_id> <patch_id>
  carina patch rollback <session_id> <patch_id>

  carina approve <session_id> <decision_id>
  carina deny <session_id> <decision_id> [reason]
  carina profile <session_id>                describe the active permission profile
  carina secret grant <session_id> <n> <v>   register a secret (returns a handle)
  carina secret request <session_id> <name>  request a secret handle
  carina plugin inspect <manifest.toml>              show declared permissions
  carina plugin run <session_id> <manifest> <wasm>   run a WASM plugin
  carina metrics
  carina auth <login|list|logout> ...        manage local BYOK credentials
  carina providers list [--refresh]          list provider catalog entries

The daemon must be running: carina-daemon &
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "carina: %v\n", err)
		os.Exit(1)
	}
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
	}

	c, err := dialDaemon()
	if err != nil {
		return err
	}
	defer c.Close()

	switch cmd {
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

	case "run", "ask":
		// --background is accepted for clarity; tasks always run in the
		// daemon and survive CLI exit (PRD §5.2/§10.2), so this is the
		// default behavior.
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
		return call(c, "task.submit", params)

	case "resume":
		return callArg(c, "session.get", args, "session_id")
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
		if len(args) < 1 {
			return fmt.Errorf("usage: carina watch <session_id>")
		}
		return watch(c, args[0])

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
		return fmt.Errorf("usage: carina providers list [--refresh]")
	}
	refresh := false
	for _, a := range args[1:] {
		switch a {
		case "--refresh":
			refresh = true
		default:
			return fmt.Errorf("unknown providers list flag %q", a)
		}
	}
	cachePath, err := provider.DefaultCachePath()
	if err != nil {
		return err
	}
	opts := provider.Options{CachePath: cachePath, ModelsURL: os.Getenv("CARINA_MODELS_URL")}
	var cat provider.Catalog
	if refresh {
		cat, err = provider.Refresh(context.Background(), opts)
	} else {
		cat, err = provider.Load(opts)
	}
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

func watch(c *rpcClient, sessionID string) error {
	if err := c.Call("session.events.stream", map[string]any{"session_id": sessionID}, nil); err != nil {
		return err
	}
	fmt.Printf("watching %s (ctrl-c to stop)\n", sessionID)
	for {
		method, params, err := c.ReadNotification()
		if err != nil {
			return err
		}
		if method == "event" {
			fmt.Println(string(params))
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
