package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServeMCPExposesKernelGatedTools drives Carina's MCP server end to end: an
// external client initializes, lists tools, reads a file (allowed), and attempts
// a blind overwrite (denied by the read-before-write guard) — proving the MCP
// surface inherits the capability kernel's gating.
func TestServeMCPExposesKernelGatedTools(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	sess, _ := d.store.CreateSession(ws, "safe-edit")
	d.kern.InitSessionWithPolicy(sess.SessionID, ws, "safe-edit", nil)
	if err := os.WriteFile(filepath.Join(ws, "hello.txt"), []byte("hi from carina\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// secret.txt exists but is never read by the client — a blind overwrite of it
	// must be refused.
	if err := os.WriteFile(filepath.Join(ws, "secret.txt"), []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reqs := []map[string]any{
		{"jsonrpc": "2.0", "id": 1, "method": "initialize"},
		{"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
		{"jsonrpc": "2.0", "id": 3, "method": "tools/call",
			"params": map[string]any{"name": "read", "arguments": map[string]any{"path": "hello.txt"}}},
		{"jsonrpc": "2.0", "id": 4, "method": "tools/call",
			"params": map[string]any{"name": "patch", "arguments": map[string]any{"path": "secret.txt", "content": "clobber\n"}}},
	}
	var in strings.Builder
	for _, r := range reqs {
		b, _ := json.Marshal(r)
		in.WriteString(string(b) + "\n")
	}
	var out strings.Builder
	if err := d.ServeMCP(context.Background(), sess.SessionID, strings.NewReader(in.String()), &out); err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}

	byID := map[float64]map[string]any{}
	sc := bufio.NewScanner(strings.NewReader(out.String()))
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("bad response %q: %v", sc.Text(), err)
		}
		if id, ok := m["id"].(float64); ok {
			byID[id] = m
		}
	}

	// tools/list advertises the catalog.
	tools := byID[2]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != len(carinaToolCatalog) {
		t.Fatalf("expected %d tools, got %d", len(carinaToolCatalog), len(tools))
	}

	// read returns the file contents (capability allowed).
	readText := toolText(t, byID[3])
	if !strings.Contains(readText, "hi from carina") {
		t.Fatalf("read did not return file content: %q", readText)
	}

	// A blind overwrite of an unread file is refused by the provenance guard —
	// the kernel gating reaches the MCP surface.
	patchText := toolText(t, byID[4])
	if !strings.Contains(patchText, "DENIED") {
		t.Fatalf("blind overwrite should be DENIED, got: %q", patchText)
	}
	if got := string(mustReadFile(t, filepath.Join(ws, "secret.txt"))); got != "keep me\n" {
		t.Fatalf("denied patch must not have written: %q", got)
	}
}

func toolText(t *testing.T, resp map[string]any) string {
	t.Helper()
	res, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result in %+v", resp)
	}
	content := res["content"].([]any)
	return content[0].(map[string]any)["text"].(string)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
