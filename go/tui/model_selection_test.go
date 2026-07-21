package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestModelCommandAppliesToNewTaskSubmission(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit":       map[string]any{"task_id": "task_model", "status": "queued"},
		"session.model.set": map[string]any{"next_model": "anthropic/claude-sonnet-4-5"},
	}}
	m, _ := newTestModel(fc)
	m.sessionID = "sess_model"
	if cmd := m.slashCommand("/model anthropic/claude-sonnet-4-5"); cmd != nil {
		drain(m, cmd)
	}
	cmd := m.beginSubmission(submissionTask, "inspect model routing", promptDraft{Text: "inspect model routing"})
	msg := cmd()
	m.Update(msg)
	last := fc.last()
	if last.method != "task.submit" || last.params["model"] != "anthropic/claude-sonnet-4-5" {
		t.Fatalf("model override not submitted: %#v", last)
	}
	if !strings.Contains(m.View().Content, "anthropic/claude-sonnet-4-5") {
		t.Fatal("active model is not visible in the footer")
	}
}

func TestModelCommandOpensAvailableModelPicker(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"model.list": map[string]any{
			"default_model": "default",
			"providers": []map[string]any{{
				"id": "openai", "name": "OpenAI", "registered": true, "available": true,
				"auth_source": "env:OPENAI_API_KEY",
				"models": []map[string]any{{"id": "openai/gpt-5", "name": "GPT-5", "available": true,
					"reasoning_efforts": []string{"low", "medium", "high"}, "default_reasoning_effort": "medium"}},
			}},
		},
		"session.model.set": map[string]any{"next_model": "openai/gpt-5"},
	}}
	m, _ := newTestModel(fc)
	cmd := m.slashCommand("/model")
	if cmd == nil || m.modelPicker == nil || !m.modelPicker.loading {
		t.Fatalf("model picker did not open: %#v", m.modelPicker)
	}
	drain(m, cmd)
	if m.modelPicker == nil || len(m.modelPicker.items) != 2 {
		t.Fatalf("model picker inventory = %#v", m.modelPicker)
	}
	if _, handled := m.modelPickerKey("down"); !handled {
		t.Fatal("picker did not handle navigation")
	}
	if _, handled := m.modelPickerKey("e"); !handled {
		t.Fatal("picker did not handle effort selection")
	}
	if _, handled := m.modelPickerKey("enter"); !handled || m.modelPicker != nil {
		t.Fatalf("picker selection failed: handled=%v state=%#v", handled, m.modelPicker)
	}
	if m.model != "openai/gpt-5" {
		t.Fatalf("selected model = %q", m.model)
	}
	if m.reasoningEffort != "high" {
		t.Fatalf("selected reasoning effort = %q", m.reasoningEffort)
	}
	if strings.Contains(m.View().Content, "inventory-secret") {
		t.Fatal("picker rendered a credential")
	}
}

func TestModelPickerKeepsLongInventoryWithinViewport(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.height = 14
	state := &modelPickerState{}
	for i := 0; i < 20; i++ {
		state.items = append(state.items, modelPickerItem{ID: fmt.Sprintf("provider/model-%02d", i)})
	}
	m.modelPicker = state
	for i := 0; i < 12; i++ {
		m.modelPickerKey("down")
	}
	page := m.modelPickerPageHeight()
	if state.selected != 12 || state.scroll == 0 || state.selected >= state.scroll+page {
		t.Fatalf("picker did not keep selection visible: selected=%d scroll=%d page=%d", state.selected, state.scroll, page)
	}
	view := m.modelPickerView()
	if !strings.Contains(view, "of 20") || strings.Contains(view, "provider/model-00") {
		t.Fatalf("picker view did not page inventory: %q", view)
	}
}

func TestModelPickerUsesActiveLocale(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.locale = string(LocaleChinese)
	m.modelPicker = &modelPickerState{items: []modelPickerItem{{ID: "default", Name: m.text(MsgModelPickerDefault, nil)}}}
	view := m.modelPickerView()
	if !strings.Contains(view, "选择模型") || !strings.Contains(view, "守护进程默认模型") {
		t.Fatalf("model picker ignored active locale: %q", view)
	}
}

func TestTaskSubmissionClosureUsesFrozenModel(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "task_frozen", "status": "queued"},
	}}
	m, _ := newTestModel(fc)
	m.sessionID = "sess_frozen"
	m.model = "openai/gpt-5"
	m.reasoningEffort = "high"
	cmd := m.beginSubmission(submissionTask, "frozen routing", promptDraft{Text: "frozen routing"})
	if cmd == nil {
		t.Fatal("submission command missing")
	}
	m.model = "anthropic/claude-sonnet-4-5-20250929"
	m.reasoningEffort = "low"
	drain(m, cmd)
	if got := fc.last().params["model"]; got != "openai/gpt-5" {
		t.Fatalf("async closure read live model: got %v", got)
	}
	if got := fc.last().params["reasoning_effort"]; got != "high" {
		t.Fatalf("async closure read live effort: got %v", got)
	}
}

func TestTaskSubmissionRetryReplaysFrozenEnvelope(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": errors.New("timeout")}}
	m, _ := newTestModel(fc)
	m.sessionID = "sess_retry_model"
	m.model = "openai/gpt-5"
	m.reasoningEffort = "high"
	m.input.SetValue("retry routing")
	drain(m, m.submit())
	first := fc.last()
	firstID := first.params["client_submission_id"]
	m.model = "anthropic/claude-sonnet-4-5-20250929"
	m.reasoningEffort = "low"
	fc.handler["task.submit"] = map[string]any{"task_id": "task_existing", "status": "running"}
	drain(m, m.submit())
	second := fc.last()
	if second.params["client_submission_id"] != firstID || second.params["model"] != first.params["model"] || second.params["reasoning_effort"] != first.params["reasoning_effort"] || second.params["mode"] != first.params["mode"] || second.params["prompt"] != first.params["prompt"] {
		t.Fatalf("retry changed immutable envelope: first=%#v second=%#v", first.params, second.params)
	}
}

func TestQueuedTaskUsesModelFromEnqueueTime(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "task_queued", "status": "queued"},
	}}
	m, _ := newTestModel(fc)
	m.sessionID = "sess_queue_model"
	m.inFlightTaskID = "task_active"
	m.model = "openai/gpt-5"
	m.input.SetValue("queued routing")
	if !m.enqueueFollowUp() {
		t.Fatal("follow-up was not queued")
	}
	m.model = "anthropic/claude-sonnet-4-5-20250929"
	m.inFlightTaskID = ""
	cmd := m.maybeSubmitNextQueued()
	if cmd == nil {
		t.Fatal("queued submission command missing")
	}
	drain(m, cmd)
	if got := fc.last().params["model"]; got != "openai/gpt-5" {
		t.Fatalf("queued task used live model: %v", got)
	}
}

func TestPrimaryTranscriptHidesInternalLifecycleNoise(t *testing.T) {
	internal := []map[string]any{
		{"type": "TaskCreated", "payload": map[string]any{"task_id": "t1"}},
		{"type": "ModelRequested", "payload": map[string]any{"model": "default"}},
		{"type": "RoutingDecision", "payload": map[string]any{"requested_model": "default"}},
		{"type": "RuntimeStageChanged", "payload": map[string]any{"stage": "model"}},
		{"type": "ModelResponded", "payload": map[string]any{"text": `{"tool":"list"}`}},
	}
	for _, ev := range internal {
		if showInPrimaryTranscript(ev) {
			t.Fatalf("internal event leaked into primary transcript: %#v", ev)
		}
	}
	if !showInPrimaryTranscript(map[string]any{"type": "ModelResponded", "payload": map[string]any{"text": `{"tool":"done","summary":"finished"}`}}) {
		t.Fatal("final agent response was hidden")
	}
	if !showInPrimaryTranscript(map[string]any{"type": "task.completed", "status": "completed", "summary": "finished"}) {
		t.Fatal("authoritative task completion was hidden")
	}
	if !showInPrimaryTranscript(map[string]any{"type": "ToolCallCompleted", "payload": map[string]any{"tool": "read"}}) {
		t.Fatal("authoritative tool completion was hidden")
	}
}
