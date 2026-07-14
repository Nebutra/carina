package tui

import (
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

const recentHistoryLimit = 200

type historyLoadedMsg struct {
	generation int
	entries    []string
	err        error
}

type historySearchStatus int

const (
	historySearchIdle historySearchStatus = iota
	historySearchMatch
	historySearchNoMatch
)

// historySearchState owns one Ctrl+R interaction. Query input stays separate
// from the textarea preview, while original preserves the complete draft for
// lossless Esc/Ctrl+C cancellation.
type historySearchState struct {
	original composerSnapshot
	query    string
	status   historySearchStatus
	matches  []int // history indexes, newest first
	position int
}

func (m *Model) loadRecentHistory(call Caller) tea.Cmd {
	if call == nil {
		return nil
	}
	m.historyLoadGen++
	generation := m.historyLoadGen
	return func() tea.Msg {
		var out struct {
			Entries []string `json:"entries"`
		}
		err := call.Call("history.recent", map[string]any{"limit": recentHistoryLimit}, &out)
		return historyLoadedMsg{generation: generation, entries: out.Entries, err: err}
	}
}

func (m *Model) handleHistoryLoaded(msg historyLoadedMsg) {
	// An unsupported daemon and a lost connection are expected downgrade paths.
	// History improves recall, but must never interrupt the active composer.
	if msg.err != nil || msg.generation != m.historyLoadGen {
		return
	}

	oldLen, oldPos := len(m.history), m.historyPos
	var navigated *promptDraft
	if oldPos >= 0 && oldPos < oldLen {
		draft := m.history[oldPos]
		navigated = &draft
	}
	m.history = mergePromptHistory(msg.entries, m.history)

	switch {
	case navigated != nil:
		m.historyPos = findDraft(m.history, *navigated)
		if m.historyPos < 0 {
			m.historyPos = len(m.history)
		}
	case oldPos >= oldLen:
		m.historyPos = len(m.history)
	default:
		m.historyPos = clampInt(oldPos, 0, len(m.history))
	}
	m.reconcileHistorySearchAfterMerge()
}

// mergePromptHistory treats the daemon result as the older prefix and the
// current process history as the newer suffix. Keeping the last equivalent
// occurrence both deduplicates persistent repeats and guarantees that entries
// submitted while the RPC was in flight remain the most recent values.
func mergePromptHistory(remote []string, local []promptDraft) []promptDraft {
	combined := make([]promptDraft, 0, len(remote)+len(local))
	for _, entry := range remote {
		if strings.TrimSpace(entry) != "" {
			combined = append(combined, promptDraft{Text: entry})
		}
	}
	for _, draft := range local {
		draft.Prefix = append([]string(nil), draft.Prefix...)
		draft.Paste = append([]string(nil), draft.Paste...)
		if historyDraftKey(draft) != "" {
			combined = append(combined, draft)
		}
	}

	seen := make(map[string]bool, len(combined))
	reversed := make([]promptDraft, 0, len(combined))
	for i := len(combined) - 1; i >= 0; i-- {
		key := historyDraftKey(combined[i])
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		reversed = append(reversed, combined[i])
	}
	result := make([]promptDraft, len(reversed))
	for i := range reversed {
		result[len(reversed)-1-i] = reversed[i]
	}
	return result
}

func historyDraftKey(draft promptDraft) string {
	value := strings.ReplaceAll(draftPrompt(draft), "\r\n", "\n")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}

func findDraft(history []promptDraft, want promptDraft) int {
	key := historyDraftKey(want)
	for i := len(history) - 1; i >= 0; i-- {
		if historyDraftKey(history[i]) == key {
			return i
		}
	}
	return -1
}

func (m *Model) beginHistorySearch() bool {
	if m.historySearch != nil || m.submitting != nil {
		return m.historySearch != nil
	}
	m.closeSuggest()
	m.historySearch = &historySearchState{
		original: m.composerSnapshot(),
		status:   historySearchIdle,
		position: -1,
	}
	m.historyPos = len(m.history)
	m.historyScratch = promptDraft{}
	m.layout()
	return true
}

// historySearchKey consumes the entire keyboard while search is visible.
// Callers route overlays first, then this method, so governance prompts retain
// modal priority without allowing global Ctrl+C to destroy a search draft.
func (m *Model) historySearchKey(key string) (tea.Cmd, bool) {
	text := key
	if strings.Contains(key, "+") {
		text = ""
	}
	switch key {
	case "up", "down", "left", "right", "enter", "tab", "esc", "backspace", "delete", "home", "end", "pgup", "pgdown":
		text = ""
	case "space":
		text = " "
	}
	return m.historySearchKeyText(key, text)
}

func (m *Model) historySearchKeyPress(msg tea.KeyPressMsg) tea.Cmd {
	cmd, _ := m.historySearchKeyText(msg.String(), msg.Key().Text)
	return cmd
}

func (m *Model) historySearchKeyText(key, text string) (tea.Cmd, bool) {
	search := m.historySearch
	if search == nil {
		return nil, false
	}
	switch {
	case m.keys.matches(KeyContextHistory, ActionHistoryPrevious, key):
		m.stepHistorySearch(-1)
	case m.keys.matches(KeyContextHistory, ActionHistoryNext, key):
		m.stepHistorySearch(1)
	case m.keys.matches(KeyContextHistory, ActionHistoryAccept, key):
		if search.status == historySearchMatch {
			before := search.original
			m.historySearch = nil
			m.historyPos = len(m.history)
			m.historyScratch = promptDraft{}
			m.recordComposerEdit(before, composerEditOther)
			m.layout()
			return m.resumeQueuedAfterTransient(), true
		}
	case m.keys.matches(KeyContextHistory, ActionHistoryCancel, key):
		return m.cancelHistorySearch(), true
	case m.keys.matches(KeyContextHistory, ActionHistoryDelete, key):
		runes := []rune(search.query)
		if len(runes) > 0 {
			m.updateHistorySearchQuery(string(runes[:len(runes)-1]))
		}
	case m.keys.matches(KeyContextHistory, ActionHistoryClear, key):
		m.updateHistorySearchQuery("")
	default:
		if text, ok := historySearchInput(text); ok {
			m.updateHistorySearchQuery(search.query + text)
			return nil, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key) {
			return tea.ClearScreen, true
		}
	}
	return nil, true
}

func historySearchInput(key string) (string, bool) {
	if key == "space" {
		return " ", true
	}
	runes := []rune(key)
	if len(runes) == 0 {
		return "", false
	}
	for _, r := range runes {
		if unicode.IsControl(r) || r == '\n' || r == '\r' || r == '\t' {
			return "", false
		}
	}
	return key, true
}

func (m *Model) appendHistorySearchQuery(value string) {
	if m.historySearch == nil {
		return
	}
	value = sanitize(value)
	value = strings.ReplaceAll(value, "\n", " ")
	m.updateHistorySearchQuery(m.historySearch.query + value)
}

func (m *Model) updateHistorySearchQuery(query string) {
	search := m.historySearch
	if search == nil {
		return
	}
	search.query = query
	search.position = -1
	search.matches = m.historyMatches(query)
	if query == "" {
		search.status = historySearchIdle
		m.restoreComposerSnapshot(search.original)
		return
	}
	if len(search.matches) == 0 {
		search.status = historySearchNoMatch
		m.restoreComposerSnapshot(search.original)
		return
	}
	m.stepHistorySearch(-1)
}

func (m *Model) historyMatches(query string) []int {
	query = strings.ToLower(query)
	if query == "" {
		return nil
	}
	seen := make(map[string]bool)
	matches := make([]int, 0)
	for i := len(m.history) - 1; i >= 0; i-- {
		draft := m.history[i]
		key := historyDraftKey(draft)
		if key == "" || seen[key] || !strings.Contains(strings.ToLower(draftPrompt(draft)), query) {
			continue
		}
		seen[key] = true
		matches = append(matches, i)
	}
	return matches
}

func (m *Model) stepHistorySearch(direction int) {
	search := m.historySearch
	if search == nil || search.query == "" {
		return
	}
	if len(search.matches) == 0 {
		search.status = historySearchNoMatch
		m.restoreComposerSnapshot(search.original)
		return
	}
	if direction < 0 {
		if search.position < len(search.matches)-1 {
			search.position++
		}
	} else if search.position > 0 {
		search.position--
	}
	if search.position < 0 {
		search.position = 0
	}
	search.status = historySearchMatch
	m.restoreDraft(m.history[search.matches[search.position]])
}

func (m *Model) cancelHistorySearch() tea.Cmd {
	search := m.historySearch
	if search == nil {
		return nil
	}
	original := search.original
	m.historySearch = nil
	m.historyPos = len(m.history)
	m.historyScratch = promptDraft{}
	// A Ctrl+C used to cancel search is complete in itself and must not arm the
	// global double-press exit gesture.
	m.lastCtrlC = time.Time{}
	m.ctrlCHint = ""
	m.restoreComposerSnapshot(original)
	return m.resumeQueuedAfterTransient()
}

func (m *Model) reconcileHistorySearchAfterMerge() {
	search := m.historySearch
	if search == nil || search.query == "" {
		return
	}
	var currentKey string
	if search.status == historySearchMatch {
		currentKey = historyDraftKey(m.currentDraft())
	}
	search.matches = m.historyMatches(search.query)
	search.position = -1
	for position, index := range search.matches {
		if historyDraftKey(m.history[index]) == currentKey {
			search.position = position
			break
		}
	}
	if search.position >= 0 {
		return
	}
	if len(search.matches) == 0 {
		search.status = historySearchNoMatch
		m.restoreComposerSnapshot(search.original)
		return
	}
	m.stepHistorySearch(-1)
}

func (m *Model) historySearchPresentation(width int) (string, string, string) {
	search := m.historySearch
	if search == nil {
		return "", "", ""
	}
	status := "type to search"
	switch search.status {
	case historySearchMatch:
		status = "match"
	case historySearchNoMatch:
		status = "no match"
	}
	if width >= 64 {
		return "reverse-i-search: ", search.query,
			"  " + status + "  " +
				m.keys.label(KeyContextHistory, ActionHistoryPrevious) + " older  " +
				m.keys.label(KeyContextHistory, ActionHistoryNext) + " newer  " +
				m.keys.label(KeyContextHistory, ActionHistoryAccept) + " accept  " +
				m.keys.label(KeyContextHistory, ActionHistoryCancel) + " cancel"
	}
	if width >= 24 {
		return "history " + status + ": ", search.query, ""
	}
	return "?" + status + ":", search.query, ""
}

func (m *Model) historySearchPanelLine(width int) string {
	prefix, query, suffix := m.historySearchPresentation(width)
	line := m.th.Style(theme.RoleMuted).Render(prefix) +
		m.th.Style(theme.RoleInfo).Render(query) +
		m.th.Style(theme.RoleMuted).Render(suffix)
	return fitRenderedLine(line, maxInt(width, 1))
}

func (m *Model) historySearchCursorX(width int) int {
	prefix, query, _ := m.historySearchPresentation(width)
	return clampInt(ansi.StringWidth(prefix+query), 0, maxInt(width-1, 0))
}
