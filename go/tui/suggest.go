package tui

import (
	"errors"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// errSuggestNotConnected mirrors the "daemon not connected" guard used by
// the other async RPC paths in update.go (cancelTask, submit, shellCommand)
// — the suggestion fetch degrades the same way any other RPC does when the
// daemon link isn't up, rather than defining a new failure mode.
var errSuggestNotConnected = errors.New("daemon not connected")

// suggestDebounce is the quiet period after the last edit to a mention/slash
// query before a suggestion fetch actually fires. Tuned as a placeholder
// against no existing TUI latency budget (none exists elsewhere in go/tui to
// anchor against) — long enough that ordinary typing speed never fires a
// fetch per keystroke, short enough that the panel still feels responsive
// once the operator pauses.
const suggestDebounce = 150 * time.Millisecond

// suggestMaxResults caps how many matches are ever shown, so the panel stays
// a fixed, glanceable size and stays within the number-key (1-9) selection
// range.
const suggestMaxResults = 8

// treeCacheTTL bounds how often workspace.tree is re-fetched to build the
// file-suggestion corpus: the tree itself rarely changes within a single
// typing burst, so repeated keystrokes filter the cached list client-side
// instead of re-issuing the RPC. Only the fetch is debounced/cached; the
// prefix filter runs fresh on every accepted trigger.
const treeCacheTTL = 5 * time.Second

// builtinCommandNames are the slash commands handled directly in
// slashCommand()/showHelp() that do not come from command.list (which lists
// project/user-defined commands only). Suggestion completion merges this
// list with the RPC results so the two surfaces cannot silently diverge.
var builtinCommandNames = []string{"help", "keys", "search", "recap", "mode", "agents", "checkpoints"}

// treeEntry is the subset of toolchain.FileEntry the suggestion panel needs.
// Kept local (rather than importing go/toolchain) to avoid a new import
// cycle risk from the tui package into the daemon's tool surface; the shape
// is decoded straight off the workspace.tree RPC response.
type treeEntry struct {
	Path string `json:"path"`
}

// suggestState is the transient, non-transcript suggestion panel. It is
// rendered as an extra block between the transcript and the input (view.go),
// never injected into the transcript, and never takes over the full frame
// the way the approval/question overlays correctly do — the operator is
// still mid-typing while it is open.
type suggestState struct {
	Kind    mentionKind
	Query   string
	Matches []string // display form as it should be spliced into the input, without the trigger char
	Start   int      // rune offset of the trigger character within the current line
	Row     int      // textarea row the trigger belongs to
}

// suggestDebounceMsg fires suggestDebounce after a trigger's query last
// changed. Carries the generation it was scheduled under so a superseded
// keystroke's stale tick is a cheap no-op instead of firing a fetch.
type suggestDebounceMsg struct {
	gen     int
	trigger mentionTrigger
	row     int
}

// suggestResultMsg carries a completed fetch. Also generation-guarded: if
// the operator kept typing or closed the panel while the RPC was in flight,
// a late result is dropped rather than reopening/repopulating a panel the
// operator has moved past.
type suggestResultMsg struct {
	gen     int
	trigger mentionTrigger
	row     int
	matches []string
	err     error

	// refreshedTree/freshEntries/freshRoot carry a workspace.tree re-fetch
	// back to Update so it can populate the client-side cache; zero values
	// when the cached tree was reused (no re-fetch happened) or the trigger
	// wasn't a file mention.
	refreshedTree bool
	freshEntries  []treeEntry
	freshRoot     string
}

// triggerSuggest schedules a debounced suggestion fetch for tr. It bumps
// suggestGen immediately (invalidating any fetch/tick already in flight) and
// returns a tea.Cmd that, after the debounce window, re-checks the
// generation before doing any I/O. Both hops (tick, then fetch) return
// through the bubbletea runtime's own scheduling — Update never blocks.
func (m *Model) triggerSuggest(tr mentionTrigger, row int) tea.Cmd {
	m.suggestGen++
	gen := m.suggestGen
	return tea.Tick(suggestDebounce, func(time.Time) tea.Msg {
		return suggestDebounceMsg{gen: gen, trigger: tr, row: row}
	})
}

// closeSuggest dismisses the panel and invalidates any fetch/tick already
// scheduled for it, so a late result cannot reopen it.
func (m *Model) closeSuggest() {
	m.suggest = nil
	m.suggestGen++
}

// handleSuggestDebounce reacts to a settled debounce tick: stale generations
// are dropped (a newer keystroke superseded this one, which is what makes
// this a debounce rather than a plain delay); otherwise it kicks off the
// actual fetch as a second, independently generation-guarded tea.Cmd.
func (m *Model) handleSuggestDebounce(msg suggestDebounceMsg) tea.Cmd {
	if msg.gen != m.suggestGen {
		return nil
	}
	return m.fetchSuggestions(msg.trigger, msg.row, msg.gen)
}

// fetchSuggestions performs the governed read (agent.list / command.list /
// workspace.tree — all existing, already-scoped daemon RPCs; workspace.tree
// is additionally gated server-side behind a FileRead kernel decision) and
// returns the matches as a suggestResultMsg. No new RPC, no new kernel
// capability: this reuses read-scope surfaces the daemon already exposes and
// already audits.
func (m *Model) fetchSuggestions(tr mentionTrigger, row int, gen int) tea.Cmd {
	call := m.call
	sid := m.sessionID
	switch tr.Kind {
	case mentionCommand:
		return func() tea.Msg {
			matches := filterPrefix(builtinCommandNames, tr.Query, suggestMaxResults)
			if call != nil {
				var out struct {
					Commands []struct {
						Name string `json:"name"`
					} `json:"commands"`
				}
				if err := call.Call("command.list", map[string]any{"session_id": sid}, &out); err == nil {
					names := make([]string, 0, len(out.Commands))
					for _, c := range out.Commands {
						names = append(names, c.Name)
					}
					matches = mergeUnique(matches, filterPrefix(names, tr.Query, suggestMaxResults))
				}
			}
			sort.Strings(matches)
			if len(matches) > suggestMaxResults {
				matches = matches[:suggestMaxResults]
			}
			return suggestResultMsg{gen: gen, trigger: tr, row: row, matches: matches}
		}
	case mentionFile:
		root, cached, cacheAt := m.workspaceRoot, m.treeCache, m.treeCacheAt
		cacheRoot := m.treeCacheRoot
		return func() tea.Msg {
			entries := cached
			fresh := cacheRoot == root && time.Since(cacheAt) < treeCacheTTL && cached != nil
			refreshed := false
			if !fresh {
				if call == nil {
					return suggestResultMsg{gen: gen, trigger: tr, row: row, err: errSuggestNotConnected}
				}
				var out []treeEntry
				if err := call.Call("workspace.tree", map[string]any{"session_id": sid}, &out); err != nil {
					return suggestResultMsg{gen: gen, trigger: tr, row: row, err: err}
				}
				entries = out
				refreshed = true
			}
			var agentNames []string
			if call != nil {
				var out struct {
					Agents []struct {
						Name string `json:"name"`
					} `json:"agents"`
				}
				if err := call.Call("agent.list", map[string]any{"session_id": sid}, &out); err == nil {
					for _, a := range out.Agents {
						agentNames = append(agentNames, a.Name)
					}
				}
			}
			paths := make([]string, 0, len(entries))
			for _, e := range entries {
				paths = append(paths, e.Path)
			}
			matches := filterPrefix(paths, tr.Query, suggestMaxResults)
			matches = mergeUnique(matches, filterPrefix(agentNames, tr.Query, suggestMaxResults))
			sortByLengthThenAlpha(matches)
			if len(matches) > suggestMaxResults {
				matches = matches[:suggestMaxResults]
			}
			return suggestResultMsg{
				gen: gen, trigger: tr, row: row, matches: matches,
				refreshedTree: refreshed, freshEntries: entries, freshRoot: root,
			}
		}
	default:
		return nil
	}
}

// handleSuggestResult applies a completed fetch to model state. Stale
// generations (the operator kept typing, or closed the panel, while the RPC
// was in flight) are dropped without effect — the async fetch never
// resurrects a panel the operator has already moved past.
func (m *Model) handleSuggestResult(msg suggestResultMsg) {
	if msg.refreshedTree {
		m.treeCache = msg.freshEntries
		m.treeCacheAt = m.now()
		m.treeCacheRoot = msg.freshRoot
	}
	if msg.gen != m.suggestGen {
		return
	}
	if msg.err != nil || len(msg.matches) == 0 {
		m.suggest = nil
		return
	}
	m.suggest = &suggestState{
		Kind:    msg.trigger.Kind,
		Query:   msg.trigger.Query,
		Matches: msg.matches,
		Start:   msg.trigger.Start,
		Row:     msg.row,
	}
}

// applySuggestSelection splices matches[idx] into the input at the trigger's
// row, replacing [Start:cursorColumn) with the chosen text (plus the
// trigger char it followed). Scoped to the textarea's current row: the
// bubbles/v2 textarea.Model exposes Line()/Column() (row + rune offset
// within that row) and SetCursorColumn (absolute column within the current
// row), but no direct multi-line absolute-cursor API, so selection is
// intentionally single-line — matching how the trigger itself is detected.
func (m *Model) applySuggestSelection(idx int) {
	if m.suggest == nil || idx < 0 || idx >= len(m.suggest.Matches) {
		return
	}
	chosen := m.suggest.Matches[idx]
	row := m.suggest.Row
	if row != m.input.Line() {
		// The operator moved to a different row since the panel opened;
		// selection no longer makes sense against a row that isn't current.
		m.closeSuggest()
		return
	}
	line := currentLine(m.input.Value(), row)
	runes := []rune(line)
	start := m.suggest.Start
	cursor := m.input.Column()
	if start < 0 || start > len(runes) || cursor < start || cursor > len(runes) {
		m.closeSuggest()
		return
	}
	prefixChar := ""
	if m.suggest.Kind != mentionCommand {
		prefixChar = "@"
	} else {
		prefixChar = "/"
	}
	replacement := prefixChar + chosen + " "
	newLine := string(runes[:start]) + replacement + string(runes[cursor:])
	newCol := start + len([]rune(replacement))

	lines := strings.Split(m.input.Value(), "\n")
	if row >= 0 && row < len(lines) {
		lines[row] = newLine
	}
	// SetValue resets the textarea and re-inserts the full string, which
	// leaves the cursor at the absolute end (last row) regardless of which
	// row was edited — walk back up to the target row before setting the
	// column within it.
	m.input.SetValue(strings.Join(lines, "\n"))
	for i := m.input.Line(); i > row; i-- {
		m.input.CursorUp()
	}
	m.input.SetCursorColumn(newCol)
	m.closeSuggest()
}

// filterPrefix returns items whose lowercased form contains query
// (case-insensitive substring match), capped to max results, preferring
// prefix matches first.
func filterPrefix(items []string, query string, max int) []string {
	q := strings.ToLower(query)
	var prefixMatches, substrMatches []string
	for _, it := range items {
		low := strings.ToLower(it)
		switch {
		case strings.HasPrefix(low, q):
			prefixMatches = append(prefixMatches, it)
		case q != "" && strings.Contains(low, q):
			substrMatches = append(substrMatches, it)
		case q == "":
			prefixMatches = append(prefixMatches, it)
		}
	}
	out := append(prefixMatches, substrMatches...)
	if len(out) > max {
		out = out[:max]
	}
	return out
}

func mergeUnique(a, b []string) []string {
	seen := make(map[string]bool, len(a))
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		if !seen[x] {
			a = append(a, x)
			seen[x] = true
		}
	}
	return a
}

func sortByLengthThenAlpha(items []string) {
	sort.Slice(items, func(i, j int) bool {
		if len(items[i]) != len(items[j]) {
			return len(items[i]) < len(items[j])
		}
		return items[i] < items[j]
	})
}
