package lsp

// V3 D3 tests: real language servers percent-encode the file:// URIs they
// emit and expect encoded URIs in requests. PathToURI/URIToPath must
// round-trip space, CJK, and reserved-char paths; parseLocations must decode
// percent-encoded URIs; and the session must send encoded URIs in didOpen and
// position params (mock-stream test).

import (
	"bufio"
	"encoding/json"
	"io"
	"testing"
	"time"
)

func TestPathToURIEncodesSegments(t *testing.T) {
	cases := []struct{ path, want string }{
		{"/ws/plain/a.rs", "file:///ws/plain/a.rs"},
		{"/ws/a b/main.rs", "file:///ws/a%20b/main.rs"},
		{"/ws/你好.rs", "file:///ws/%E4%BD%A0%E5%A5%BD.rs"},
		{"/ws/50%.rs", "file:///ws/50%25.rs"},
		{"/ws/q?.rs", "file:///ws/q%3F.rs"},
		{"/ws/f#1.rs", "file:///ws/f%231.rs"},
	}
	for _, c := range cases {
		if got := PathToURI(c.path); got != c.want {
			t.Errorf("PathToURI(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestURIToPathDecodes(t *testing.T) {
	cases := []struct{ uri, want string }{
		{"file:///ws/plain/a.rs", "/ws/plain/a.rs"},
		{"file:///ws/a%20b/main.rs", "/ws/a b/main.rs"},
		{"file:///ws/%E4%BD%A0%E5%A5%BD.rs", "/ws/你好.rs"},
		{"file:///ws/50%25.rs", "/ws/50%.rs"},
	}
	for _, c := range cases {
		got, ok := URIToPath(c.uri)
		if !ok || got != c.want {
			t.Errorf("URIToPath(%q) = (%q, %v), want (%q, true)", c.uri, got, ok, c.want)
		}
	}
}

func TestURIToPathRejectsNonFileSchemes(t *testing.T) {
	for _, uri := range []string{
		"https://example.com/a.rs",
		"untitled:Untitled-1",
		"vscode-notebook-cell:/ws/nb.ipynb#cell",
		"",
	} {
		if p, ok := URIToPath(uri); ok {
			t.Errorf("URIToPath(%q) must reject non-file schemes, got (%q, true)", uri, p)
		}
	}
}

func TestURIRoundTripsSpaceCJKAndReservedChars(t *testing.T) {
	for _, path := range []string{
		"/ws/a b/main.rs",
		"/ws/你好/世界.rs",
		"/ws/emoji 😀/x.rs",
		"/ws/50%/f#1?.rs",
		"/private/tmp/link ws/深/a b.rs",
	} {
		uri := PathToURI(path)
		got, ok := URIToPath(uri)
		if !ok || got != path {
			t.Errorf("round trip %q -> %q -> (%q, %v)", path, uri, got, ok)
		}
	}
}

func TestParseLocationsDecodesPercentEncodedURIs(t *testing.T) {
	raw := json.RawMessage(`[
		{"uri":"file:///ws/a%20b/%E4%BD%A0%E5%A5%BD.rs",
		 "range":{"start":{"line":0,"character":2},"end":{"line":0,"character":9}}},
		{"targetUri":"file:///ws/50%25.rs",
		 "targetRange":{"start":{"line":4,"character":0},"end":{"line":4,"character":3}}}
	]`)
	locs, err := parseLocations(raw)
	if err != nil {
		t.Fatalf("parseLocations: %v", err)
	}
	if len(locs) != 2 {
		t.Fatalf("expected 2 locations, got %+v", locs)
	}
	if locs[0].Path != "/ws/a b/你好.rs" || locs[0].Line != 1 || locs[0].Char != 3 {
		t.Fatalf("location[0] = %+v, want decoded /ws/a b/你好.rs:1:3", locs[0])
	}
	if locs[1].Path != "/ws/50%.rs" {
		t.Fatalf("location[1] = %+v, want decoded /ws/50%%.rs", locs[1])
	}
}

// uriRecordingServer answers initialize and records the textDocument URIs the
// client sends in didOpen and definition requests.
type uriRecordingServer struct {
	didOpenURI string
	queryURI   string
}

func (m *uriRecordingServer) run(r io.Reader, w io.Writer) {
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
		var p struct {
			TextDocument struct {
				URI string `json:"uri"`
			} `json:"textDocument"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		switch msg.Method {
		case "initialize":
			_ = writeMsg(w, map[string]any{
				"jsonrpc": "2.0", "id": *msg.ID,
				"result": map[string]any{"capabilities": map[string]any{}},
			})
		case "textDocument/didOpen":
			m.didOpenURI = p.TextDocument.URI
		case "textDocument/definition":
			m.queryURI = p.TextDocument.URI
			_ = writeMsg(w, map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": []any{}})
		case "shutdown":
			if msg.ID != nil {
				_ = writeMsg(w, map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": nil})
			}
		}
	}
}

// TestSessionSendsPercentEncodedURIs: didOpen and position params must carry
// percent-encoded file:// URIs — a real server 400s (or silently misses) on
// raw spaces and CJK bytes.
func TestSessionSendsPercentEncodedURIs(t *testing.T) {
	cR, sW := io.Pipe() // server -> client
	sR, cW := io.Pipe() // client -> server
	srv := &uriRecordingServer{}
	go srv.run(sR, sW)
	sess, err := newSession(cW, cR, PathToURI("/root/a b"), 3*time.Second)
	if err != nil {
		t.Fatalf("newSession: %v", err)
	}
	defer sess.Close()

	path := "/root/a b/你.go"
	want := "file:///root/a%20b/%E4%BD%A0.go"
	if err := sess.DidOpen(path, "go", "package m\n"); err != nil {
		t.Fatalf("didOpen: %v", err)
	}
	if _, err := sess.Definition(path, 1, 1); err != nil {
		t.Fatalf("definition: %v", err)
	}
	if srv.didOpenURI != want {
		t.Fatalf("didOpen uri = %q, want %q", srv.didOpenURI, want)
	}
	if srv.queryURI != want {
		t.Fatalf("definition uri = %q, want %q", srv.queryURI, want)
	}
}
