// carina-tui is a minimal read-only terminal dashboard for Carina.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Nebutra/carina/go/rpc"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "carina-tui: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("carina-tui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	socket := fs.String("socket", defaultSocket(), "carina daemon unix socket")
	if err := fs.Parse(args); err != nil {
		return err
	}
	view := "status"
	if fs.NArg() > 0 {
		view = fs.Arg(0)
	}
	c, err := rpc.Dial(*socket)
	if err != nil {
		return err
	}
	defer c.Close()
	switch view {
	case "status":
		var status map[string]any
		if err := c.Call("daemon.status", map[string]any{}, &status); err != nil {
			return err
		}
		fmt.Print(formatStatus(status))
	case "sessions":
		var sessions []sessionRow
		if err := c.Call("session.list", map[string]any{}, &sessions); err != nil {
			return err
		}
		fmt.Print(formatSessions(sessions))
	case "json":
		var status map[string]any
		if err := c.Call("daemon.status", map[string]any{}, &status); err != nil {
			return err
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	default:
		return fmt.Errorf("usage: carina-tui [--socket path] [status|sessions|json]")
	}
	return nil
}

func defaultSocket() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".carina", "daemon.sock")
	}
	return filepath.Join(home, ".carina", "daemon.sock")
}

func formatStatus(status map[string]any) string {
	keys := []string{"version", "sessions", "tasks", "workers", "tools", "uptime_seconds", "rpc_endpoint"}
	var b strings.Builder
	b.WriteString("Carina Runtime\n")
	b.WriteString("==============\n")
	for _, key := range keys {
		if v, ok := status[key]; ok {
			fmt.Fprintf(&b, "%-15s %v\n", key, v)
		}
	}
	var rest []string
	for key := range status {
		if !contains(keys, key) {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		fmt.Fprintf(&b, "%-15s %v\n", key, status[key])
	}
	return b.String()
}

type sessionRow struct {
	SessionID     string `json:"session_id"`
	WorkspaceRoot string `json:"workspace_root"`
	Profile       string `json:"profile"`
	Status        string `json:"status"`
}

func formatSessions(sessions []sessionRow) string {
	var b strings.Builder
	b.WriteString("Sessions\n")
	b.WriteString("========\n")
	if len(sessions) == 0 {
		b.WriteString("no sessions\n")
		return b.String()
	}
	for _, s := range sessions {
		fmt.Fprintf(&b, "%s  %-8s  %-10s  %s\n", s.SessionID, s.Status, s.Profile, s.WorkspaceRoot)
	}
	return b.String()
}

func contains(items []string, item string) bool {
	for _, candidate := range items {
		if candidate == item {
			return true
		}
	}
	return false
}
