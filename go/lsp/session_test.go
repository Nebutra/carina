package lsp

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// defRefServer is a minimal LSP server for session tests: it answers
// initialize, swallows notifications, and serves canned definition/references
// responses. It records the includeDeclaration flag it was asked for.
type defRefServer struct {
	includeDecl *bool
	sawDidOpen  bool
}

func (m *defRefServer) run(r io.Reader, w io.Writer) {
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
			m.sawDidOpen = true
		case "textDocument/definition":
			_ = writeMsg(w, map[string]any{
				"jsonrpc": "2.0", "id": *msg.ID,
				"result": []any{map[string]any{
					"uri": "file:///root/def.go",
					"range": map[string]any{
						"start": map[string]any{"line": 4, "character": 2},
						"end":   map[string]any{"line": 4, "character": 10},
					},
				}},
			})
		case "textDocument/references":
			var p struct {
				Context struct {
					IncludeDeclaration bool `json:"includeDeclaration"`
				} `json:"context"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			m.includeDecl = &p.Context.IncludeDeclaration
			_ = writeMsg(w, map[string]any{
				"jsonrpc": "2.0", "id": *msg.ID,
				"result": []any{
					map[string]any{
						"uri": "file:///root/use_a.go",
						"range": map[string]any{
							"start": map[string]any{"line": 9, "character": 0},
							"end":   map[string]any{"line": 9, "character": 6},
						},
					},
					map[string]any{
						"uri": "file:///root/use_b.go",
						"range": map[string]any{
							"start": map[string]any{"line": 0, "character": 8},
							"end":   map[string]any{"line": 0, "character": 14},
						},
					},
				},
			})
		case "shutdown":
			if msg.ID != nil {
				_ = writeMsg(w, map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": nil})
			}
		}
	}
}

func newMockSession(t *testing.T) (*Session, *defRefServer) {
	t.Helper()
	cR, sW := io.Pipe() // server -> client
	sR, cW := io.Pipe() // client -> server
	srv := &defRefServer{}
	go srv.run(sR, sW)
	sess, err := newSession(cW, cR, "file:///root", 3*time.Second)
	if err != nil {
		t.Fatalf("newSession over mock streams: %v", err)
	}
	return sess, srv
}

// TestSessionDefinitionOverMockStreams: initialize + didOpen + definition,
// with LSP 0-based positions converted to 1-based Locations.
func TestSessionDefinitionOverMockStreams(t *testing.T) {
	sess, srv := newMockSession(t)
	defer sess.Close()
	if err := sess.DidOpen("/root/m.go", "go", "package m\nfunc target() {}\n"); err != nil {
		t.Fatalf("didOpen: %v", err)
	}
	locs, err := sess.Definition("/root/m.go", 2, 6)
	if err != nil {
		t.Fatalf("definition: %v", err)
	}
	if !srv.sawDidOpen {
		t.Fatal("the server must see didOpen before queries")
	}
	if len(locs) != 1 {
		t.Fatalf("expected 1 location, got %+v", locs)
	}
	want := Location{Path: "/root/def.go", Line: 5, Char: 3} // wire 4/2 -> 1-based 5/3
	if locs[0] != want {
		t.Fatalf("definition location = %+v, want %+v", locs[0], want)
	}
}

// TestSessionReferencesOverMockStreams: references carry includeDeclaration
// and every returned site converts to a 1-based Location.
func TestSessionReferencesOverMockStreams(t *testing.T) {
	sess, srv := newMockSession(t)
	defer sess.Close()
	if err := sess.DidOpen("/root/m.go", "go", "package m\nfunc target() {}\n"); err != nil {
		t.Fatalf("didOpen: %v", err)
	}
	locs, err := sess.References("/root/m.go", 2, 6, false)
	if err != nil {
		t.Fatalf("references: %v", err)
	}
	if srv.includeDecl == nil || *srv.includeDecl != false {
		t.Fatalf("includeDeclaration=false must reach the server, got %v", srv.includeDecl)
	}
	want := []Location{
		{Path: "/root/use_a.go", Line: 10, Char: 1},
		{Path: "/root/use_b.go", Line: 1, Char: 9},
	}
	if len(locs) != len(want) {
		t.Fatalf("expected %d locations, got %+v", len(want), locs)
	}
	for i := range want {
		if locs[i] != want[i] {
			t.Fatalf("reference[%d] = %+v, want %+v", i, locs[i], want[i])
		}
	}
}

// TestStartSessionMissingBinary: an absent server binary is a clean,
// descriptive error — the daemon's degrade path keys off it.
func TestStartSessionMissingBinary(t *testing.T) {
	_, err := StartSession("definitely-not-a-real-lsp-binary", nil, "/root", time.Second, nil)
	if err == nil {
		t.Fatal("a missing server binary must return an error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error must say the binary was not found, got: %v", err)
	}
}
