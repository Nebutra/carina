// pi is the Pi-OS command-line client (PRD §11). It is a thin JSON-RPC
// client of pi-daemon — the CLI is not the runtime.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const usage = `pi — Pi Agent OS Runtime client (Phase 0)

Usage:
  pi status                      daemon health and counters
  pi sessions                    list sessions
  pi run "<prompt>"              create a session in cwd and submit a task
  pi resume <session_id>         show a session
  pi audit <session_id>          replay the session event stream
  pi close <session_id>          close a session

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
	c, err := dialDaemon()
	if err != nil {
		return err
	}
	defer c.Close()

	switch cmd {
	case "status":
		return call(c, "daemon.status", map[string]any{})

	case "sessions":
		return call(c, "session.list", map[string]any{})

	case "run":
		if len(args) < 1 {
			return fmt.Errorf(`usage: pi run "<prompt>"`)
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
		if len(args) < 1 {
			return fmt.Errorf("usage: pi resume <session_id>")
		}
		return call(c, "session.get", map[string]any{"session_id": args[0]})

	case "audit":
		if len(args) < 1 {
			return fmt.Errorf("usage: pi audit <session_id>")
		}
		return call(c, "session.replay", map[string]any{"session_id": args[0]})

	case "close":
		if len(args) < 1 {
			return fmt.Errorf("usage: pi close <session_id>")
		}
		return call(c, "session.close", map[string]any{"session_id": args[0]})

	default:
		fmt.Print(usage)
		return fmt.Errorf("unknown command %q", cmd)
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

func defaultSocketPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi-os", "daemon.sock"), nil
}
