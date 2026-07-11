package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// typeText delivers each rune of s to the model as a KeyPressMsg and, for
// each keystroke, calls refreshSuggestTrigger directly — the same trigger
// re-evaluation Update's fallthrough path drives — to obtain the
// debounce-scheduling command without executing Update's full returned
// tea.Batch. That batch also carries the textarea's own cursor-blink Tick
// command, which (like the debounce Tick) blocks on a real timer when
// invoked, so naive batch-unwrapping in a test would be flaky/slow for
// reasons unrelated to the suggestion feature under test.
func typeText(m *Model, s string) tea.Cmd {
	var last tea.Cmd
	for _, r := range s {
		m.input, _ = m.input.Update(tea.KeyPressMsg{Text: string(r), Code: r})
		if cmd := m.refreshSuggestTrigger(); cmd != nil {
			last = cmd
		}
	}
	return last
}

func TestSuggestDebounceCollapsesRapidKeystrokes(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"workspace.tree": []treeEntry{{Path: "main.go"}, {Path: "main_test.go"}},
		"agent.list":     map[string]any{"agents": []map[string]any{}},
	}}
	m, _ := newTestModel(fc)

	// Type "@main" one keystroke at a time. Each keystroke schedules a new
	// debounce tick (bumping suggestGen) but none of the intermediate ticks
	// should ever reach the RPC layer — only the LAST one, once fired by
	// hand below, may.
	cmd := typeText(m, "@main")
	if cmd == nil {
		t.Fatal("expected the final keystroke to return a debounce-scheduling command")
	}
	if len(fc.calls) != 0 {
		t.Fatalf("no RPC call should happen before the debounce tick fires; got %d calls", len(fc.calls))
	}

	// Execute the scheduled tea.Tick command by hand (deterministic: no
	// reliance on real elapsed time) and feed its message back in.
	msg := cmd()
	debounce, ok := msg.(suggestDebounceMsg)
	if !ok {
		t.Fatalf("expected suggestDebounceMsg, got %T", msg)
	}
	if debounce.gen != m.suggestGen {
		t.Fatalf("the last scheduled tick's gen (%d) must match the model's current gen (%d)", debounce.gen, m.suggestGen)
	}
	_, fetchCmd := m.Update(debounce)
	if fetchCmd == nil {
		t.Fatal("expected a fetch command once the debounce settles")
	}
	resultMsg := fetchCmd()
	result, ok := resultMsg.(suggestResultMsg)
	if !ok {
		t.Fatalf("expected suggestResultMsg, got %T", resultMsg)
	}
	m.Update(result)

	if len(fc.calls) == 0 {
		t.Fatal("expected exactly one round of RPC calls once the debounce settled")
	}
	for _, c := range fc.calls {
		if c.method != "workspace.tree" && c.method != "agent.list" {
			t.Errorf("unexpected RPC call: %s", c.method)
		}
	}
	if m.suggest == nil {
		t.Fatal("expected the suggestion panel to be populated")
	}
	found := false
	for _, match := range m.suggest.Matches {
		if match == "main.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected main.go among matches, got %v", m.suggest.Matches)
	}
}

func TestSuggestStaleDebounceTickIsANoOp(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"workspace.tree": []treeEntry{{Path: "main.go"}},
		"agent.list":     map[string]any{"agents": []map[string]any{}},
	}}
	m, _ := newTestModel(fc)

	// Simulate "@a" then "@ag" arriving before any tick fires: two
	// triggerSuggest calls happen back-to-back (as two keystrokes would
	// produce), each bumping suggestGen. Only the second's tick should ever
	// be honored.
	firstCmd := m.triggerSuggest(mentionTrigger{Kind: mentionFile, Query: "a", Start: 0}, 0)
	secondCmd := m.triggerSuggest(mentionTrigger{Kind: mentionFile, Query: "ag", Start: 0}, 0)

	firstMsg := firstCmd().(suggestDebounceMsg)
	secondMsg := secondCmd().(suggestDebounceMsg)

	if firstMsg.gen == secondMsg.gen {
		t.Fatal("two distinct triggerSuggest calls must produce distinct generations")
	}

	// Deliver the STALE (first) tick first: must be a no-op (no fetch
	// command returned).
	_, cmd := m.Update(firstMsg)
	if cmd != nil {
		t.Error("a stale debounce tick must not schedule a fetch")
	}
	if len(fc.calls) != 0 {
		t.Fatalf("a stale debounce tick must not touch the RPC layer; got %d calls", len(fc.calls))
	}

	// Deliver the CURRENT (second) tick: must schedule exactly one fetch.
	_, cmd = m.Update(secondMsg)
	if cmd == nil {
		t.Fatal("the current debounce tick must schedule a fetch")
	}
	resultMsg := cmd()
	result := resultMsg.(suggestResultMsg)
	if result.trigger.Query != "ag" {
		t.Errorf("expected the fetch to be for query %q, got %q", "ag", result.trigger.Query)
	}
}

func TestSuggestStaleFetchResultIsANoOp(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"workspace.tree": []treeEntry{{Path: "main.go"}},
		"agent.list":     map[string]any{"agents": []map[string]any{}},
	}}
	m, _ := newTestModel(fc)

	staleResult := suggestResultMsg{
		gen:     m.suggestGen - 1, // older than the model's current generation
		trigger: mentionTrigger{Kind: mentionFile, Query: "main", Start: 0},
		matches: []string{"main.go"},
	}
	m.Update(staleResult)
	if m.suggest != nil {
		t.Error("a stale (superseded) fetch result must not open/repopulate the panel")
	}
}

func TestSuggestFileTriggerCallsWorkspaceTreeNotRawReadDir(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"workspace.tree": []treeEntry{{Path: "README.md"}},
		"agent.list":     map[string]any{"agents": []map[string]any{}},
	}}
	m, _ := newTestModel(fc)
	cmd := m.triggerSuggest(mentionTrigger{Kind: mentionFile, Query: "", Start: 0}, 0)
	msg := cmd().(suggestDebounceMsg)
	_, fetchCmd := m.Update(msg)
	fetchCmd()

	sawTree := false
	for _, c := range fc.calls {
		if c.method == "workspace.tree" {
			sawTree = true
		}
	}
	if !sawTree {
		t.Error("expected a workspace.tree RPC call for a file-mention trigger")
	}
}

func TestApplySuggestSelectionSplicesIntoInput(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("hello @ma world")
	// Cursor at end by default after SetValue; move it to just after "@ma"
	// (index 10) to simulate the operator having typed up to there.
	for m.input.Column() > 10 {
		m.input.SetCursorColumn(m.input.Column() - 1)
	}
	m.suggest = &suggestState{
		Kind:    mentionFile,
		Query:   "ma",
		Matches: []string{"main.go", "makefile"},
		Start:   6, // rune offset of '@' in "hello @ma world"
		Row:     0,
	}
	m.suggestGen++
	m.applySuggestSelection(0)

	want := "hello @main.go world"
	if got := m.input.Value(); got != want {
		t.Errorf("applySuggestSelection: got %q, want %q", got, want)
	}
	if m.suggest != nil {
		t.Error("selection must close the suggestion panel")
	}
}

func TestApplySuggestSelectionMidString(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.input.SetValue("@fi and more text")
	for m.input.Column() > 3 {
		m.input.SetCursorColumn(m.input.Column() - 1)
	}
	m.suggest = &suggestState{
		Kind:    mentionFile,
		Query:   "fi",
		Matches: []string{"file.go"},
		Start:   0,
		Row:     0,
	}
	m.suggestGen++
	m.applySuggestSelection(0)

	want := "@file.go  and more text"
	if got := m.input.Value(); got != want {
		t.Errorf("applySuggestSelection mid-string: got %q, want %q", got, want)
	}
}

func TestApprovalOverlaySuppressesSuggestionKeys(t *testing.T) {
	m, _ := newTestModel(&fakeCaller{})
	m.suggest = &suggestState{Kind: mentionFile, Matches: []string{"a.go", "b.go"}}
	m.approval = &approvalState{DecisionID: "d1", Action: "command.exec"}
	// While an approval overlay is open, "1" must resolve the approval (its
	// own numeric convention), not select suggestion index 0 — approval
	// precedence is checked first in handleKey.
	_, handled := m.handleKey("1")
	if !handled {
		t.Fatal("expected the approval overlay to handle the keypress")
	}
	if m.suggest == nil {
		t.Error("the suggestion panel must be unaffected by a keypress the approval overlay consumed")
	}
}
