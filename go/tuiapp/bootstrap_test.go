package tuiapp

import (
	"errors"
	"strings"
	"testing"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

func TestBootstrapModeUsesRenderedHitGeometryWithKeyboardParity(t *testing.T) {
	m := newRuntimeModeChoiceModel("en")
	m.width, m.height = 48, 12
	frame := m.runtime.BeginFrame(m.component, ui.Rect{Width: m.width, Height: m.height})
	legacy := bootstrapHit(t, frame.Root, "legacy")

	lines := strings.Split(frame.Root.Content, "\n")
	if legacy.Bounds.Y >= len(lines) || !strings.Contains(lines[legacy.Bounds.Y], "Legacy global runtime") {
		t.Fatalf("legacy hit does not match rendered row: bounds=%+v\n%s", legacy.Bounds, frame.Root.Content)
	}

	hover := m.runtime.Dispatch(ui.Event{Kind: ui.EventPointer, Pointer: ui.PointerEvent{
		Kind: ui.PointerMove, X: legacy.Bounds.X, Y: legacy.Bounds.Y,
	}})
	if !hover.Handled || m.hovered != "legacy" || m.selected != 0 {
		t.Fatalf("hover changed keyboard selection: handled=%v hovered=%q selected=%d", hover.Handled, m.hovered, m.selected)
	}
	pointer := m.runtime.Dispatch(ui.Event{Kind: ui.EventPointer, Pointer: ui.PointerEvent{
		Kind: ui.PointerClick, X: legacy.Bounds.X, Y: legacy.Bounds.Y,
	}})
	if got := bootstrapActionID(pointer); got != "legacy" {
		t.Fatalf("pointer action=%q, want legacy", got)
	}

	m.selected = 1
	keyboard := m.runtime.Dispatch(ui.Event{Kind: ui.EventKey, Key: "enter"})
	if got := bootstrapActionID(keyboard); got != "legacy" {
		t.Fatalf("keyboard action=%q, want legacy", got)
	}
}

func TestBootstrapFailureOwnsRetryDetailsAndExit(t *testing.T) {
	m := newBootstrapModel(Options{}, "en", false)
	m.stage = bootstrapRuntime
	m.prepared.projectRoot = "/work/example"
	m.prepared.logPath = "/work/example/.carina/runtime.log"
	m.failure = &bootstrapFailure{
		stage: bootstrapRuntime, code: microcopy.BootstrapStartupFailed,
		err: errors.New("socket identity mismatch"), outcome: tui.OutcomeRuntimeError,
	}
	m.width, m.height = 64, 16
	frame := m.runtime.BeginFrame(m.component, ui.Rect{Width: m.width, Height: m.height})
	for _, action := range []string{"retry", "details", "exit"} {
		_ = bootstrapHit(t, frame.Root, action)
	}

	details := m.component.Handle(ui.Event{Kind: ui.EventKey, Key: "d"})
	if got := bootstrapActionID(details); got != "details" {
		t.Fatalf("details key action=%q", got)
	}
	_, _ = m.applyActions(details)
	if !m.detailsOpen {
		t.Fatal("details action did not open evidence")
	}
	view := m.View().Content
	if !strings.Contains(view, "socket identity mismatch") || !strings.Contains(view, m.prepared.logPath) {
		t.Fatalf("details omitted evidence:\n%s", view)
	}

	retry := m.component.Handle(ui.Event{Kind: ui.EventKey, Key: "r"})
	if got := bootstrapActionID(retry); got != "retry" {
		t.Fatalf("retry key action=%q", got)
	}
	exit := m.component.Handle(ui.Event{Kind: ui.EventKey, Key: "q"})
	if got := bootstrapActionID(exit); got != "exit" {
		t.Fatalf("exit key action=%q", got)
	}
}

func TestBootstrapIdentityRetryReloadsRuntimeConfig(t *testing.T) {
	m := newBootstrapModel(Options{}, "en", false)
	m.stage = bootstrapIdentity
	m.failure = &bootstrapFailure{
		stage: bootstrapIdentity, code: microcopy.BootstrapConfigFailed,
		err: errors.New("invalid keybinding"), outcome: tui.OutcomeUsage,
	}
	previousOperation := m.operation
	_, cmd := m.applyActions(bootstrapActionResult("retry"))
	if cmd == nil {
		t.Fatal("identity retry did not schedule runtime config resolution")
	}
	if m.stage != bootstrapRuntime {
		t.Fatalf("retry stage=%v, want runtime config reload", m.stage)
	}
	if m.operation != previousOperation+1 {
		t.Fatalf("retry operation=%d, want %d", m.operation, previousOperation+1)
	}
}

func TestBootstrapStageReducerReachesModeDecisionWithoutLosingWorkspace(t *testing.T) {
	m := newBootstrapModel(Options{}, "en", false)
	prepared := bootstrapPrepared{home: "/home/test", projectRoot: "/work/repo"}
	model, cmd := m.applyStep(bootstrapStepMsg{
		stage: bootstrapMode, prepared: prepared, modeDecision: true,
	})
	if model != m || cmd != nil {
		t.Fatal("mode decision should remain on the bootstrap screen")
	}
	if m.stage != bootstrapMode || !m.modeDecision || m.prepared.projectRoot != prepared.projectRoot {
		t.Fatalf("mode state lost typed bootstrap data: stage=%v decision=%v prepared=%+v", m.stage, m.modeDecision, m.prepared)
	}
}

func bootstrapHit(t *testing.T, node ui.Node, action string) ui.HitRegion {
	t.Helper()
	for _, hit := range node.Hit {
		if hit.Action == "bootstrap-action" && hit.Data == action {
			return hit
		}
	}
	t.Fatalf("missing bootstrap hit %q in %+v", action, node.Hit)
	return ui.HitRegion{}
}

func bootstrapActionID(result ui.Result) string {
	if len(result.Actions) == 0 {
		return ""
	}
	id, _ := result.Actions[0].Data.(string)
	return id
}
