package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

func TestTaskCreatedProjectsStableUserPresentationAndReplayDraft(t *testing.T) {
	p := presentEvent(map[string]any{
		"type": "TaskCreated", "task_id": "task_1",
		"payload": map[string]any{
			"user_prompt":     "检查 workspace 并修复 parser",
			"requested_model": "openai/gpt-5", "requested_reasoning_effort": "high",
			"agent": "build", "mode": "background",
			"input_media_refs": []any{map[string]any{
				"artifact_id": "artifact_1", "media_type": "image/png", "bytes": float64(42), "origin": "screen.png",
			}},
		},
	}, theme.New(theme.Mono), "en")

	if p.Kind != presentationUser || p.Key != "user:task_1" || p.TaskID != "task_1" || !p.Branchable || p.Steer {
		t.Fatalf("user presentation identity = %#v", p)
	}
	if p.UserDraft.Text != "检查 workspace 并修复 parser" || p.UserDraft.Model != "openai/gpt-5" ||
		p.UserDraft.ReasoningEffort != "high" || p.UserDraft.Agent != "build" || p.UserDraft.Mode != "background" {
		t.Fatalf("replayed draft metadata = %#v", p.UserDraft)
	}
	if len(p.UserDraft.Attachments) != 1 || p.UserDraft.Attachments[0].Ref == nil ||
		p.UserDraft.Attachments[0].Ref.ArtifactID != "artifact_1" || p.UserDraft.Attachments[0].ID == "" {
		t.Fatalf("replayed media references = %#v", p.UserDraft.Attachments)
	}
	local := newUserPresentation("task_1", p.UserDraft, false)
	if local.Key != p.Key || local.Kind != p.Kind || local.TaskID != p.TaskID || local.Branchable != p.Branchable ||
		!reflect.DeepEqual(local.UserDraft, p.UserDraft) {
		t.Fatalf("local/replay semantic mismatch: local=%#v replay=%#v", local, p)
	}
	for _, line := range strings.Split(ansi.Strip(p.render(theme.New(theme.Mono), 24)), "\n") {
		if ansi.StringWidth(line) > 24 {
			t.Fatalf("CJK user projection exceeded width: %q", line)
		}
	}
}

func TestUserBacktrackTargetsUseSemanticTaskBoundaries(t *testing.T) {
	th := theme.New(theme.Mono)
	tr := transcript{}
	first := newUserPresentation("task_1", promptDraft{Text: "first"}, false)
	steer := newUserPresentation("task_1", promptDraft{Text: "steer"}, true)
	steer.Key = "user:task_1:steer:1"
	second := newUserPresentation("task_2", promptDraft{Text: "second"}, false)
	tr.pushPresentation(first, th, 80)
	tr.pushPresentation(steer, th, 80)
	tr.push("compatibility receipt")
	tr.pushPresentation(second, th, 80)

	targets := tr.userBacktrackTargets()
	if len(targets) != 3 {
		t.Fatalf("targets = %#v", targets)
	}
	want := []struct {
		key, task, previous string
		branchable, steer   bool
	}{
		{"user:task_1", "task_1", "", true, false},
		{"user:task_1:steer:1", "task_1", "task_1", false, true},
		{"user:task_2", "task_2", "task_1", true, false},
	}
	for index, expected := range want {
		got := targets[index]
		if got.EntryKey != expected.key || got.TaskID != expected.task || got.PreviousTaskID != expected.previous ||
			got.Branchable != expected.branchable || got.Steer != expected.steer {
			t.Fatalf("target %d = %#v, want %#v", index, got, expected)
		}
	}
}

func TestReplayUpsertPreservesRicherLocalUserDraft(t *testing.T) {
	var tr transcript
	local := newUserPresentation("task_1", promptDraft{
		Text: "ship", Paste: []string{"local paste"}, Attachments: []draftAttachment{{
			ID: "local", Digest: "abc", Data: []byte("image"), MediaType: "image/png",
		}},
	}, false)
	tr.pushPresentation(local, theme.New(theme.Mono), 80)
	replay := newUserPresentation("task_1", promptDraft{Text: "ship"}, false)
	tr.pushPresentation(replay, theme.New(theme.Mono), 80)

	got := tr.entries[0].presentation.UserDraft
	if len(got.Paste) != 1 || len(got.Attachments) != 1 || got.Attachments[0].ID != "local" {
		t.Fatalf("replay discarded richer local editor state: %#v", got)
	}
}

func TestSubmittedTaskCreatedIsVisibleAndConvergesWithLocalAck(t *testing.T) {
	m, _ := newTestModel(nil)
	m.pushUserPresentation("task_1", promptDraft{Text: "部署到上海", Paste: []string{"local"}}, false, 1)
	event := map[string]any{
		"type": "TaskCreated", "task_id": "task_1",
		"payload": map[string]any{"status": "submitted", "user_prompt": "部署到上海"},
	}
	if got := classifyTranscriptEvent(event).Class; got != transcriptPermanentConversation {
		t.Fatalf("submitted prompt classification = %v", got)
	}
	m.pushEvent(event)
	if len(m.tr.entries) != 1 || m.tr.entries[0].key != "user:task_1" || m.tr.entries[0].presentation.Kind != presentationUser {
		t.Fatalf("local/replay user cells diverged: %#v", m.tr.entries)
	}
	if got := m.tr.entries[0].presentation.UserDraft; len(got.Paste) != 1 || got.Paste[0] != "local" {
		t.Fatalf("replay discarded local draft snapshot: %#v", got)
	}
}

func TestCompatibilityReceiptsAreTypedKeyedAndResizeStable(t *testing.T) {
	tr := transcript{}
	tr.push("\x1b[31mfirst\x1b[0m")
	tr.push("second")

	if len(tr.entries) != 2 || tr.entries[0].key != "receipt:1" || tr.entries[1].key != "receipt:2" {
		t.Fatalf("receipt identities = %#v", tr.entries)
	}
	for _, entry := range tr.entries {
		if entry.presentation == nil || entry.presentation.Kind != presentationReceipt || entry.key == "" {
			t.Fatalf("anonymous compatibility entry = %#v", entry)
		}
	}
	before := append([]string(nil), tr.lines...)
	tr.resizePresentations(theme.New(theme.ANSI256), 20)
	if !reflect.DeepEqual(tr.lines, before) {
		t.Fatalf("compatibility receipt changed on resize: before=%q after=%q", before, tr.lines)
	}
}

func TestTranscriptActionsAreContextualByPresentationKind(t *testing.T) {
	m, _ := newTestModel(nil)
	m.inFlightTaskID = "task_1"
	tests := []struct {
		name string
		p    eventPresentation
		want []string
		not  []string
	}{
		{name: "user", p: newUserPresentation("task_1", promptDraft{Text: "ship"}, false), want: []string{"edit", "copy"}, not: []string{"inspect", "open", "cancel"}},
		{name: "assistant", p: eventPresentation{Key: "result:task_1", Kind: presentationAgent, ArtifactIDs: []string{"artifact_1"}}, want: []string{"copy", "open"}, not: []string{"inspect", "cancel"}},
		{name: "tool", p: eventPresentation{Key: "tool:call_1", Kind: presentationTool, Status: statusRunning, TaskID: "task_1", ArtifactIDs: []string{"artifact_1"}}, want: []string{"inspect", "open", "cancel"}, not: []string{"copy"}},
		{name: "governance", p: eventPresentation{Key: "governance:decision_1", Kind: presentationGovernance}, want: []string{"inspect"}, not: []string{"copy", "open", "cancel"}},
		{name: "receipt", p: compatibilityReceiptPresentation("receipt:1", "notice"), want: []string{"copy"}, not: []string{"inspect", "open", "cancel"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := entry{key: test.p.Key, presentation: &test.p}
			actions := m.transcriptComponentActions(&entry)
			for _, name := range test.want {
				if !hasTranscriptAction(actions, name) {
					t.Fatalf("actions %#v missing %q", actions, name)
				}
			}
			for _, name := range test.not {
				if hasTranscriptAction(actions, name) {
					t.Fatalf("actions %#v unexpectedly contain %q", actions, name)
				}
			}
		})
	}
}

func TestSelectedTranscriptStateIsIndependentFromHoverAndFocus(t *testing.T) {
	cell := newConversationTranscriptCell("transcript-cell:user:task_1")
	cell.sync(conversationTranscriptCellView{
		ID: "transcript-cell:user:task_1", Content: "you\n  selected prompt", Role: ui.RoleTitle, Selected: true,
		LineCount: 2, Actions: []conversationTranscriptActionView{{
			Name: "copy", Label: "copy", Shortcut: "c", Data: transcriptComponentAction{Key: "user:task_1", Name: "copy"},
		}},
	})
	cell.Layout(ui.Rect{Width: 40, Height: 2})

	hovered := cell.Render(ui.RenderContext{Hovered: cell.hoverHitID()})
	if hovered.Role != ui.RoleSelected || !hovered.Hovered || hovered.Focused || !strings.HasPrefix(ansi.Strip(hovered.Content), "> ") {
		t.Fatalf("selected hover state = %#v", hovered)
	}
	cell.HasFocus = true
	focused := cell.Render(ui.RenderContext{})
	if focused.Role != ui.RoleSelected || focused.Hovered || !focused.Focused || !strings.HasPrefix(ansi.Strip(focused.Content), "> ") {
		t.Fatalf("selected focus state = %#v", focused)
	}
}
