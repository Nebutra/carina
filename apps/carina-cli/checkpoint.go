package main

import "fmt"

// cmdCheckpoint intentionally delegates all state changes to the daemon: the
// daemon owns audit provenance and rejects restore when the workspace changed
// since preview. The CLI never edits files or transcript state itself.
func cmdCheckpoint(c *rpcClient, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: carina checkpoint <list|preview|restore|summarize> <session_id> [checkpoint_id] [--yes]")
	}
	action, sessionID := args[0], args[1]
	switch action {
	case "list":
		if len(args) != 2 {
			return fmt.Errorf("usage: carina checkpoint list <session_id>")
		}
		return call(c, "session.checkpoint.list", map[string]any{"session_id": sessionID})
	case "preview", "summarize":
		if len(args) != 3 {
			return fmt.Errorf("usage: carina checkpoint %s <session_id> <checkpoint_id>", action)
		}
		return call(c, "session.checkpoint."+action, map[string]any{"session_id": sessionID, "checkpoint_id": args[2]})
	case "restore":
		if len(args) != 4 || args[3] != "--yes" {
			return fmt.Errorf("restore is destructive; preview first, then use: carina checkpoint restore <session_id> <checkpoint_id> --yes")
		}
		return call(c, "session.checkpoint.restore", map[string]any{"session_id": sessionID, "checkpoint_id": args[2], "confirmed": true})
	default:
		return fmt.Errorf("unknown checkpoint action %q", action)
	}
}
