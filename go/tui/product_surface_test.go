package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestTypedPresentationOmitsThoughtAndRawJSON(t *testing.T) {
	th := theme.New(theme.Mono)
	ev := map[string]any{
		"type":      "ModelResponded",
		"timestamp": "2026-07-09T10:11:12Z",
		"payload": map[string]any{
			"text": `{"thought":"private hidden reasoning","tool":"run","command":["go","test","./..."]}`,
		},
	}
	presentation := presentEvent(ev, th, "en")
	presentation.Collapsed = false
	line := presentation.render(th, 120)
	for _, forbidden := range []string{"private hidden reasoning", `"thought"`, `"tool"`} {
		if strings.Contains(line, forbidden) {
			t.Fatalf("typed presentation leaked %q: %s", forbidden, line)
		}
	}
	for _, want := range []string{"agent", "selected run", "$ go test ./..."} {
		if !strings.Contains(line, want) {
			t.Fatalf("typed presentation missing %q: %s", want, line)
		}
	}
}

func TestLastVerboseEventCanBeFoldedAndExpanded(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.Update(EventMsg{Raw: map[string]any{
		"type": "CommandOutput",
		"payload": map[string]any{
			"stream": "stdout",
			"chunk":  "first line\nsecond line",
		},
	}})
	before := transcriptText(m)
	if !strings.Contains(before, "[+2]") || strings.Contains(before, "second line") {
		t.Fatalf("command output should start folded:\n%s", before)
	}
	if _, handled := m.handleKey("ctrl+o"); !handled {
		t.Fatal("ctrl+o did not toggle the latest fold")
	}
	after := transcriptText(m)
	if !strings.Contains(after, "[open]") || !strings.Contains(after, "second line") {
		t.Fatalf("command output did not expand:\n%s", after)
	}
}

func TestTaskGraphBuildsSubagentAndWorkflowHierarchy(t *testing.T) {
	var graph taskGraph
	graph.observeEvent(map[string]any{
		"type": "TaskCreated", "task_id": "task_main",
		"payload": map[string]any{"task_id": "task_main", "user_prompt": "build release"},
	})
	graph.observeEvent(map[string]any{
		"type": "ToolApproved", "task_id": "task_main",
		"payload": map[string]any{"spawn_agent": "reviewer", "child_session": "sess_child"},
	})
	graph.observeEvent(map[string]any{
		"type": "TaskCreated", "task_id": "task_main",
		"payload": map[string]any{"status": "workflow_started", "workflow": "release", "run_id": "wf_1"},
	})
	graph.observeEvent(map[string]any{
		"type": "ToolApproved", "task_id": "task_main",
		"payload": map[string]any{"workflow": "release", "run_id": "wf_1", "step": "test", "agent": "qa"},
	})
	text := strings.Join(graph.lines(theme.New(theme.Mono), 80, 8), "\n")
	for _, want := range []string{"subagent", "reviewer", "workflow", "release", "step", "test qa", "  `-"} {
		if !strings.Contains(text, want) {
			t.Fatalf("task tree missing %q:\n%s", want, text)
		}
	}

	graph.observeEvent(map[string]any{
		"type": "ModelResponded", "task_id": "task_main",
		"payload": map[string]any{"status": "workflow_completed", "workflow": "release", "run_id": "wf_1"},
	})
	if graph.nodes["wf_1"].Status != "completed" || graph.nodes["wf_1:test"].Status != "completed" {
		t.Fatalf("workflow completion did not fold into children: %#v %#v", graph.nodes["wf_1"], graph.nodes["wf_1:test"])
	}
}

func TestNarrowCJKAndANSIViewDoesNotOverflow(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.ANSI256), Locale: "zh", Socket: "/tmp/s.sock"})
	m.Update(tea.WindowSizeMsg{Width: 32, Height: 18})
	m.Update(EventMsg{Raw: map[string]any{
		"type":      "CommandOutput",
		"timestamp": "2026-07-09T10:11:12Z",
		"task_id":   "任务_很长的标识符",
		"payload": map[string]any{
			"stream": "stdout",
			"chunk":  "你好世界你好世界你好世界\x1b[31m红色\x1b[0m and a very long suffix",
		},
	}})
	view := m.View().Content
	if strings.Contains(ansi.Strip(view), "\x1b[31m") {
		t.Fatal("attacker ANSI survived as visible content")
	}
	for i, line := range strings.Split(view, "\n") {
		if width := ansi.StringWidth(line); width > 32 {
			t.Fatalf("line %d width=%d exceeds terminal width 32: %q", i, width, line)
		}
	}
}
