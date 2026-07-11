package main

import (
	"fmt"
	"strings"
)

func cmdSession(c *rpcClient, args []string) error {
	if len(args) != 2 || args[0] != "review" || strings.TrimSpace(args[1]) == "" {
		return fmt.Errorf("usage: carina session review <session_id>")
	}
	return call(c, "session.review", map[string]any{"session_id": strings.TrimSpace(args[1])})
}

func cmdChannel(c *rpcClient, args []string) error {
	if len(args) == 1 && args[0] == "pending" {
		return call(c, "channel.event.pending", map[string]any{})
	}
	if len(args) != 5 || args[0] != "reconcile" || args[4] != "--yes" {
		return fmt.Errorf("usage: carina channel reconcile <sender_id> <event_id> <executed|not-executed> --yes")
	}
	outcome := ""
	switch args[3] {
	case "executed":
		outcome = "executed"
	case "not-executed":
		outcome = "not_executed"
	default:
		return fmt.Errorf("outcome must be executed or not-executed")
	}
	if strings.TrimSpace(args[1]) == "" || strings.TrimSpace(args[2]) == "" {
		return fmt.Errorf("sender_id and event_id are required")
	}
	return call(c, "channel.event.reconcile", map[string]any{"sender_id": strings.TrimSpace(args[1]), "event_id": strings.TrimSpace(args[2]), "outcome": outcome, "confirmed": true})
}
