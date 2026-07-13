package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

func TestSessionReadyLoadsPersistentHistoryAsynchronouslyAndMergesLate(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{
			"entries": []string{"older", "duplicate", "newer", "duplicate"},
		},
	}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.input.SetValue("work in progress")
	m.pendingPaste = []string{"keep\nthis"}

	_, cmd := m.Update(SessionReadyMsg{SessionID: "sess_history", Call: fc})
	if cmd == nil || len(fc.calls) != 0 {
		t.Fatal("SessionReady must schedule history I/O without blocking Update")
	}
	// These entries represent successful local submissions while the daemon
	// RPC is still in flight. The loaded result must merge around them.
	m.recordHistory(promptDraft{Text: "during load"})
	m.recordHistory(promptDraft{Text: "duplicate"})
	m.historyPos = len(m.history)

	drain(m, cmd)
	if len(fc.calls) != 1 || fc.last().method != "history.recent" {
		t.Fatalf("history RPC calls = %#v", fc.calls)
	}
	if got := fc.last().params["limit"]; got != float64(recentHistoryLimit) {
		t.Fatalf("history limit = %#v, want %d", got, recentHistoryLimit)
	}
	if got, want := historyTexts(m.history), []string{"older", "newer", "during load", "duplicate"}; !stringSlicesEqual(got, want) {
		t.Fatalf("merged history = %#v, want %#v", got, want)
	}
	if got := m.currentDraft(); got.Text != "work in progress" || len(got.Paste) != 1 || got.Paste[0] != "keep\nthis" {
		t.Fatalf("late history load changed active draft: %+v", got)
	}
}

func TestHistoryLoadUnsupportedIsSilentAndKeepsDraft(t *testing.T) {
	fc := &fakeCaller{handler: map[string]any{"history.recent": errors.New("method not found")}}
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	m.input.SetValue("offline draft")
	m.pendingPaste = []string{"payload"}

	_, cmd := m.Update(SessionReadyMsg{SessionID: "sess_old_daemon", Call: fc})
	before := transcriptText(m)
	drain(m, cmd)
	if transcriptText(m) != before {
		t.Fatal("unsupported history RPC surfaced a noisy transcript error")
	}
	if got := m.currentDraft(); got.Text != "offline draft" || len(got.Paste) != 1 || got.Paste[0] != "payload" {
		t.Fatalf("history downgrade changed draft: %+v", got)
	}
}

func TestLateHistoryGenerationCannotReplaceNewerMerge(t *testing.T) {
	m := New(Options{Theme: theme.New(theme.Mono), Locale: "en"})
	old := m.loadRecentHistory(&fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []string{"stale"}},
	}})
	newer := m.loadRecentHistory(&fakeCaller{handler: map[string]any{
		"history.recent": map[string]any{"entries": []string{"fresh"}},
	}})
	drain(m, newer)
	drain(m, old)
	if got := historyTexts(m.history); !stringSlicesEqual(got, []string{"fresh"}) {
		t.Fatalf("stale load replaced newer history: %#v", got)
	}
}

func TestHistorySearchCancelRestoresExactDraftAndPaste(t *testing.T) {
	for _, cancelKey := range []string{"esc", "ctrl+c"} {
		t.Run(cancelKey, func(t *testing.T) {
			m, _ := newTestModel(nil)
			m.history = []promptDraft{{Text: "deploy production", Paste: []string{"matched\npaste"}}}
			m.historyPos = len(m.history)
			m.input.SetValue("unfinished 中文 draft")
			m.pendingPaste = []string{"original\npaste", "second item"}

			startHistorySearch(t, m)
			typeHistoryQuery(t, m, "deploy")
			if m.input.Value() != "deploy production" {
				t.Fatalf("match was not previewed: %q", m.input.Value())
			}
			cmd, handled := m.handleKey(cancelKey)
			if !handled || cmd != nil || m.historySearch != nil {
				t.Fatal("search cancellation leaked to the global key handler")
			}
			got := m.currentDraft()
			want := promptDraft{Text: "unfinished 中文 draft", Paste: []string{"original\npaste", "second item"}}
			if !draftsEqual(got, want) {
				t.Fatalf("cancel restored %+v, want %+v", got, want)
			}
			if !m.lastCtrlC.IsZero() || m.ctrlCHint != "" {
				t.Fatal("search Ctrl+C armed the global double-press exit state")
			}
		})
	}
}

func TestHistorySearchNoMatchKeepsOriginalAndQueryOpen(t *testing.T) {
	m, _ := newTestModel(nil)
	m.history = []promptDraft{{Text: "build release"}}
	m.historyPos = len(m.history)
	m.input.SetValue("original")
	m.pendingPaste = []string{"keep"}

	startHistorySearch(t, m)
	typeHistoryQuery(t, m, "missing")
	if m.historySearch == nil || m.historySearch.status != historySearchNoMatch {
		t.Fatal("missing query did not enter no-match state")
	}
	if got := m.currentDraft(); got.Text != "original" || len(got.Paste) != 1 {
		t.Fatalf("no match changed original draft: %+v", got)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "no match") {
		t.Fatal("no-match feedback is not visible")
	}
	if cmd, handled := m.handleKey("enter"); !handled || cmd != nil || m.historySearch == nil {
		t.Fatal("Enter accepted a nonexistent history match")
	}
}

func TestHistorySearchPasteOnlyEditsQueryAndCancelRestoresPendingPaste(t *testing.T) {
	m, _ := newTestModel(nil)
	m.history = []promptDraft{{Text: "部署 生产"}}
	m.historyPos = len(m.history)
	m.input.SetValue("original draft")
	m.pendingPaste = []string{"existing\npaste"}
	startHistorySearch(t, m)

	m.Update(tea.PasteMsg{Content: "部署\n生产"})
	if m.historySearch == nil {
		t.Fatal("search paste unexpectedly closed search mode")
	}
	if m.historySearch.query != "部署 生产" {
		t.Fatalf("search paste query = %q", m.historySearch.query)
	}
	if len(m.pendingPaste) != 0 {
		t.Fatalf("search PasteMsg leaked into preview pendingPaste: %#v", m.pendingPaste)
	}
	pressHistoryKey(t, m, "ctrl+c")
	if got := m.currentDraft(); got.Text != "original draft" || len(got.Paste) != 1 || got.Paste[0] != "existing\npaste" {
		t.Fatalf("cancel after search paste restored %+v", got)
	}
}

func TestHistorySearchTraversalSkipsDuplicatesAndMovesBothDirections(t *testing.T) {
	m, _ := newTestModel(nil)
	// Seed directly to cover legacy persistent duplicates as well as the
	// process-level dedup path used for new submissions.
	m.history = []promptDraft{
		{Text: "deploy alpha"},
		{Text: "deploy beta"},
		{Text: "deploy alpha"},
		{Text: "deploy gamma"},
	}
	m.historyPos = len(m.history)
	startHistorySearch(t, m)
	typeHistoryQuery(t, m, "deploy")
	assertDraftText(t, m, "deploy gamma")

	pressHistoryKey(t, m, "ctrl+r")
	assertDraftText(t, m, "deploy alpha")
	pressHistoryKey(t, m, "up")
	assertDraftText(t, m, "deploy beta")
	pressHistoryKey(t, m, "ctrl+r")
	assertDraftText(t, m, "deploy beta") // oldest boundary is stable
	pressHistoryKey(t, m, "ctrl+s")
	assertDraftText(t, m, "deploy alpha")
	pressHistoryKey(t, m, "down")
	assertDraftText(t, m, "deploy gamma")
}

func TestHistorySearchCJKAndAcceptAsEditableDraft(t *testing.T) {
	m, _ := newTestModel(nil)
	m.history = []promptDraft{
		{Text: "部署到北京"},
		{Text: "发布生产"},
		{Text: "部署到上海", Paste: []string{"保留附件说明"}},
	}
	m.historyPos = len(m.history)
	m.input.SetValue("搜索前草稿")

	startHistorySearch(t, m)
	typeHistoryQuery(t, m, "部署")
	assertDraftText(t, m, "部署到上海")
	pressHistoryKey(t, m, "ctrl+r")
	assertDraftText(t, m, "部署到北京")
	pressHistoryKey(t, m, "down")
	assertDraftText(t, m, "部署到上海")

	cmd, handled := m.handleKey("enter")
	if !handled || cmd != nil || m.historySearch != nil {
		t.Fatal("Enter did not accept the current match as an editable draft")
	}
	if got := m.currentDraft(); got.Text != "部署到上海" || len(got.Paste) != 1 || got.Paste[0] != "保留附件说明" {
		t.Fatalf("accepted CJK draft lost content: %+v", got)
	}
	if _, handled := m.handleKey("!"); handled {
		t.Fatal("accepted match is not editable by the normal textarea")
	}
}

func TestHistorySearchNarrowLayoutAndOverlayPriority(t *testing.T) {
	m, _ := newTestModel(nil)
	m.Update(tea.WindowSizeMsg{Width: 18, Height: 2})
	m.history = []promptDraft{{Text: "部署生产"}}
	m.historyPos = len(m.history)
	startHistorySearch(t, m)
	typeHistoryQuery(t, m, "部署")

	view := m.View()
	plain := ansi.Strip(view.Content)
	if !strings.Contains(plain, "match") || !strings.Contains(plain, "部署") {
		t.Fatalf("narrow search prompt lost query/status:\n%s", plain)
	}
	for _, line := range strings.Split(view.Content, "\n") {
		if width := ansi.StringWidth(line); width > 18 {
			t.Fatalf("narrow search line width = %d: %q", width, ansi.Strip(line))
		}
	}
	if view.Cursor == nil || view.Cursor.Position.X < 0 || view.Cursor.Position.X >= 18 || view.Cursor.Position.Y < 0 || view.Cursor.Position.Y >= 2 {
		t.Fatalf("search cursor outside terminal: %+v", view.Cursor)
	}

	query := m.historySearch.query
	m.approval = &approvalState{DecisionID: "perm_overlay", Action: "command.exec", Resource: "rm -rf build"}
	overlay := ansi.Strip(m.View().Content)
	if strings.Contains(overlay, query) || !strings.Contains(overlay, "allow") {
		t.Fatalf("history search rendered above governance overlay:\n%s", overlay)
	}
	if cmd, handled := m.handleKey("x"); !handled || cmd != nil || m.historySearch.query != query {
		t.Fatal("governance overlay did not retain keyboard priority")
	}
}

func TestLateHistoryMergePreservesActiveSearchPreview(t *testing.T) {
	m, _ := newTestModel(nil)
	m.history = []promptDraft{{Text: "local deploy"}}
	m.historyPos = len(m.history)
	startHistorySearch(t, m)
	typeHistoryQuery(t, m, "deploy")

	m.historyLoadGen = 7
	m.handleHistoryLoaded(historyLoadedMsg{generation: 7, entries: []string{"remote deploy"}})
	if m.historySearch == nil || m.historySearch.status != historySearchMatch {
		t.Fatal("late merge closed active search")
	}
	assertDraftText(t, m, "local deploy")
	pressHistoryKey(t, m, "ctrl+r")
	assertDraftText(t, m, "remote deploy")
}

func startHistorySearch(t *testing.T, m *Model) {
	t.Helper()
	cmd, handled := m.handleKey("ctrl+r")
	if !handled || cmd != nil || m.historySearch == nil {
		t.Fatal("Ctrl+R did not open history search")
	}
}

func typeHistoryQuery(t *testing.T, m *Model, query string) {
	t.Helper()
	for _, r := range query {
		pressHistoryKey(t, m, string(r))
	}
}

func pressHistoryKey(t *testing.T, m *Model, key string) {
	t.Helper()
	cmd, handled := m.handleKey(key)
	if !handled || cmd != nil {
		t.Fatalf("history key %q was not consumed", key)
	}
}

func assertDraftText(t *testing.T, m *Model, want string) {
	t.Helper()
	if got := m.input.Value(); got != want {
		t.Fatalf("draft text = %q, want %q", got, want)
	}
}

func historyTexts(history []promptDraft) []string {
	result := make([]string, len(history))
	for i, draft := range history {
		result[i] = historyDraftKey(draft)
	}
	return result
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
