package lsp

import (
	"bufio"
	"encoding/json"
	"io"
	"testing"
	"time"
)

// mockServer is a minimal LSP server: answers initialize, then on didOpen
// publishes one error diagnostic for the opened file.
func mockServer(r io.Reader, w io.Writer) {
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
					"uri": "file:///root/m.go",
					"diagnostics": []any{map[string]any{
						"severity": 1,
						"message":  `cannot use "oops" (untyped string) as int value`,
						"range": map[string]any{
							"start": map[string]any{"line": 1, "character": 8},
						},
					}},
				},
			})
		}
	}
}

// TestCollectDiagnosticsHandshake drives the full LSP handshake against a mock
// server over in-memory pipes — no real language server needed.
func TestCollectDiagnosticsHandshake(t *testing.T) {
	cR, sW := io.Pipe() // server -> client
	sR, cW := io.Pipe() // client -> server
	go mockServer(sR, sW)

	diags, err := collect(cW, cR, "file:///root", "file:///root/m.go", "go",
		"package m\nvar x int = \"oops\"\n", 3*time.Second)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 diagnostic, got %d: %+v", len(diags), diags)
	}
	d := diags[0]
	if d.Severity != "error" {
		t.Fatalf("expected error severity, got %q", d.Severity)
	}
	if d.Line != 2 { // LSP line 1 (0-based) -> 1-based line 2
		t.Fatalf("expected 1-based line 2, got %d", d.Line)
	}
	if d.Message == "" {
		t.Fatal("diagnostic message should be populated")
	}
}

// TestDiagnoseMissingServer: an absent server is a clean error, not a panic.
func TestDiagnoseMissingServer(t *testing.T) {
	_, err := Diagnose("definitely-not-a-real-lsp-binary", nil, "/root", "/root/m.go", "go", "package m\n", time.Second)
	if err == nil {
		t.Fatal("a missing server binary must return an error")
	}
}

// TestCollectTimeout: if no diagnostics arrive, collect times out cleanly.
func TestCollectTimeout(t *testing.T) {
	cR, sW := io.Pipe()
	sR, cW := io.Pipe()
	// A server that only answers initialize and never publishes diagnostics.
	go func() {
		br := bufio.NewReader(sR)
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
			if msg.Method == "initialize" {
				_ = writeMsg(sW, map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{}})
			}
		}
	}()
	if _, err := collect(cW, cR, "file:///root", "file:///root/m.go", "go", "package m\n", 200*time.Millisecond); err == nil {
		t.Fatal("collect should time out when no diagnostics are published")
	}
}
