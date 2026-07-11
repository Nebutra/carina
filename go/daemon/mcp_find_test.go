package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/mcp"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

// findMockServerPy is a minimal MCP server over stdio JSON-RPC advertising two
// tools with descriptions and input schemas, so mcp_find rendering can be
// tested end to end against a real subprocess (same harness style as
// go/mcp/mcp_test.go).
const findMockServerPy = `import sys, json
tools = [
    {"name": "create_ticket", "description": "create an issue tracker ticket", "inputSchema": {"type": "object", "properties": {"title": {"type": "string", "description": "ticket title"}, "priority": {"type": "string"}}}},
    {"name": "close_ticket", "description": "close an existing ticket", "inputSchema": {"type": "object", "properties": {"ticket_id": {"type": "string"}}}},
]
for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        continue
    mid = msg.get("id")
    method = msg.get("method")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"mock"},"capabilities":{}}})+"\n")
        sys.stdout.flush()
    elif method == "tools/list":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"tools":tools}})+"\n")
        sys.stdout.flush()
    elif method == "prompts/list":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"prompts":[]}})+"\n")
        sys.stdout.flush()
    elif method and method.startswith("notifications/"):
        pass
    elif mid is not None:
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"error":{"code":-32601,"message":"method not found"}})+"\n")
        sys.stdout.flush()
`

func mcpFindFixture(t *testing.T) (*Daemon, *sessionstore.Session, *scheduler.Task) {
	t.Helper()
	d := &Daemon{mcp: mcp.NewManager()}
	t.Cleanup(d.mcp.Close)
	return d, &sessionstore.Session{SessionID: "sess_find"}, &scheduler.Task{TaskID: "task_find"}
}

func connectFindMock(t *testing.T, d *Daemon, name string, private bool) {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "find_mock.py")
	if err := os.WriteFile(script, []byte(findMockServerPy), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := mcp.Server{Command: "python3", Args: []string{script}}
	var err error
	if private {
		err = d.mcp.ConnectPrivate(name, srv)
	} else {
		err = d.mcp.Connect(name, srv)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestMCPFindOutcomeRejectsEmptyQuery(t *testing.T) {
	d, sess, task := mcpFindFixture(t)
	for _, q := range []string{"", "   "} {
		out := d.mcpFindOutcome(sess, task, &action{Tool: "mcp_find", Query: q})
		if out.status != "failed" || out.errorCategory != "invalid_arguments" {
			t.Fatalf("query %q should fail with invalid_arguments, got %+v", q, out)
		}
		if !strings.Contains(out.display, "mcp_find needs query") {
			t.Fatalf("unexpected error display: %q", out.display)
		}
	}
}

func TestMCPFindOutcomeNoServersNotesDisconnection(t *testing.T) {
	d, sess, task := mcpFindFixture(t)
	out := d.mcpFindOutcome(sess, task, &action{Tool: "mcp_find", Query: "ticket"})
	if out.status != "completed" {
		t.Fatalf("no-match should still complete (it is an answer, not an error), got %+v", out)
	}
	if !strings.Contains(out.display, "no MCP tools matched") || !strings.Contains(out.display, "disconnected") {
		t.Fatalf("no-match message should mention possible disconnection: %q", out.display)
	}
}

func TestMCPFindOutcomeRendersMatchesWithSchemas(t *testing.T) {
	d, sess, task := mcpFindFixture(t)
	connectFindMock(t, d, "tracker", false)

	out := d.mcpFindOutcome(sess, task, &action{Tool: "mcp_find", Query: "create ticket"})
	if out.status != "completed" {
		t.Fatalf("expected completed outcome, got %+v", out)
	}
	// Both tools match "ticket"; the name+description match ranks first.
	first := strings.Index(out.display, "mcp__tracker__create_ticket")
	second := strings.Index(out.display, "mcp__tracker__close_ticket")
	if first < 0 || second < 0 || first > second {
		t.Fatalf("expected create_ticket ranked above close_ticket:\n%s", out.display)
	}
	for _, want := range []string{
		"create an issue tracker ticket", // description
		`"title"`,                        // full input schema surfaced
		`"ticket title"`,                 // schema property description
		`"tool":"mcp"`,                   // invocation hint
	} {
		if !strings.Contains(out.display, want) {
			t.Fatalf("rendered matches missing %s:\n%s", want, out.display)
		}
	}

	// A query with no overlap yields the disconnected/no-match note.
	miss := d.mcpFindOutcome(sess, task, &action{Tool: "mcp_find", Query: "quantum teleporter"})
	if miss.status != "completed" || !strings.Contains(miss.display, "no MCP tools matched") {
		t.Fatalf("unexpected no-match outcome: %+v", miss)
	}
}

func TestMCPFindOutcomeExcludesPrivateServers(t *testing.T) {
	d, sess, task := mcpFindFixture(t)
	connectFindMock(t, d, "internal", true)

	out := d.mcpFindOutcome(sess, task, &action{Tool: "mcp_find", Query: "ticket"})
	if out.status != "completed" {
		t.Fatalf("expected completed outcome, got %+v", out)
	}
	if strings.Contains(out.display, "internal") || !strings.Contains(out.display, "no MCP tools matched") {
		t.Fatalf("private server metadata must not leak through mcp_find:\n%s", out.display)
	}
}
