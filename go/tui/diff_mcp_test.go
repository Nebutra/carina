package tui

import (
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestDiffOpensPagerAndRendersBoundaries(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"workspace.diff": map[string]any{"files": []any{map[string]any{"path": "a.txt", "status": " M", "binary": false, "truncated": false, "bytes": 10, "diff": "@@\n-old\n+new"}, map[string]any{"path": "b.bin", "status": "??", "binary": true, "truncated": false, "bytes": 3}}, "truncated": false, "total_bytes": 10, "limits": map[string]any{}}}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	m.locale = string(LocaleChinese)
	cmd := m.slashCommand("/diff")
	if cmd == nil || m.transcriptPager == nil || !m.transcriptPager.loading {
		t.Fatal("diff pager not opened")
	}
	if view := m.transcriptPagerView(m.width, m.height); !strings.Contains(view, "正在加载工作区差异") || strings.Contains(view, "规范会话记录") {
		t.Fatalf("diff loading state reused canonical copy: %q", view)
	}
	m.Update(cmd())
	if got := m.transcriptPager.text; !strings.Contains(got, "+new") || !strings.Contains(got, m.text(MsgDiffBinary, nil)) {
		t.Fatalf("pager=%q", got)
	}
}

func TestMCPVerboseIsReadOnlyInventory(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"mcp.inventory": map[string]any{
			"count": 1,
			"servers": []any{map[string]any{
				"name": "docs", "health": "connected", "prompts": 0,
				"tools": []any{map[string]any{"name": "search", "description": "docs only"}},
			}},
		},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.sessionID, m.call = "sess", fc
	cmd := m.slashCommand("/mcp verbose")
	if cmd == nil {
		t.Fatal("mcp command missing")
	}
	m.Update(cmd())
	if len(fc.calls) != 1 || fc.calls[0].method != "mcp.inventory" || fc.calls[0].params["verbose"] != true {
		t.Fatalf("calls=%#v", fc.calls)
	}
	if got := transcriptText(m); !strings.Contains(got, "docs") || !strings.Contains(got, "search") {
		t.Fatalf("inventory=%q", got)
	}
}
