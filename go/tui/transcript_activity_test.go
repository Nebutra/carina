package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

func readLifecycleEvent(eventType, taskID, callID, tool, detail string) map[string]any {
	payload := map[string]any{
		"call_id": callID,
		"tool":    tool,
		"kind":    "read",
		"status":  strings.ToLower(strings.TrimPrefix(eventType, "ToolCall")),
	}
	if detail != "" {
		key := "path"
		if tool == "search" {
			key = "pattern"
		} else if strings.HasPrefix(tool, "code.") || tool == "mcp_find" {
			key = "query"
		}
		payload["arguments"] = map[string]any{key: detail}
	}
	return map[string]any{
		"type": eventType, "task_id": taskID, "timestamp": "2026-07-22T10:00:00Z", "payload": payload,
	}
}

func TestTranscriptEventClassificationPolicy(t *testing.T) {
	media := map[string]any{
		"type": "ToolCallCompleted", "task_id": "task_1",
		"payload": map[string]any{
			"call_id": "call_media", "tool": "read", "kind": "read", "status": "completed",
			"media_refs": []any{map[string]any{
				"artifact_id": strings.Repeat("a", 64), "media_type": "image/png", "bytes": float64(12),
			}},
		},
	}
	cases := []struct {
		name string
		ev   map[string]any
		want transcriptEventClass
	}{
		{name: "final response", ev: map[string]any{"type": "ModelResponded", "payload": map[string]any{"text": `{"tool":"done","summary":"finished"}`}}, want: transcriptPermanentConversation},
		{name: "model action telemetry", ev: map[string]any{"type": "ModelResponded", "payload": map[string]any{"text": `{"tool":"list"}`}}, want: transcriptAuditOnly},
		{name: "routing", ev: map[string]any{"type": "RoutingDecision"}, want: transcriptAuditOnly},
		{name: "successful read", ev: readLifecycleEvent("ToolCallCompleted", "task_1", "call_1", "read", "a.go"), want: transcriptGroupedActivity},
		{name: "failed read", ev: readLifecycleEvent("ToolCallFailed", "task_1", "call_1", "read", "a.go"), want: transcriptPermanentOperational},
		{name: "media read", ev: media, want: transcriptPermanentOperational},
		{name: "write", ev: map[string]any{"type": "ToolCallCompleted", "task_id": "task_1", "payload": map[string]any{"call_id": "call_write", "tool": "patch", "kind": "write"}}, want: transcriptPermanentOperational},
		{name: "duplicate file read", ev: map[string]any{"type": "FileRead", "task_id": "task_1", "payload": map[string]any{"path": "a.go"}}, want: transcriptEphemeralActivity},
		{name: "approval request", ev: map[string]any{"type": "ToolRequested", "task_id": "task_1", "payload": map[string]any{"status": "permission_requested", "decision_id": "dec_1"}}, want: transcriptPermanentOperational},
		{name: "question request", ev: map[string]any{"type": "ToolRequested", "task_id": "task_1", "payload": map[string]any{"status": "user_question_requested", "question_id": "q_1"}}, want: transcriptPermanentOperational},
		{name: "legacy denial", ev: map[string]any{"type": "ToolDenied", "payload": map[string]any{"tool": "run"}}, want: transcriptPermanentOperational},
		{name: "terminal durable task", ev: map[string]any{"type": "TaskCreated", "task_id": "task_1", "payload": map[string]any{"status": "degraded"}}, want: transcriptPermanentConversation},
		{name: "queued durable task", ev: map[string]any{"type": "TaskCreated", "task_id": "task_1", "payload": map[string]any{"status": "queued"}}, want: transcriptAuditOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyTranscriptEvent(tc.ev).Class; got != tc.want {
				t.Fatalf("class = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGovernanceRequestsAndResolutionsStayPermanent(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()

	durableRequest := map[string]any{
		"type": "ToolRequested", "task_id": "task_1",
		"payload": map[string]any{
			"status": "user_question_requested", "question_id": "q_1",
			"request": map[string]any{"question_id": "q_1", "prompt": "Deploy to production?"},
		},
	}
	m.handleEvent(durableRequest)
	m.handleEvent(map[string]any{
		"type": "user.question", "task_id": "task_1", "question_id": "q_1", "prompt": "Deploy to production?",
	})
	m.handleEvent(map[string]any{
		"type": "TaskCreated", "task_id": "task_1",
		"payload": map[string]any{"status": "user_question_resolved", "question_id": "q_1", "cancelled": true},
	})

	requestKey := "governance:q_1:request"
	resolvedKey := "governance:q_1:resolved"
	if m.tr.indexOf(requestKey) < 0 || m.tr.indexOf(resolvedKey) < 0 {
		t.Fatalf("governance entries missing: %#v", m.tr.entries)
	}
	requestCount := 0
	for _, entry := range m.tr.entries {
		if entry.key == requestKey {
			requestCount++
		}
	}
	if requestCount != 1 {
		t.Fatalf("durable/transient request count = %d, want 1", requestCount)
	}
	resolved := m.tr.entries[m.tr.indexOf(resolvedKey)].presentation
	if resolved == nil || resolved.Status != statusFailure {
		t.Fatalf("cancelled resolution = %#v", resolved)
	}
	if got := strings.Join(m.tr.lines, "\n"); !strings.Contains(got, "Deploy to production?") || !strings.Contains(got, "cancelled") {
		t.Fatalf("governance transcript incomplete:\n%s", got)
	}
}

func TestApprovedReadLifecycleStaysOnPermanentRow(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()

	m.handleEvent(readLifecycleEvent("ToolCallRequested", "task_1", "call_approval", "read", "secret.txt"))
	approval := readLifecycleEvent("ToolCallApprovalRequired", "task_1", "call_approval", "read", "")
	approval["payload"].(map[string]any)["decision_id"] = "dec_1"
	m.handleEvent(approval)
	m.handleEvent(readLifecycleEvent("ToolCallStarted", "task_1", "call_approval", "read", ""))
	m.handleEvent(readLifecycleEvent("ToolCallCompleted", "task_1", "call_approval", "read", ""))

	if group := m.activityGroups["activity:task_1:read"]; group != nil {
		t.Fatalf("approved call returned to activity group: %#v", group)
	}
	i := m.tr.indexOf("tool:call_approval")
	if i < 0 || m.tr.entries[i].presentation == nil || m.tr.entries[i].presentation.Status != statusSuccess {
		t.Fatalf("permanent tool lifecycle = %#v", m.tr.entries)
	}
	if got := strings.Join(m.tr.lines, "\n"); strings.Contains(got, "awaiting approval") {
		t.Fatalf("stale approval state remained visible:\n%s", got)
	}
}

func TestSuccessfulReadActivityGroupsByTaskAndCallID(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.followTail = false

	events := []map[string]any{
		readLifecycleEvent("ToolCallRequested", "task_1", "call_read", "read", "go/tui/model.go"),
		readLifecycleEvent("ToolCallStarted", "task_1", "call_read", "read", ""),
		map[string]any{"type": "FileRead", "task_id": "task_1", "payload": map[string]any{"path": "go/tui/model.go", "bytes": float64(100)}},
		readLifecycleEvent("ToolCallCompleted", "task_1", "call_read", "read", ""),
		readLifecycleEvent("ToolCallRequested", "task_1", "call_search", "search", "showInPrimaryTranscript"),
		readLifecycleEvent("ToolCallCompleted", "task_1", "call_search", "search", ""),
	}
	for _, ev := range events {
		m.handleEvent(ev)
	}

	key := "activity:task_1:read"
	if len(m.tr.entries) != 1 || m.tr.entries[0].key != key {
		t.Fatalf("entries = %#v", m.tr.entries)
	}
	group := m.activityGroups[key]
	if group == nil || len(group.order) != 2 {
		t.Fatalf("group = %#v", group)
	}
	p := m.tr.entries[0].presentation
	if p == nil || !p.Collapsible || !p.Collapsed || len(p.Body) != 2 || p.Status != statusSuccess {
		t.Fatalf("presentation = %#v", p)
	}
	if m.unseenLines != 1 {
		t.Fatalf("group lifecycle inflated unseen lines: %d", m.unseenLines)
	}
	if !m.tr.toggleLastCollapsible(m.th, 80) {
		t.Fatal("activity group was not keyboard-expandable")
	}
	expanded := strings.Join(m.tr.lines, "\n")
	for _, want := range []string{"go/tui/model.go", "showInPrimaryTranscript", "completed"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded group missing %q:\n%s", want, expanded)
		}
	}
}

func TestFailedReadLeavesGroupAndStaysPermanent(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.handleEvent(readLifecycleEvent("ToolCallCompleted", "task_1", "call_ok", "list", ""))
	m.handleEvent(readLifecycleEvent("ToolCallRequested", "task_1", "call_bad", "read", "missing.go"))
	failed := readLifecycleEvent("ToolCallFailed", "task_1", "call_bad", "read", "")
	failed["payload"].(map[string]any)["error"] = map[string]any{
		"code": "tool_failed", "category": "internal", "message": "tool did not complete successfully",
	}
	m.handleEvent(failed)

	group := m.activityGroups["activity:task_1:read"]
	if group == nil || len(group.order) != 1 || group.order[0] != "call_ok" {
		t.Fatalf("failed call remained grouped: %#v", group)
	}
	i := m.tr.indexOf("tool:call_bad")
	if i < 0 || m.tr.entries[i].presentation == nil || m.tr.entries[i].presentation.Status != statusFailure {
		t.Fatalf("failure presentation missing: %#v", m.tr.entries)
	}
	m.tr.entries[i].presentation.Collapsed = false
	m.tr.entries[i].setRendered(m.tr.entries[i].presentation.render(m.th, 100))
	m.tr.rebuildLines()
	if got := strings.Join(m.tr.lines, "\n"); !strings.Contains(got, "tool_failed") || !strings.Contains(got, "tool did not complete") {
		t.Fatalf("failure detail missing:\n%s", got)
	}
}

func TestActivityProjectionMatchesLiveAndReplay(t *testing.T) {
	events := []map[string]any{
		readLifecycleEvent("ToolCallRequested", "task_1", "call_a", "read", "a.go"),
		readLifecycleEvent("ToolCallCompleted", "task_1", "call_a", "read", ""),
		readLifecycleEvent("ToolCallRequested", "task_1", "call_b", "search", "needle"),
		readLifecycleEvent("ToolCallCompleted", "task_1", "call_b", "search", ""),
		map[string]any{"type": "PatchApplied", "task_id": "task_1", "timestamp": "2026-07-22T10:00:03Z", "payload": map[string]any{"patch_id": "patch_1", "affected_files": []any{"a.go"}, "diff": "@@\n+change"}},
		map[string]any{"type": "CommandOutput", "task_id": "task_1", "timestamp": "2026-07-22T10:00:04Z", "payload": map[string]any{"command_id": "cmd_1", "stream": "stdout", "chunk": "tests passed"}},
		map[string]any{"type": "TaskCreated", "task_id": "task_1", "timestamp": "2026-07-22T10:00:05Z", "payload": map[string]any{"status": "approval_resolved", "decision_id": "dec_1", "granted": true}},
		map[string]any{"type": "ModelResponded", "task_id": "task_1", "timestamp": "2026-07-22T10:00:06Z", "payload": map[string]any{"text": `{"tool":"done","summary":"Final answer"}`}},
		map[string]any{"type": "task.completed", "task_id": "task_1", "timestamp": "2026-07-22T10:00:07Z", "status": "degraded", "summary": "Final answer"},
	}
	project := func() *Model {
		m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
		for _, ev := range events {
			m.handleEvent(ev)
		}
		return m
	}
	live, replay := project(), project()
	defer live.Close()
	defer replay.Close()

	keys := func(m *Model) []string {
		out := make([]string, len(m.tr.entries))
		for i, entry := range m.tr.entries {
			out[i] = entry.key
		}
		return out
	}
	if !reflect.DeepEqual(keys(live), keys(replay)) || strings.Join(live.tr.lines, "\n") != strings.Join(replay.tr.lines, "\n") {
		t.Fatalf("live/replay mismatch:\n%#v\n%#v\n%s\n---\n%s", keys(live), keys(replay), strings.Join(live.tr.lines, "\n"), strings.Join(replay.tr.lines, "\n"))
	}
	if count := func() int {
		n := 0
		for _, key := range keys(live) {
			if key == "result:task_1" {
				n++
			}
		}
		return n
	}(); count != 1 {
		t.Fatalf("final result entries = %d, keys=%#v", count, keys(live))
	}
	result := live.tr.entries[live.tr.indexOf("result:task_1")].presentation
	if result == nil || result.Status != statusFailure || result.Summary != "degraded" {
		t.Fatalf("degraded result projection = %#v", result)
	}
	for _, ev := range events[:4] {
		live.handleEvent(ev)
	}
	if group := live.activityGroups["activity:task_1:read"]; group == nil || len(group.order) != 2 {
		t.Fatalf("duplicate replay changed group membership: %#v", group)
	}
	groupEntries := 0
	for _, key := range keys(live) {
		if key == "activity:task_1:read" {
			groupEntries++
		}
	}
	if groupEntries != 1 {
		t.Fatalf("duplicate replay created %d group entries: %#v", groupEntries, keys(live))
	}
	for _, want := range []string{"patch_1", "tests passed", "approval resolved", "Final answer"} {
		if !strings.Contains(strings.Join(live.tr.lines, "\n"), want) {
			t.Fatalf("permanent content missing %q:\n%s", want, strings.Join(live.tr.lines, "\n"))
		}
	}
}

func TestActivityGroupMonoNarrowAndExport(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.width = 28
	m.handleEvent(readLifecycleEvent("ToolCallRequested", "task_narrow", "call_narrow", "read", "a/very/long/path/that/must/fit.go"))
	m.handleEvent(readLifecycleEvent("ToolCallCompleted", "task_narrow", "call_narrow", "read", ""))
	if !m.tr.toggleLastCollapsible(m.th, m.transcriptWidth()) {
		t.Fatal("activity group did not expand")
	}
	for i, line := range m.tr.lines {
		if width := ansi.StringWidth(line); width > m.transcriptWidth() {
			t.Fatalf("line %d width=%d exceeds %d: %q", i, width, m.transcriptWidth(), line)
		}
		for _, glyph := range []string{"✓", "✗", "·", "›"} {
			if strings.Contains(line, glyph) {
				t.Fatalf("Mono activity contains glyph %q: %q", glyph, line)
			}
		}
	}
	exported := m.tr.plainExport()
	if !strings.Contains(exported, "activity") || !strings.Contains(exported, "a/very/long") || strings.Contains(exported, "\x1b") {
		t.Fatalf("group export = %q", exported)
	}
}
