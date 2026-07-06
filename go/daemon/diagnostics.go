package daemon

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"path/filepath"
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
