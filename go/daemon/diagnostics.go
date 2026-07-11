package daemon

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// checkEdited runs a fast language-appropriate syntax/type check on a file the
// agent just edited and returns any diagnostics (empty if clean or the checker
// is unavailable). This is the post-edit diagnostics-delta feedback loop: the
// agent immediately sees compile/parse errors it introduced, instead of finding
// out turns later. Stage 1 uses per-language probes; a full LSP integration
// (gopls/tsserver/…) is a later stage.
func checkEdited(abspath string) string {
	var argv []string
	stdoutToErr := false
	switch strings.ToLower(filepath.Ext(abspath)) {
	case ".go":
		argv = []string{"gofmt", "-e", abspath} // parse-checks; errors on stderr
		stdoutToErr = true                      // discard the reformatted source
	case ".py":
		argv = []string{"python3", "-m", "py_compile", abspath}
	case ".js", ".mjs", ".cjs":
		argv = []string{"node", "--check", abspath}
	case ".rs":
		argv = []string{"rustc", "--edition", "2021", "--emit", "metadata", "-o", "/dev/null", abspath}
	default:
		return ""
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return "" // checker not installed — no diagnostics rather than a false error
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if stdoutToErr {
		cmd.Stdout = io.Discard
	}
	if err := cmd.Run(); err == nil {
		return "" // clean
	}
	return strings.TrimSpace(stderr.String())
}

// diagnosticHeaderRe matches the start of a new diagnostic block: a location
// line identifying (file, line[, col]) in either shape our two
// line-anchored checkers emit as their true first line —
//   - "path/to/file.go:12:3: message"        (gofmt, node --check)
//   - "  File \"path/to/file.py\", line 12"  (py_compile)
//
// rustc's block starts with an "error[E...]: message" summary line that has
// no location on it (the "--> path:line:col" comes on the line below, mid-
// block) — deliberately NOT matched here, since anchoring on it would split
// one rustc diagnostic into two block fragments instead of grouping it as a
// unit. Lines that match neither shape above are continuation lines (source
// snippets, carets, summary lines) and stay attached to the most recent
// header (or, for rustc, the whole diagnostic collapses into one
// undifferentiated block per checkEdited call — coarser than gofmt/python,
// but still grouped correctly rather than fragmented).
var diagnosticHeaderRe = regexp.MustCompile(`(?i)^(\S+:\d+(:\d+)?:?|\s*File\s+".*",\s*line\s+\d+)`)

// diagnosticLocTokenRe strips line[:col] location numbers — "path:12:3:",
// "path:12:", or "line 12" — from a header so the match key is the
// diagnostic's message/path content, not the position that shifts whenever
// an edit inserts or removes an unrelated line elsewhere in the file.
var diagnosticLocTokenRe = regexp.MustCompile(`(?i):\d+(:\d+)?:?|\bline\s+\d+\b`)

// diagnosticBlocks splits a raw checker dump into one block per diagnostic: a
// header line plus any following continuation lines, up to the next header.
func diagnosticBlocks(s string) []string {
	var blocks []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, line := range strings.Split(s, "\n") {
		if diagnosticHeaderRe.MatchString(line) {
			flush()
		}
		if line == "" && len(cur) == 0 {
			continue // skip blank separators between blocks
		}
		cur = append(cur, line)
	}
	flush()
	return blocks
}

// diagnosticKey is a block's match key for the before/after diff: the header
// line with its line[:col] location stripped (replaced with a stable
// separator), plus any continuation lines verbatim.
func diagnosticKey(block string) string {
	lines := strings.SplitN(block, "\n", 2)
	head := strings.TrimSpace(diagnosticLocTokenRe.ReplaceAllString(lines[0], ":"))
	if len(lines) == 1 {
		return head
	}
	return head + "\n" + lines[1]
}

// diagnosticsDelta returns only the diagnostic blocks present in `after`
// that were not already present in `before`, matched by diagnosticKey (i.e.
// by message content, not line position). This is the pre/post half of the
// diagnostics feedback loop: checkEdited alone only reports the post-edit
// state, so an edit that shifts unrelated line numbers elsewhere in the file
// (a very common case — inserting or removing any line above an existing
// error) would otherwise make that pre-existing error look newly introduced
// by the edit, which is the opposite of the intended signal. Call
// checkEdited before applying an edit and again after, then pass both
// outputs here; the caller reports only the delta as newly introduced.
//
// Matching is a multiset diff (counts, not just set membership), so if the
// same message legitimately appears more times after the edit than before,
// the extra occurrences are still reported as new.
func diagnosticsDelta(before, after string) string {
	after = strings.TrimSpace(after)
	if after == "" {
		return ""
	}
	remaining := map[string]int{}
	for _, b := range diagnosticBlocks(before) {
		remaining[diagnosticKey(b)]++
	}
	var newBlocks []string
	for _, b := range diagnosticBlocks(after) {
		k := diagnosticKey(b)
		if remaining[k] > 0 {
			remaining[k]--
			continue // matched a pre-existing diagnostic (line may have shifted) — not new
		}
		newBlocks = append(newBlocks, b)
	}
	return strings.Join(newBlocks, "\n")
}
