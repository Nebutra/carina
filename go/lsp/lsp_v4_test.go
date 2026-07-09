package lsp

// V4 D2 tests (docs/plans/code-intelligence.md): Diagnose/collect must speak
// percent-encoded file:// URIs (PathToURI) and match publishDiagnostics
// through symlink-canonical path comparison (URIToPath + EvalSymlinks) —
// the treatment the Session path got in V3. Real servers (gopls, pyright)
// percent-encode the URIs they emit and canonicalize paths, so the current
// raw "file://"+path concatenation and exact string compare lose their
// diagnostics on spaces, CJK, and symlinked roots (/tmp vs /private/tmp).

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// diagPublishServer is a mock LSP server: answers initialize, then on
// didOpen publishes one error diagnostic under pubURI (which a real server
// derives itself — encoded, canonicalized — rather than echoing the client).
type diagPublishServer struct {
	pubURI string
}

func (m *diagPublishServer) run(r io.Reader, w io.Writer) {
	br := bufio.NewReader(r)
	for {
		raw, err := readMsg(br)
		if err != nil {
			return
		}
		var msg struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		_ = json.Unmarshal(raw, &msg)
		switch msg.Method {
		case "initialize":
			_ = writeMsg(w, map[string]any{
				"jsonrpc": "2.0", "id": *msg.ID,
				"result": map[string]any{"capabilities": map[string]any{}},
			})
		case "textDocument/didOpen":
			_ = writeMsg(w, map[string]any{
				"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri": m.pubURI,
					"diagnostics": []any{map[string]any{
						"severity": 1,
						"message":  "boom",
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
						},
					}},
				},
			})
		}
	}
}

// TestCollectMatchesPercentEncodedDiagnosticsURI: the server publishes the
// percent-encoded (and canonicalized) URI for a space+CJK path; collect must
// still match it against the file it opened.
func TestCollectMatchesPercentEncodedDiagnosticsURI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "你好.go")
	if err := os.WriteFile(path, []byte("package m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	canon, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}

	cR, sW := io.Pipe() // server -> client
	sR, cW := io.Pipe() // client -> server
	go (&diagPublishServer{pubURI: PathToURI(canon)}).run(sR, sW)

	diags, err := collect(cW, cR, "file://"+filepath.Dir(path), "file://"+path, "go",
		"package m\n", 3*time.Second)
	if err != nil {
		t.Fatalf("a percent-encoded published URI must match the opened file: %v", err)
	}
	if len(diags) != 1 || diags[0].Severity != "error" {
		t.Fatalf("expected the published diagnostic, got %+v", diags)
	}
}

// TestCollectMatchesSymlinkCanonicalDiagnosticsURI: the file opens via a
// symlinked root while the server answers with the canonical path — the
// filterWorkspaceLocations treatment must apply to diagnostics matching too.
func TestCollectMatchesSymlinkCanonicalDiagnosticsURI(t *testing.T) {
	real := t.TempDir()
	canon, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "rootlink")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.WriteFile(filepath.Join(real, "m.go"), []byte("package m\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cR, sW := io.Pipe()
	sR, cW := io.Pipe()
	go (&diagPublishServer{pubURI: PathToURI(filepath.Join(canon, "m.go"))}).run(sR, sW)

	diags, err := collect(cW, cR, "file://"+link, "file://"+filepath.Join(link, "m.go"), "go",
		"package m\n", 3*time.Second)
	if err != nil {
		t.Fatalf("a canonical-path published URI must match a symlinked open: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected the published diagnostic, got %+v", diags)
	}
}

// TestFakeDiagnoseServerHelper is not a real test: re-exec'ed with
// CARINA_FAKE_DIAGNOSE=1 it behaves like a real language server — it decodes
// the didOpen URI, canonicalizes the path, and publishes one diagnostic
// under the encoded canonical URI, echoing the received didOpen URI in the
// diagnostic message so the caller can assert outbound encoding.
func TestFakeDiagnoseServerHelper(t *testing.T) {
	if os.Getenv("CARINA_FAKE_DIAGNOSE") != "1" {
		t.Skip("helper process, spawned by TestDiagnoseSpeaksEncodedCanonicalURIs")
	}
	runFakeDiagnoseServer(os.Stdin, os.Stdout)
	os.Exit(0)
}

func runFakeDiagnoseServer(r io.Reader, w io.Writer) {
	br := bufio.NewReader(r)
	for {
		raw, err := readMsg(br)
		if err != nil {
			return
		}
		var msg struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(raw, &msg)
		switch msg.Method {
		case "initialize":
			_ = writeMsg(w, map[string]any{
				"jsonrpc": "2.0", "id": *msg.ID,
				"result": map[string]any{"capabilities": map[string]any{}},
			})
		case "textDocument/didOpen":
			var p struct {
				TextDocument struct {
					URI string `json:"uri"`
				} `json:"textDocument"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			path, ok := URIToPath(p.TextDocument.URI)
			if !ok {
				return
			}
			if canon, err := filepath.EvalSymlinks(path); err == nil {
				path = canon
			}
			_ = writeMsg(w, map[string]any{
				"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics",
				"params": map[string]any{
					"uri": PathToURI(path),
					"diagnostics": []any{map[string]any{
						"severity": 1,
						"message":  "didOpen=" + p.TextDocument.URI,
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 0},
						},
					}},
				},
			})
		}
	}
}

// TestDiagnoseSpeaksEncodedCanonicalURIs: end-to-end over a spawned
// real-server-shaped fake — Diagnose must send a percent-encoded didOpen URI
// and match the encoded, symlink-canonical publishDiagnostics answer for a
// space+CJK path under a canonicalizing temp root.
func TestDiagnoseSpeaksEncodedCanonicalURIs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "你好.go")
	if err := os.WriteFile(path, []byte("package m\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "CARINA_FAKE_DIAGNOSE=1")

	diags, err := Diagnose(os.Args[0], []string{"-test.run=^TestFakeDiagnoseServerHelper$"},
		filepath.Dir(path), path, "go", "package m\n", 4*time.Second, env)
	if err != nil {
		t.Fatalf("Diagnose must receive the canonically-published diagnostics: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %+v", diags)
	}
	want := "didOpen=" + PathToURI(path)
	if diags[0].Message != want {
		t.Fatalf("didOpen must carry the percent-encoded URI: got %q, want %q", diags[0].Message, want)
	}
}
