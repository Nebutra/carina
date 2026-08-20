package daemon

import (
	"fmt"
	"os"
	"strings"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// recordCompactRebuild rehydrates cited files into the volatile transcript
// after a Step-2 fold, then audits the receipt. Rebuild never mutates the
// cacheable Workspace/F prefix: greetings and converse still must not dump
// AGENTS.md just because compact ran.
func (d *Daemon) recordCompactRebuild(sess *sessionstore.Session, task *scheduler.ExecutionRun, tr *Transcript, receipt *CompactionReceipt, extra map[string]any) {
	if d == nil || receipt == nil {
		return
	}
	payload := map[string]any{}
	for k, v := range extra {
		payload[k] = v
	}
	if paths := d.rebuildAfterCompact(sess, task, tr, receipt); len(paths) > 0 {
		payload["rebuild_paths"] = paths
	}
	sessionID, runID := "", ""
	if sess != nil {
		sessionID = sess.SessionID
	}
	if task != nil {
		runID = task.RunID
	}
	d.record(sessionID, "ContextCompacted", runID, "go", contextCompactedPayload(receipt, payload), "")
}

func (d *Daemon) rebuildAfterCompact(sess *sessionstore.Session, task *scheduler.ExecutionRun, tr *Transcript, receipt *CompactionReceipt) []string {
	if tr == nil {
		return nil
	}
	paths := []string(nil)
	if receipt != nil {
		paths = append(paths, receipt.CitedFiles...)
		if len(paths) == 0 {
			paths = append(paths, receipt.KeyFiles...)
		}
	}
	if len(paths) > maxRebuildFiles {
		paths = paths[:maxRebuildFiles]
	}

	var b strings.Builder
	var kept []string
	remaining := maxRebuildTotalBytes
	for _, rel := range paths {
		if remaining <= 64 {
			break
		}
		body, ok := d.readRebuildFile(sess, task, rel)
		if !ok {
			continue
		}
		limit := maxRebuildFileBytes
		if remaining < limit {
			limit = remaining
		}
		body = truncateUTF8Bytes(body, limit)
		if strings.TrimSpace(body) == "" {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("REBUILT CONTEXT (post-compact; re-read, not new user input):\n")
		}
		fmt.Fprintf(&b, "--- %s ---\n%s\n", rel, body)
		remaining = maxRebuildTotalBytes - b.Len()
		kept = append(kept, rel)
	}
	tr.Rebuild = truncateUTF8Bytes(strings.TrimSpace(b.String()), maxRebuildTotalBytes)
	return kept
}

func (d *Daemon) readRebuildFile(sess *sessionstore.Session, task *scheduler.ExecutionRun, rel string) (string, bool) {
	rel, ok := rebuildRelPath(rel)
	if !ok || d == nil || sess == nil || task == nil {
		return "", false
	}
	abs := resolveIn(sess.WorkspaceRoot, rel)
	dec, err := d.fileReadDecision(sess, task, abs, nil)
	if err != nil || dec == nil || dec.Decision != "allowed" {
		return "", false
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return "", false
	}
	if _, isImage := sniffImageMediaType(content); isImage {
		return "", false
	}
	d.record(sess.SessionID, "FileRead", task.RunID, "go", map[string]any{
		"path": abs, "bytes": len(content), "source": "compact_rebuild",
	}, dec.DecisionID)
	d.recordRead(sess.SessionID, rel, string(content))
	return string(content), true
}
