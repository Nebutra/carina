package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

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
