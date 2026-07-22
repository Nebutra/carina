package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/tui/theme"
)

func availableOpenAIInventory() modelListResponse {
	return modelListResponse{
		DefaultModel: "openai/gpt-5",
		Reasoner:     &modelListReasoner{Backend: "model-router", Available: true},
		Providers: []modelListProvider{{
			ID: "openai", Registered: true, Available: true, DefaultModel: "gpt-5",
			Models: []modelListModel{{ID: "openai/gpt-5", Available: true}},
		}},
	}
}

func TestRuntimeInventoryDrivesReadinessAndConcreteFooterModel(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionID = "sess_1"
	m.handleRuntimeStatus(runtimeStatusMsg{sessionID: "sess_1", inventoryLoaded: true, inventory: availableOpenAIInventory()})
	if m.conversation.Readiness != readinessReady {
		t.Fatalf("readiness = %v, reason %q", m.conversation.Readiness, m.runtime.ReadinessReason)
	}
	footer := m.statusFooterView(80)
	if !strings.Contains(footer, "model:openai/gpt-5") || strings.Contains(footer, "model:default") {
		t.Fatalf("footer = %q", footer)
	}
}

func TestRuntimeInventoryBlocksMissingOrUnavailableSelection(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionID = "sess_1"
	m.handleRuntimeStatus(runtimeStatusMsg{
		sessionID: "sess_1", inventoryLoaded: true,
		inventory: modelListResponse{Reasoner: &modelListReasoner{Backend: "", Available: false}},
	})
	if m.conversation.Readiness != readinessBlocked || strings.Contains(m.statusFooterView(80), "default") {
		t.Fatalf("missing provider state = %+v footer=%q", m.conversation, m.statusFooterView(80))
	}

	m.model = "openai/missing"
	m.handleRuntimeStatus(runtimeStatusMsg{sessionID: "sess_1", inventoryLoaded: true, inventory: availableOpenAIInventory()})
	if m.conversation.Readiness != readinessBlocked || !strings.Contains(m.runtime.ReadinessReason, "selected model") {
		t.Fatalf("unavailable selection = %+v reason=%q", m.conversation, m.runtime.ReadinessReason)
	}
}

func TestExplicitCLIReasonerIsReadyWithoutProviderDefault(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.model = "openai/stale-session-model"
	m.applyModelInventory(modelListResponse{
		Reasoner: &modelListReasoner{Backend: "codex-cli", Available: true, Explicit: true},
	})
	if m.conversation.Readiness != readinessReady {
		t.Fatalf("CLI readiness = %+v", m.conversation)
	}
	footer := m.statusFooterView(80)
	if !strings.Contains(footer, "backend:codex-cli") || strings.Contains(footer, "model:default") {
		t.Fatalf("CLI footer = %q", footer)
	}
}

func TestNonRouterReasonerMustBeExplicit(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.applyModelInventory(modelListResponse{
		Reasoner: &modelListReasoner{Backend: "codex-cli", Available: true},
	})
	if m.conversation.Readiness != readinessBlocked || !strings.Contains(m.runtime.ReadinessReason, "explicitly configured") {
		t.Fatalf("implicit CLI readiness = %+v reason=%q", m.conversation, m.runtime.ReadinessReason)
	}
}

func TestInventoryErrorIsUnavailableAndStaleGenerationIsIgnored(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionID, m.sessionGeneration = "sess_1", 2
	m.conversation.Readiness = readinessReady
	m.handleRuntimeStatus(runtimeStatusMsg{sessionID: "sess_1", generation: 1, inventoryErr: errors.New("stale")})
	if m.conversation.Readiness != readinessReady {
		t.Fatal("stale inventory changed readiness")
	}
	m.handleRuntimeStatus(runtimeStatusMsg{sessionID: "sess_1", generation: 2, inventoryErr: errors.New("inventory unavailable")})
	if m.conversation.Readiness != readinessUnavailable || !strings.Contains(m.statusActivityText(), "not attached") {
		t.Fatalf("inventory error state = %+v activity=%q", m.conversation, m.statusActivityText())
	}
}

func TestBlockedNewTaskPreservesDraftAndOpensActions(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.submit": map[string]any{"task_id": "must_not_run"}}}
	m, _ := newTestModel(fc)
	m.applyModelInventory(modelListResponse{Reasoner: &modelListReasoner{Available: false}})
	m.input.SetValue("keep this draft")
	if cmd := m.submit(); cmd != nil {
		t.Fatal("blocked submission returned an RPC command")
	}
	if m.input.Value() != "keep this draft" || m.submitting != nil {
		t.Fatalf("blocked submission mutated draft/state: %q %+v", m.input.Value(), m.submitting)
	}
	for _, call := range fc.calls {
		if call.method == "task.submit" {
			t.Fatalf("blocked submission emitted task.submit: %#v", fc.calls)
		}
	}
	if m.settings == nil || m.settings.tab != settingsTabModel {
		t.Fatalf("blocked submission did not open model actions: %#v", m.settings)
	}
	if text := transcriptText(m); !strings.Contains(text, "/model") || !strings.Contains(text, "/doctor") {
		t.Fatalf("blocked submission lacks actions: %q", text)
	}
}

func TestBlockedTaskSlashPreservesExactDraft(t *testing.T) {
	for _, draft := range []string{"/review main", "/commit concise", "/btw explain this", "/side explain this", "/plan inspect first"} {
		t.Run(strings.TrimPrefix(strings.Fields(draft)[0], "/"), func(t *testing.T) {
			m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
			defer m.Close()
			m.sessionID, m.call = "sess", &fakeCaller{}
			m.conversation.Readiness = readinessBlocked
			m.runtime.ReadinessReason = "no runnable provider model"
			m.input.SetValue(draft)
			if cmd := m.submit(); cmd != nil {
				t.Fatalf("blocked slash returned a command: %T", cmd)
			}
			if got := m.input.Value(); got != draft {
				t.Fatalf("draft = %q, want %q", got, draft)
			}
		})
	}
}

func TestReadinessGateDoesNotBlockSteering(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"task.steer": nil}}
	m, _ := newTestModel(fc)
	m.conversation.Readiness = readinessBlocked
	m.inFlightTaskID = "task_active"
	m.input.SetValue("change direction")
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("readiness gate blocked steering")
	}
	drain(m, cmd)
	if fc.last().method != "task.steer" {
		t.Fatalf("last call = %#v", fc.last())
	}
}

func TestReconnectRefreshesInventoryImmediately(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"session.get":      map[string]any{},
		"config.inventory": map[string]any{},
		"context.summary":  map[string]any{},
		"model.list":       availableOpenAIInventory(),
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	defer m.Close()
	m.sessionID = "sess_reconnect"
	m.conn = ConnLost
	m.conversation.Readiness = readinessUnavailable
	m.Update(ConnRestoredMsg{SessionID: "sess_reconnect"})
	_, cmd := m.Update(SessionReadyMsg{SessionID: "sess_reconnect", Call: fc})
	if cmd == nil {
		t.Fatal("reconnect did not schedule an immediate readiness refresh")
	}
	drain(m, cmd)
	if m.conversation.Readiness != readinessReady {
		t.Fatalf("readiness after reconnect = %+v", m.conversation)
	}
	found := false
	for _, call := range fc.calls {
		if call.method == "model.list" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reconnect calls = %#v", fc.calls)
	}
}

func TestPendingSideQuestionWaitsForReadiness(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"task.submit": map[string]any{"task_id": "tsk_side", "status": "queued"},
	}}
	m, _ := newTestModel(fc)
	m.conversation.Readiness = readinessChecking
	m.pendingSideQuestion = "explain readiness"
	if cmd := m.flushPendingSideQuestion(); cmd != nil || m.pendingSideQuestion == "" {
		t.Fatalf("checking readiness consumed pending side question: pending=%q cmd=%v", m.pendingSideQuestion, cmd != nil)
	}
	cmd := m.handleRuntimeStatus(runtimeStatusMsg{sessionID: m.sessionID, inventoryLoaded: true, inventory: availableOpenAIInventory()})
	if cmd == nil || m.pendingSideQuestion != "" {
		t.Fatalf("ready inventory did not release side question: pending=%q cmd=%v", m.pendingSideQuestion, cmd != nil)
	}
	drain(m, cmd)
	if fc.last().method != "task.submit" {
		t.Fatalf("side question calls = %#v", fc.calls)
	}
}

func TestOpenRouterDefaultKeepsProviderPrefix(t *testing.T) {
	response := modelListResponse{Providers: []modelListProvider{{
		ID: "openrouter", Registered: true, Available: true, DefaultModel: "openai/gpt-5",
	}}}
	if got := effectiveInventoryDefault(response); got != "openrouter/openai/gpt-5" {
		t.Fatalf("default = %q", got)
	}
}
