package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/lsp"
)

// semanticServer is a language server invocation for a file type.
type semanticServer struct {
	bin    string
	args   []string
	langID string
}

func serverForExt(ext string) (semanticServer, bool) {
	switch strings.ToLower(ext) {
	case ".go":
		return semanticServer{bin: "gopls", args: nil, langID: "go"}, true
	case ".ts", ".tsx":
		return semanticServer{bin: "typescript-language-server", args: []string{"--stdio"}, langID: "typescript"}, true
	case ".py":
		return semanticServer{bin: "pyright-langserver", args: []string{"--stdio"}, langID: "python"}, true
	case ".rs":
		return semanticServer{bin: "rust-analyzer", args: nil, langID: "rust"}, true
	case ".c", ".h", ".cc", ".cpp", ".hpp", ".cxx":
		return semanticServer{bin: "clangd", args: nil, langID: "cpp"}, true
	case ".zig":
		return semanticServer{bin: "zls", args: nil, langID: "zig"}, true
	case ".rb":
		return semanticServer{bin: "solargraph", args: []string{"stdio"}, langID: "ruby"}, true
	default:
		return semanticServer{}, false
	}
}

// semanticDiagnostics returns error-severity semantic diagnostics for an edited
// file via a language server (type errors, undefined symbols — beyond what the
// syntax probe catches), or "" if no server is installed for the file type. This
// keeps the syntax probe as the always-available baseline and the LSP pass as an
// opportunistic upgrade. Best-effort with a short timeout.
func (d *Daemon) semanticDiagnostics(abspath, root string) string {
	srv, ok := serverForExt(filepath.Ext(abspath))
	if !ok {
		return ""
	}
	if _, err := exec.LookPath(srv.bin); err != nil {
		return "" // server not installed — the syntax probe already ran
	}
	content, err := os.ReadFile(abspath)
	if err != nil {
		return ""
	}
	diags, err := lsp.Diagnose(srv.bin, srv.args, root, abspath, srv.langID, string(content), 8*time.Second, d.lspEnv())
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, dg := range diags {
		if dg.Severity != "error" {
			continue // surface only errors to keep the feedback signal tight
		}
		fmt.Fprintf(&b, "%s:%d: %s\n", filepath.Base(abspath), dg.Line, dg.Message)
	}
	return strings.TrimSpace(b.String())
}
