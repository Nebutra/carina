package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

const recentHistoryLimit = 200

type historyScope string

const (
	historyScopeSession   historyScope = "session"
	historyScopeWorkspace historyScope = "workspace"
	historyScopeGlobal    historyScope = "global"
)

type historyLoadedMsg struct {
	generation          int
	search              bool
	scope               historyScope
	entries             []string
	err                 error
	modelLoaded         bool
	nextModel           string
	nextReasoningEffort string
	modelErr            error
}

type historySearchStatus int

const (
	historySearchIdle historySearchStatus = iota
	historySearchMatch
	historySearchNoMatch
)

// historySearchState owns one Ctrl+R interaction. Query input stays separate
// from the textarea preview, while original preserves the complete draft for
// lossless Ctrl+C cancellation.
type historySearchState struct {
	original       composerSnapshot
	query          string
	status         historySearchStatus
	matches        []int // history indexes, newest first
	position       int
	scope          historyScope
	loadedScope    historyScope
	entries        []promptDraft
	loading        bool
	loadGeneration int
	loadError      string
}

func (m *Model) loadRecentHistory(call Caller) tea.Cmd {
	if call == nil {
		return nil
	}
	m.historyLoadGen++
	generation := m.historyLoadGen
	sessionID := m.sessionID
	return func() tea.Msg {
		var historyOut struct {
			Entries             []string `json:"entries"`
			NextModel           string   `json:"next_model"`
			NextReasoningEffort string   `json:"next_reasoning_effort"`
		}
		historyErr := call.Call("history.recent", map[string]any{"limit": recentHistoryLimit, "scope": string(historyScopeWorkspace), "session_id": sessionID}, &historyOut)
		return historyLoadedMsg{generation: generation, scope: historyScopeWorkspace, entries: historyOut.Entries, err: historyErr, modelLoaded: historyErr == nil, nextModel: historyOut.NextModel, nextReasoningEffort: historyOut.NextReasoningEffort}
	}
}

func (m *Model) loadHistoryScope(call Caller, scope historyScope, generation int, search bool) tea.Cmd {
	sessionID := m.sessionID
	return func() tea.Msg {
		if call == nil {
			return historyLoadedMsg{generation: generation, search: search, scope: scope, err: fmt.Errorf("daemon not connected")}
		}
		var out struct {
			Entries []string `json:"entries"`
		}
		err := call.Call("history.recent", map[string]any{
			"limit": recentHistoryLimit, "scope": string(scope), "session_id": sessionID,
		}, &out)
		return historyLoadedMsg{generation: generation, search: search, scope: scope, entries: out.Entries, err: err}
	}
}

func (m *Model) handleHistoryLoaded(msg historyLoadedMsg) {
	if msg.search {
		search := m.historySearch
		if search == nil || msg.generation != search.loadGeneration || msg.scope != search.scope {
			return
		}
		search.loading = false
		if msg.err != nil {
			if search.loadedScope != "" {
				search.scope = search.loadedScope
				search.loadError = m.text(MsgHistoryLoadKept, MessageArgs{
					"scope": m.historyScopeText(msg.scope), "kept": m.historyScopeText(search.loadedScope),
				})
			} else {
				search.entries = nil
				search.matches = nil
				search.position = -1
				search.status = historySearchNoMatch
				search.loadError = m.text(MsgHistoryLoadCleared, MessageArgs{"scope": m.historyScopeText(msg.scope)})
				m.restoreComposerSnapshot(search.original)
			}
			return
		}
		search.loadError = ""
		search.loadedScope = msg.scope
		search.entries = promptDraftsFromStrings(msg.entries)
		m.reconcileHistorySearchEntries()
		return
	}
	if msg.generation != m.historyLoadGen {
		return
	}
	if msg.modelLoaded {
		m.handleModelPreference(modelPreferenceMsg{sessionID: m.sessionID, generation: m.sessionGeneration, loaded: true, model: msg.nextModel, effort: msg.nextReasoningEffort, err: msg.modelErr})
	}
	// An unsupported daemon and a lost connection are expected downgrade paths.
	// History improves recall, but must never interrupt the active composer.
	if msg.err != nil {
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
	if m.historySearch != nil && m.historySearch.loadedScope == historyScopeWorkspace {
		m.historySearch.entries = clonePromptHistory(m.history)
		m.reconcileHistorySearchEntries()
	}
}

func promptDraftsFromStrings(entries []string) []promptDraft {
	result := make([]promptDraft, 0, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry) != "" {
			result = append(result, promptDraft{Text: entry})
		}
	}
	return mergePromptHistory(nil, result)
}

func clonePromptHistory(entries []promptDraft) []promptDraft {
	result := make([]promptDraft, len(entries))
	for i := range entries {
		result[i] = cloneDraft(entries[i])
	}
	return result
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
		original:    m.composerSnapshot(),
		status:      historySearchIdle,
		position:    -1,
		scope:       historyScopeWorkspace,
		loadedScope: historyScopeWorkspace,
		entries:     clonePromptHistory(m.history),
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
	if search.loading && (m.keys.matches(KeyContextHistory, ActionHistoryPrevious, key) ||
		m.keys.matches(KeyContextHistory, ActionHistoryNext, key) ||
		m.keys.matches(KeyContextHistory, ActionHistoryExecute, key) ||
		m.keys.matches(KeyContextHistory, ActionHistoryAccept, key)) {
		return nil, true
	}
	switch {
	case m.keys.matches(KeyContextHistory, ActionHistoryPrevious, key):
		m.stepHistorySearch(-1)
	case m.keys.matches(KeyContextHistory, ActionHistoryNext, key):
		m.stepHistorySearch(1)
	case m.keys.matches(KeyContextHistory, ActionHistoryExecute, key):
		if search.status == historySearchMatch {
			return m.acceptHistorySearch(true), true
		}
	case m.keys.matches(KeyContextHistory, ActionHistoryAccept, key):
		return m.acceptHistorySearch(false), true
	case m.keys.matches(KeyContextHistory, ActionHistoryCancel, key):
		return m.cancelHistorySearch(), true
	case m.keys.matches(KeyContextHistory, ActionHistoryCycleScope, key):
		return m.cycleHistorySearchScope(), true
	case m.keys.matches(KeyContextHistory, ActionHistoryDelete, key):
		if search.query != "" {
			m.updateHistorySearchQuery(dropLastGrapheme(search.query))
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
	search.matches = historyMatchesIn(search.entries, query)
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
	entries := m.history
	if m.historySearch != nil {
		entries = m.historySearch.entries
	}
	return historyMatchesIn(entries, query)
}

func historyMatchesIn(entries []promptDraft, query string) []int {
	query = strings.ToLower(query)
	if query == "" {
		return nil
	}
	seen := make(map[string]bool)
	matches := make([]int, 0)
	for i := len(entries) - 1; i >= 0; i-- {
		draft := entries[i]
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
	m.restoreDraft(search.entries[search.matches[search.position]])
}

func (m *Model) acceptHistorySearch(execute bool) tea.Cmd {
	search := m.historySearch
	if search == nil {
		return nil
	}
	before := search.original
	if search.status != historySearchMatch {
		m.restoreComposerSnapshot(search.original)
	}
	m.historySearch = nil
	m.historyPos = len(m.history)
	m.historyScratch = promptDraft{}
	m.recordComposerEdit(before, composerEditOther)
	m.layout()
	if execute {
		return m.submitWithIntent(false)
	}
	return m.resumeQueuedAfterTransient()
}

func (m *Model) cycleHistorySearchScope() tea.Cmd {
	search := m.historySearch
	if search == nil {
		return nil
	}
	switch search.scope {
	case historyScopeSession:
		search.scope = historyScopeWorkspace
	case historyScopeWorkspace:
		search.scope = historyScopeGlobal
	default:
		search.scope = historyScopeSession
	}
	search.loading = true
	search.loadError = ""
	search.loadGeneration++
	return m.loadHistoryScope(m.call, search.scope, search.loadGeneration, true)
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
	if m.historySearch != nil && m.historySearch.loadedScope == historyScopeWorkspace {
		m.historySearch.entries = clonePromptHistory(m.history)
	}
	m.reconcileHistorySearchEntries()
}

func (m *Model) reconcileHistorySearchEntries() {
	search := m.historySearch
	if search == nil || search.query == "" {
		return
	}
	var currentKey string
	if search.status == historySearchMatch {
		currentKey = historyDraftKey(m.currentDraft())
	}
	search.matches = historyMatchesIn(search.entries, search.query)
	search.position = -1
	for position, index := range search.matches {
		if historyDraftKey(search.entries[index]) == currentKey {
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
	status := m.text(MsgHistoryTypeToSearch, nil)
	switch search.status {
	case historySearchMatch:
		status = m.text(MsgHistoryMatch, nil)
	case historySearchNoMatch:
		status = m.text(MsgHistoryNoMatch, nil)
	}
	if search.loading {
		status = m.text(MsgHistoryLoading, nil)
	} else if search.loadError != "" {
		status = search.loadError
	}
	scope := m.historyScopeText(search.scope)
	marker := "\ufff0"
	args := MessageArgs{"query": marker, "scope": scope, "status": status}
	var rendered string
	if width >= 64 {
		args["older"] = m.keys.label(KeyContextHistory, ActionHistoryPrevious)
		args["newer"] = m.keys.label(KeyContextHistory, ActionHistoryNext)
		args["run"] = m.keys.label(KeyContextHistory, ActionHistoryExecute)
		args["edit"] = m.keys.label(KeyContextHistory, ActionHistoryAccept)
		args["cycle"] = m.keys.label(KeyContextHistory, ActionHistoryCycleScope)
		args["cancel"] = m.keys.label(KeyContextHistory, ActionHistoryCancel)
		rendered = m.text(MsgHistoryWide, args)
	} else if width >= 24 {
		rendered = m.text(MsgHistoryMedium, args)
	} else {
		rendered = m.text(MsgHistoryTiny, args)
	}
	prefix, suffix, ok := strings.Cut(rendered, marker)
	if !ok {
		return rendered, search.query, ""
	}
	return prefix, search.query, suffix
}

func (m *Model) historyScopeText(scope historyScope) string {
	switch scope {
	case historyScopeSession:
		return m.text(MsgHistoryScopeSession, nil)
	case historyScopeGlobal:
		return m.text(MsgHistoryScopeGlobal, nil)
	default:
		return m.text(MsgHistoryScopeWorkspace, nil)
	}
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
