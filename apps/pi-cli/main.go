// pi is the Pi-OS command-line client (PRD §11). It is a thin JSON-RPC
// client of pi-daemon — the CLI is not the runtime.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const usage = `pi — Pi Agent OS Runtime client

Usage:
  pi init                         create ~/.pi-os and print daemon hint
  pi status                       daemon health and counters
  pi sessions                     list sessions
  pi run "<prompt>"               create a session in cwd and submit a task
  pi ask "<prompt>"               alias for run
  pi resume <session_id>          show a session
  pi watch <session_id>           stream the live event feed
  pi audit <session_id>           replay the session event stream
  pi report <session_id>          audit summary (violations, files, commands)
  pi export <session_id>          export the full audit bundle (centralized audit)
  pi search <session_id> <text>   structured workspace search (pi-grep)
  pi exec <session_id> -- cmd...  run a command through the kernel

  pi patch list <session_id>
  pi patch show <session_id> <patch_id>
  pi patch propose <session_id> <path> <<< "new file content"
  pi patch apply <session_id> <patch_id>
  pi patch rollback <session_id> <patch_id>

  pi approve <session_id> <decision_id>
  pi deny <session_id> <decision_id> [reason]
  pi profile <session_id>                describe the active permission profile
  pi secret grant <session_id> <n> <v>   register a secret (returns a handle)
  pi secret request <session_id> <name>  request a secret handle
  pi plugin inspect <manifest.toml>              show declared permissions
  pi plugin run <session_id> <manifest> <wasm>   run a WASM plugin
  pi metrics

The daemon must be running: pi-daemon &
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "pi: %v\n", err)
		os.Exit(1)
	}
}

func run(cmd string, args []string) error {
	switch cmd {
	case "init":
		return cmdInit()
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
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

	case "run", "ask":
		if len(args) < 1 {
			return fmt.Errorf(`usage: pi %s "<prompt>"`, cmd)
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
		return call(c, "task.submit", map[string]any{"session_id": sess.SessionID, "prompt": args[0]})

	case "resume":
		return callArg(c, "session.get", args, "session_id")
	case "audit":
		return callArg(c, "session.replay", args, "session_id")
	case "report":
		return callArg(c, "audit.report", args, "session_id")
	case "export":
		return callArg(c, "audit.export", args, "session_id")
	case "close":
		return callArg(c, "session.close", args, "session_id")

	case "watch":
		if len(args) < 1 {
			return fmt.Errorf("usage: pi watch <session_id>")
		}
		return watch(c, args[0])

	case "search":
		if len(args) < 2 {
			return fmt.Errorf("usage: pi search <session_id> <text>")
		}
		return call(c, "workspace.search", map[string]any{"session_id": args[0], "pattern": args[1]})

	case "exec":
		return cmdExec(c, args)

	case "approve":
		if len(args) < 2 {
			return fmt.Errorf("usage: pi approve <session_id> <decision_id> [role]")
		}
		p := map[string]any{"session_id": args[0], "decision_id": args[1]}
		if len(args) > 2 {
			p["role"] = args[2]
		}
		return call(c, "task.action.approve", p)
	case "deny":
		if len(args) < 2 {
			return fmt.Errorf("usage: pi deny <session_id> <decision_id> [reason]")
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
			return fmt.Errorf("usage: pi profile <session_id>")
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

func cmdInit() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".pi-os")
	if err := os.MkdirAll(filepath.Join(dir, "state"), 0o700); err != nil {
		return err
	}
	fmt.Printf("initialized %s\nstart the runtime with:  pi-daemon &\n", dir)
	return nil
}

func cmdExec(c *rpcClient, args []string) error {
	// pi exec <session_id> -- cmd arg...
	if len(args) < 3 || args[1] != "--" {
		return fmt.Errorf("usage: pi exec <session_id> -- <command> [args...]")
	}
	return call(c, "command.exec", map[string]any{"session_id": args[0], "argv": args[2:]})
}

func cmdPatch(c *rpcClient, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: pi patch <list|show|propose|apply|rollback> <session_id> [...]")
	}
	sub, sessionID := args[0], args[1]
	switch sub {
	case "list":
		return call(c, "workspace.patch.list", map[string]any{"session_id": sessionID})
	case "show":
		if len(args) < 3 {
			return fmt.Errorf("usage: pi patch show <session_id> <patch_id>")
		}
		return call(c, "workspace.patch.show", map[string]any{"session_id": sessionID, "patch_id": args[2]})
	case "apply":
		if len(args) < 3 {
			return fmt.Errorf("usage: pi patch apply <session_id> <patch_id>")
		}
		return call(c, "workspace.patch.apply", map[string]any{"session_id": sessionID, "patch_id": args[2]})
	case "rollback":
		if len(args) < 3 {
			return fmt.Errorf("usage: pi patch rollback <session_id> <patch_id>")
		}
		return call(c, "workspace.patch.rollback", map[string]any{"session_id": sessionID, "patch_id": args[2]})
	case "propose":
		if len(args) < 3 {
			return fmt.Errorf("usage: pi patch propose <session_id> <path>  (new content on stdin)")
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
		return fmt.Errorf("usage: pi plugin <inspect|run> ...")
	}
	switch args[0] {
	case "inspect":
		if len(args) < 2 {
			return fmt.Errorf("usage: pi plugin inspect <manifest.toml>")
		}
		manifest, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		return call(c, "plugin.inspect", map[string]any{"manifest_toml": string(manifest)})
	case "run":
		if len(args) < 4 {
			return fmt.Errorf("usage: pi plugin run <session_id> <manifest.toml> <module.wasm> [signature.sig]")
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

func cmdSecret(c *rpcClient, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: pi secret <grant|request> <session_id> [name] [value]")
	}
	sub, sessionID := args[0], args[1]
	switch sub {
	case "grant":
		if len(args) < 4 {
			return fmt.Errorf("usage: pi secret grant <session_id> <name> <value>")
		}
		return call(c, "secret.grant", map[string]any{"session_id": sessionID, "name": args[2], "value": args[3]})
	case "request":
		if len(args) < 3 {
			return fmt.Errorf("usage: pi secret request <session_id> <name>")
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

func callArg(c *rpcClient, method string, args []string, key string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: pi ... <%s>", key)
	}
	return call(c, method, map[string]any{key: args[0]})
}

func defaultSocketPath() (string, error) {
	if s := os.Getenv("PI_OS_SOCKET"); s != "" {
		return s, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi-os", "daemon.sock"), nil
}
