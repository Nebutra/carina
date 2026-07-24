package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

func TestConversationRetainedFrameStaysBoundedAtProductSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{40, 10}, {80, 24}, {120, 40}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			m := New(Options{Theme: theme.New(theme.Mono), Locale: "zh"})
			defer m.Close()
			m.workspaceRoot = "/tmp/carina-product"
			m.push("助手正在处理包含中文和 emoji 的长会话内容")
			m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
			view := m.View().Content
			if m.componentFrame.Root.ID != conversationScreenID || m.componentFrame.Generation == 0 {
				t.Fatalf("conversation frame = %#v", m.componentFrame)
			}
			assertNodeWithin(t, m.componentFrame.Root, ui.Rect{Width: size.width, Height: size.height})
			for _, line := range strings.Split(view, "\n") {
				if got := ansi.StringWidth(line); got > size.width {
					t.Fatalf("rendered line width=%d > %d: %q", got, size.width, ansi.Strip(line))
				}
			}
			if len(strings.Split(view, "\n")) > size.height {
				t.Fatalf("rendered height exceeds %d", size.height)
			}
		})
	}
}

func assertNodeWithin(t *testing.T, node ui.Node, viewport ui.Rect) {
	t.Helper()
	if !node.Bounds.Empty() && node.Bounds.Intersect(viewport) != node.Bounds {
		t.Fatalf("node %q escaped viewport: bounds=%+v viewport=%+v", node.ID, node.Bounds, viewport)
	}
	for _, child := range node.Children {
		assertNodeWithin(t, child, viewport)
	}
}

func TestConversationSurfaceUsesOnePersistentFrame(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 18})
	m.push("assistant response")

	view := m.View().Content
	if got := strings.Count(view, "╭"); got != 1 {
		t.Fatalf("persistent top borders = %d, want composer only:\n%s", got, view)
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "assistant response") {
			if !strings.HasPrefix(line, " ") || strings.HasPrefix(line, "│") {
				t.Fatalf("transcript should be padded, not framed: %q", line)
			}
			return
		}
	}
	t.Fatal("transcript content missing")
}

func TestStatusFooterDegradesByPriorityWithoutOverflow(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.model = "openai/gpt-5"
	m.reasoningEffort = "high"
	m.runtime = runtimeStatus{Profile: "safe-edit", Sandbox: "on", ContextAvailable: true, ContextPercent: 42, ContextLimit: 1000}
	m.conversation.Readiness = readinessReady

	wide := ansi.Strip(m.statusFooterView(110))
	for _, want := range []string{"build", "model:openai/gpt-5/high", "profile:safe-edit", "sandbox:on", "ctx:42%", "/model"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide footer missing %q: %q", want, wide)
		}
	}
	for _, width := range []int{72, 48, 32, 16} {
		line := m.statusFooterView(width)
		if got := ansi.StringWidth(line); got > width {
			t.Fatalf("footer width %d exceeds %d: %q", got, width, ansi.Strip(line))
		}
		if strings.TrimSpace(ansi.Strip(line)) == "" {
			t.Fatalf("footer disappeared at width %d", width)
		}
	}
	if narrow := ansi.Strip(m.statusFooterView(48)); !strings.Contains(narrow, "model:openai/gpt-5/high") || !strings.Contains(narrow, "ready") {
		t.Fatalf("narrow footer lost mandatory state: %q", narrow)
	}
	m.inFlightTaskID = "task_1234567890"
	m.unreadAttention = 3
	m.goal = &goalView{Status: "active", TokenBudget: 1000, TokensUsed: 750}
	if busy := ansi.Strip(m.statusFooterView(48)); !strings.Contains(busy, "model:openai/gpt-5/high") || !strings.Contains(busy, "running") {
		t.Fatalf("busy narrow footer lost model/activity anchor: %q", busy)
	}
}

func TestSingleTaskUsesOneLineRail(t *testing.T) {
	var graph taskGraph
	graph.observeEvent(map[string]any{"type": "TaskCreated", "task_id": "task_1", "payload": map[string]any{"task_id": "task_1", "user_prompt": "ship the release", "status": "running"}})
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	lines := graph.lines(m, 80, 4)
	if len(lines) != 1 || strings.Contains(lines[0], "tasks ·") || !strings.Contains(lines[0], "ship the release") {
		t.Fatalf("single task rail = %#v", lines)
	}
}

func TestCompletedTaskLeavesRailAndResultStaysInTranscript(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.tasks.observeEvent(map[string]any{"type": "TaskCreated", "task_id": "task_1", "payload": map[string]any{"user_prompt": "inspect workspace", "status": "running"}})
	m.inFlightTaskID = "task_1"
	m.Update(EventMsg{Raw: map[string]any{
		"type": "task.completed", "task_id": "task_1", "status": "completed", "summary": "workspace inspection complete",
	}})
	if lines := m.tasks.lines(m, 80, 4); len(lines) != 0 {
		t.Fatalf("completed task remained in top rail: %#v", lines)
	}
	if got := transcriptText(m); !strings.Contains(got, "workspace inspection complete") {
		t.Fatalf("completion result missing from transcript tail:\n%s", got)
	}
}

func TestSuccessfulCompletionReadsAsAssistantReply(t *testing.T) {
	p := presentEvent(map[string]any{
		"type": "task.completed", "task_id": "task_internal", "status": "completed", "summary": "**done** and `tested`",
	}, theme.New(theme.Mono), "en")
	if p.Title != "" || p.Summary != "completed" {
		t.Fatalf("completion header = %q %q, want a single agent completed label", p.Title, p.Summary)
	}
	if p.BodyMarkdown == "" || p.BodyProse {
		t.Fatalf("successful completion did not preserve markdown semantics: %#v", p)
	}
	rendered := p.render(theme.New(theme.Mono), 80)
	if strings.Contains(rendered, "task_internal") {
		t.Fatal("successful assistant reply leaked internal task id")
	}
	if strings.Contains(rendered, "**") {
		t.Fatalf("markdown emphasis markers leaked into rendered reply: %q", rendered)
	}
}

func TestModelAndTaskCompletionShareOneResultSurface(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.Update(EventMsg{Raw: map[string]any{
		"type": "ModelResponded", "task_id": "task_1",
		"payload": map[string]any{"text": `{"tool":"done","summary":"one final answer"}`},
	}})
	m.Update(EventMsg{Raw: map[string]any{
		"type": "task.completed", "task_id": "task_1", "status": "completed", "summary": "one final answer",
	}})
	got := transcriptText(m)
	if count := strings.Count(got, "one final answer"); count != 1 {
		t.Fatalf("completion result rendered %d times, want once:\n%s", count, got)
	}
}

func TestPrimaryTranscriptSuppressesAuditWALButKeepsOutcomes(t *testing.T) {
	for _, eventType := range []string{"MemoryRecallRequested", "MemoryWriteRequested", "GoalChangeRequested", "ScheduleChanged"} {
		if showInPrimaryTranscript(map[string]any{"type": eventType}) {
			t.Fatalf("%s leaked into primary transcript", eventType)
		}
	}
	if !showInPrimaryTranscript(map[string]any{"type": "MemoryWritten", "payload": map[string]any{"status": "committed"}}) {
		t.Fatal("committed memory outcome was hidden")
	}
	if showInPrimaryTranscript(map[string]any{"type": "MemoryProjectionChanged", "payload": map[string]any{"status": "completed"}}) {
		t.Fatal("successful projection telemetry leaked into primary transcript")
	}
	if !showInPrimaryTranscript(map[string]any{"type": "MemoryProjectionChanged", "payload": map[string]any{"status": "reconcile"}}) {
		t.Fatal("actionable projection recovery was hidden")
	}
}
