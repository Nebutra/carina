package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui/theme"
)

// ctrlCWindow is the double-press window for the cascading interrupt (P1.4):
// the first Ctrl-C cancels the in-flight task, a second within this window
// exits.
const ctrlCWindow = 2 * time.Second

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// The cascading-interrupt arming window (ctrlC, below) is a strict
	// double-press gesture: it must disarm on intervening operator activity
	// (typing, pasting) so a Ctrl-C that merely lands inside the stale 2s
	// window — after the operator did something else entirely — is treated
	// as a fresh first press (cancel), not as confirmation of an earlier
	// press it was never the second half of. It deliberately does NOT
	// disarm on messages that are asynchronous fallout of the first Ctrl-C
	// itself (e.g. cancelDoneMsg from the task.cancel it triggered) — those
	// aren't "unrelated activity", and disarming on them would break the
	// documented cascade (first press cancels, second press within 2s
	// exits) the moment the cancel RPC's result arrives.
	switch kp := msg.(type) {
	case tea.KeyPressMsg:
		if !m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, kp.String()) {
			m.lastCtrlC = time.Time{}
			m.ctrlCHint = ""
		}
	case tea.PasteMsg:
		m.lastCtrlC = time.Time{}
		m.ctrlCHint = ""
	}

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case SessionReadyMsg:
		m.sessionID = msg.SessionID
		m.call = msg.Call
		m.conn = ConnConnected
		m.attempt = 0
		m.push(m.th.Style(theme.RoleMuted).Render("- attached to " + msg.SessionID))
		return m, m.loadRecentHistory(msg.Call)

	case TaskActiveMsg:
		if msg.TaskID != "" && msg.TaskID != m.inFlightTaskID {
			m.inFlightTaskID = msg.TaskID
			m.tasks.setTask(msg.TaskID, "running")
			m.push(m.th.Style(theme.RoleMuted).Render("- active task " + msg.TaskID + " restored"))
			m.layout()
		}
		return m, nil

	case ConnLostMsg:
		m.conn = ConnLost
		m.push(fmt.Sprintf("%s %s", glyphFailed(m.th), microcopy.Degrade(
			microcopy.DegradeDaemonUnreachable,
			microcopy.Args{"socket": m.socket},
			microcopy.WithLocale(m.locale), microcopy.WithPlain(true),
		)))
		return m, nil

	case ReconnectingMsg:
		m.conn = ConnReconnecting
		m.attempt = msg.Attempt
		return m, nil

	case ConnRestoredMsg:
		m.conn = ConnConnected
		m.attempt = 0
		m.push(m.th.Style(theme.RoleMuted).Render("- reconnected: live event stream resumed"))
		return m, nil

	case EventMsg:
		m.handleEvent(msg.Raw)
		return m, nil

	case submissionDoneMsg:
		m.handleSubmissionDone(msg)
		return m, nil

	case cancelDoneMsg:
		if msg.err != nil {
			m.push(fmt.Sprintf("%s cancel failed for task %s: %s", glyphFailed(m.th), msg.taskID, msg.err.Error()))
			return m, nil
		}
		if m.inFlightTaskID == msg.taskID {
			m.inFlightTaskID = ""
		}
		m.tasks.setTask(msg.taskID, "cancelled")
		m.push(m.th.Style(theme.RoleMuted).Render("- cancel recorded for task " + msg.taskID))
		m.layout()
		return m, nil

	case approvalDoneMsg:
		m.handleApprovalDone(msg)
		return m, nil

	case questionDoneMsg:
		m.handleQuestionDone(msg)
		return m, nil

	case historyLoadedMsg:
		m.handleHistoryLoaded(msg)
		return m, nil

	case rpcErrMsg:
		m.push(fmt.Sprintf("%s rpc: %s", glyphFailed(m.th), msg.err.Error()))
		return m, nil
	case surfaceResultMsg:
		m.push(m.th.Style(theme.RoleTitle).Render(msg.label) + "\n" + msg.text)
		return m, nil

	case suggestDebounceMsg:
		return m, m.handleSuggestDebounce(msg)

	case suggestResultMsg:
		m.handleSuggestResult(msg)
		return m, nil

	case tea.PasteMsg:
		m.pasteBurst.reset()
		// Governance overlays exclusively own input while open, including while
		// their RPC is resolving. Never let a terminal paste mutate the hidden
		// composer behind the modal.
		if m.approval != nil || m.question != nil || m.submitting != nil {
			return m, nil
		}
		if m.historySearch != nil {
			// Modal precedence is approval/question > history search > normal
			// composer paste handling. A hidden search never lets pasted bytes
			// leak into pendingPaste behind a governance overlay.
			if m.approval == nil && m.question == nil {
				m.appendHistorySearchQuery(msg.Content)
			}
			return m, nil
		}
		return m, m.handlePaste(msg)

	case tea.MouseWheelMsg:
		return m, m.handleMouseWheel(msg)

	case tea.KeyPressMsg:
		if m.approval != nil || m.question != nil || m.historySearch != nil || m.submitting != nil {
			m.pasteBurst.reset()
		} else if cmd, handled := m.handlePasteBurstKey(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.handleKey(msg.String()); handled {
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.layout()
	return m, tea.Batch(cmd, m.refreshSuggestTrigger())
}

// refreshSuggestTrigger re-evaluates the mention/slash trigger at the
// textarea's current cursor position after the input itself has processed a
// message (a keypress, paste, etc. that isn't otherwise intercepted by
// handleKey). It is the one place trigger detection is driven from, so it
// runs uniformly regardless of what edited the input.
func (m *Model) refreshSuggestTrigger() tea.Cmd {
	if m.approval != nil || m.question != nil {
		// Overlays own the keyboard; a mention panel must not appear behind
		// or interfere with them.
		return nil
	}
	row := m.input.Line()
	line := currentLine(m.input.Value(), row)
	tr := detectTrigger(line, m.input.Column())
	if tr.Kind == mentionNone {
		if m.suggest != nil {
			m.closeSuggest()
		}
		return nil
	}
	if m.suggest != nil && m.suggest.Row == row && m.suggest.Start == tr.Start && m.suggest.Query == tr.Query {
		return nil // no change since the last fetch/schedule
	}
	return m.triggerSuggest(tr, row)
}

// handleEvent renders a streamed event and reacts to governance moments.
func (m *Model) handleEvent(ev map[string]any) {
	m.pushEvent(ev)
	m.tasks.observeEvent(ev)
	m.observeQuestionResolution(ev)
	m.observeApprovalResolution(ev)
	switch str(ev["type"]) {
	case "permission.request":
		m.openApproval(ev)
	case "user.question":
		m.openQuestion(ev)
	case "task.completed":
		if id := str(ev["task_id"]); id != "" && id == m.inFlightTaskID {
			m.inFlightTaskID = ""
		}
	}
	m.layout()
}

// handleKey processes one key. It returns handled=false for keys that belong
// to the input box; Update forwards those (grapheme handling lives in
// bubbles' textinput).
func (m *Model) handleKey(key string) (tea.Cmd, bool) {
	// Governance is always the highest visual/input context. It must not be
	// covered by help or a transient search panel.
	if m.question != nil {
		if cmd, handled := m.questionKey(key); handled {
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key) {
			return tea.ClearScreen, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			return m.ctrlC(), true
		}
		return nil, true
	}
	if m.approval != nil {
		if cmd, handled := m.approvalKey(key); handled {
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key) {
			return tea.ClearScreen, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			return m.ctrlC(), true
		}
		return nil, true
	}

	// Help is a real overlay rather than transcript output. It owns navigation
	// and close keys, while redraw remains globally available.
	if m.helpOpen {
		if cmd, handled := m.helpKey(key); handled {
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key) {
			return tea.ClearScreen, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			m.closeHelp()
		}
		return nil, true
	}

	// Reverse search owns every key while visible. In particular Ctrl+C must
	// restore the exact draft instead of arming the global exit cascade.
	if m.historySearch != nil {
		return m.historySearchKey(key)
	}

	// The mention/slash suggestion panel is a transient, non-modal aid: it
	// never takes the keyboard away from the approval/question overlays
	// (those are already handled and returned above, so reaching here means
	// neither is open) and only intercepts a small set of keys itself.
	if m.suggest != nil && m.calculateLayout().suggestLines > 0 {
		if cmd, handled := m.suggestKey(key); handled {
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			m.closeSuggest()
			m.lastCtrlC = time.Time{}
			m.ctrlCHint = ""
			m.layout()
			return nil, true
		}
	}

	if m.submitting != nil {
		switch {
		case m.keys.matches(KeyContextGlobal, ActionGlobalHelp, key):
			m.showHelp()
			return nil, true
		case m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key):
			return tea.ClearScreen, true
		case m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key):
			return m.ctrlC(), true
		default:
			// Preserve the exact draft and caret until the RPC acknowledges it.
			return nil, true
		}
	}
	if m.keys.matches(KeyContextComposer, ActionComposerHistorySearch, key) {
		return nil, m.beginHistorySearch()
	}
	if len(m.pendingPaste) > 0 && m.keys.matches(KeyContextComposer, ActionComposerUndoPaste, key) {
		m.pendingPaste = m.pendingPaste[:len(m.pendingPaste)-1]
		m.layout()
		return nil, true
	}
	if m.keys.matches(KeyContextComposer, ActionComposerHistoryPrevious, key) &&
		(canonicalKey(key) != "up" || m.atHistoryBoundary(-1)) {
		return nil, m.moveHistory(-1)
	}
	if m.keys.matches(KeyContextComposer, ActionComposerHistoryNext, key) &&
		(canonicalKey(key) != "down" || m.atHistoryBoundary(1)) {
		return nil, m.moveHistory(1)
	}
	if m.keys.matches(KeyContextComposer, ActionComposerSubmit, key) {
		return m.submit(), true
	}
	if m.keys.matches(KeyContextComposer, ActionComposerNewline, key) {
		return nil, false
	}

	switch {
	case m.keys.matches(KeyContextPager, ActionPagerPageUp, key):
		m.vp.PageUp()
		m.followTail = false
		return nil, true
	case m.keys.matches(KeyContextPager, ActionPagerPageDown, key):
		m.vp.PageDown()
		if m.vp.AtBottom() {
			m.followTail = true
			m.unseenLines = 0
		}
		return nil, true
	case m.keys.matches(KeyContextPager, ActionPagerTop, key):
		m.vp.GotoTop()
		m.followTail = false
		return nil, true
	case m.keys.matches(KeyContextPager, ActionPagerBottom, key):
		m.vp.GotoBottom()
		m.followTail = true
		m.unseenLines = 0
		return nil, true
	case m.keys.matches(KeyContextPager, ActionPagerToggleDetail, key):
		if m.tr.toggleLastCollapsible(m.th, m.transcriptWidth()) {
			m.vp.SetContentLines(m.tr.lines)
			if m.followTail {
				m.vp.GotoBottom()
			}
		}
		return nil, true
	case m.keys.matches(KeyContextGlobal, ActionGlobalHelp, key):
		m.showHelp()
		return nil, true
	case m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key):
		return tea.ClearScreen, true
	case m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key):
		return m.ctrlC(), true
	case m.keys.matches(KeyContextGlobal, ActionGlobalExit, key):
		return m.ctrlD()
	}
	return nil, false
}

func (m *Model) handleMouseWheel(msg tea.MouseWheelMsg) tea.Cmd {
	delta := 0
	switch msg.Button {
	case tea.MouseWheelUp:
		delta = -3
	case tea.MouseWheelDown:
		delta = 3
	default:
		return nil
	}
	if m.question != nil {
		m.question.Scroll += delta
		m.clampQuestionScroll()
		return nil
	}
	if m.approval != nil {
		m.approval.Scroll += delta
		m.clampApprovalScroll()
		return nil
	}
	var cmd tea.Cmd
	m.vp, cmd = m.vp.Update(msg)
	m.followTail = m.vp.AtBottom()
	if m.followTail {
		m.unseenLines = 0
	}
	return cmd
}

// suggestKey handles keys while the mention/slash suggestion panel is open.
// Selection follows common completion-menu conventions: arrows or ctrl+p/n
// move, tab/enter accepts, and esc dismisses. Printable characters, including
// digits, remain prompt input rather than hidden accelerators.
func (m *Model) suggestKey(key string) (tea.Cmd, bool) {
	switch {
	case m.keys.matches(KeyContextSuggestion, ActionSuggestionDismiss, key):
		m.closeSuggest()
		return nil, true
	case m.keys.matches(KeyContextSuggestion, ActionSuggestionPrevious, key):
		if len(m.suggest.Matches) > 0 {
			m.suggest.Selected = (m.suggest.Selected - 1 + len(m.suggest.Matches)) % len(m.suggest.Matches)
		}
		return nil, true
	case m.keys.matches(KeyContextSuggestion, ActionSuggestionNext, key):
		if len(m.suggest.Matches) > 0 {
			m.suggest.Selected = (m.suggest.Selected + 1) % len(m.suggest.Matches)
		}
		return nil, true
	case m.keys.matches(KeyContextSuggestion, ActionSuggestionAccept, key):
		if len(m.suggest.Matches) > 0 {
			m.applySuggestSelection(m.suggest.Selected)
		}
		return nil, true
	}
	// Navigation/editing keys that plausibly continue shaping the query
	// (backspace, arrows, plain character keys) are left unhandled here so
	// they reach the textarea and the input path's own trigger re-detection
	// (refreshSuggestTrigger) naturally updates or closes the panel.
	return nil, false
}

// ctrlC implements the cascading interrupt (P1.4): first press cancels the
// in-flight task via task.cancel and says so; a second press within 2s
// exits. Never a silent kill, never an accidental quit.
func (m *Model) ctrlC() tea.Cmd {
	now := m.now()
	armed := !m.lastCtrlC.IsZero() && now.Sub(m.lastCtrlC) <= ctrlCWindow
	m.lastCtrlC = now
	if armed {
		m.ctrlCHint = ""
		return tea.Quit
	}
	if m.submitting != nil {
		m.push(m.th.Style(theme.RoleMuted).Render("- submission is being acknowledged; press ctrl+c again within 2s to exit"))
		return nil
	}
	const hintText = "- press ctrl+c again within 2s to exit"
	hint := m.th.Style(theme.RoleMuted).Render(hintText)
	// Recorded plain (not just pushed to the transcript): while the
	// approval overlay is open, View() replaces the whole frame with the
	// overlay (view.go) and the transcript is not rendered at all, so the
	// pushed line alone would be invisible until the overlay closes.
	// overlayView reads this to surface the hint in the overlay itself.
	m.ctrlCHint = hintText
	if m.inFlightTaskID != "" {
		tid := m.inFlightTaskID
		m.push(microcopy.Degrade(microcopy.DegradeInterruptedByUser, nil,
			microcopy.WithLocale(m.locale), microcopy.WithPlain(m.plain())))
		m.push(hint)
		return m.cancelTask(tid)
	}
	if draft := m.currentDraft(); draft.Text != "" || len(draft.Paste) > 0 {
		m.recordHistory(draft)
		m.historyPos = len(m.history)
		m.historyScratch = promptDraft{}
		m.input.Reset()
		m.pendingPaste = nil
		m.closeSuggest()
		m.layout()
		m.push(m.th.Style(theme.RoleMuted).Render("- draft cleared; use prompt history to restore it"))
		m.push(hint)
		return nil
	}
	m.push(hint)
	return nil
}

func (m *Model) ctrlD() (tea.Cmd, bool) {
	if m.approval != nil || m.question != nil || m.helpOpen {
		return nil, true
	}
	if draft := m.currentDraft(); draft.Text != "" || len(draft.Paste) > 0 {
		// Let bubbles retain its standard Ctrl-D delete-forward behavior.
		return nil, false
	}
	if m.inFlightTaskID != "" || m.submitting != nil {
		return nil, true
	}
	return tea.Quit, true
}

func (m *Model) cancelTask(taskID string) tea.Cmd {
	call := m.call
	return func() tea.Msg {
		if call == nil {
			return cancelDoneMsg{taskID: taskID, err: errors.New("daemon not connected")}
		}
		err := call.Call("task.cancel", map[string]any{"task_id": taskID}, nil)
		return cancelDoneMsg{taskID: taskID, err: err}
	}
}

// submit sends a new task while idle. While a task is running, the same input
// surface becomes steering: the operator can redirect the current loop without
// cancelling it or accidentally starting a second concurrent task.
func (m *Model) submit() tea.Cmd {
	draft := m.currentDraft()
	text := strings.TrimSpace(draft.Text)
	paste := draft.Paste
	if text == "" && len(paste) == 0 {
		return nil
	}
	if strings.HasPrefix(text, "/") {
		cmd := m.slashCommand(text)
		if validSlashCommand(text) {
			m.commitDraft(draft, false)
		}
		return cmd
	}
	if strings.HasPrefix(text, "!") {
		return m.submitShell(draft, strings.TrimSpace(strings.TrimPrefix(text, "!")))
	}
	if m.inFlightTaskID != "" {
		return m.beginSubmission(submissionSteer, m.inFlightTaskID, draft)
	}
	return m.beginSubmission(submissionTask, "", draft)
}

func (m *Model) currentDraft() promptDraft {
	return promptDraft{Text: m.input.Value(), Paste: append([]string(nil), m.pendingPaste...)}
}

func draftsEqual(a, b promptDraft) bool {
	if a.Text != b.Text || len(a.Paste) != len(b.Paste) {
		return false
	}
	for i := range a.Paste {
		if a.Paste[i] != b.Paste[i] {
			return false
		}
	}
	return true
}

func draftPrompt(draft promptDraft) string {
	text := strings.TrimSpace(draft.Text)
	paste := strings.Join(draft.Paste, "\n")
	if text == "" {
		return paste
	}
	if paste == "" {
		return text
	}
	return text + "\n" + paste
}

func draftLabel(draft promptDraft) string {
	if text := strings.TrimSpace(draft.Text); text != "" {
		return text
	}
	return "[pasted content]"
}

func (m *Model) beginSubmission(kind submissionKind, target string, draft promptDraft) tea.Cmd {
	if m.submitting != nil {
		return nil
	}
	call := m.call
	if call == nil {
		m.push(fmt.Sprintf("%s not connected: draft kept for retry", glyphFailed(m.th)))
		return nil
	}
	m.submissionGen++
	state := &submissionState{
		generation:   m.submissionGen,
		kind:         kind,
		target:       target,
		draft:        draft,
		consumePaste: kind != submissionShell,
	}
	m.submitting = state
	m.closeSuggest()
	m.layout()
	prompt, sid, generation := draftPrompt(draft), m.sessionID, state.generation
	return func() tea.Msg {
		switch kind {
		case submissionSteer:
			err := call.Call("task.steer", map[string]any{"task_id": target, "message": prompt}, nil)
			return submissionDoneMsg{generation: generation, taskID: target, err: err}
		case submissionShell:
			var out any
			err := call.Call("command.exec", map[string]any{"session_id": sid, "argv": strings.Fields(target)}, &out)
			raw, _ := json.MarshalIndent(out, "", "  ")
			return submissionDoneMsg{generation: generation, result: string(raw), err: err}
		default:
			var out struct {
				TaskID string `json:"task_id"`
			}
			err := call.Call("task.submit", map[string]any{"session_id": sid, "prompt": prompt}, &out)
			if err == nil && out.TaskID == "" {
				err = errors.New("daemon returned an empty task_id")
			}
			return submissionDoneMsg{generation: generation, taskID: out.TaskID, err: err}
		}
	}
}

func (m *Model) submitShell(draft promptDraft, command string) tea.Cmd {
	if command == "" {
		m.push("usage: !<command>")
		return nil
	}
	return m.beginSubmission(submissionShell, command, draft)
}

func (m *Model) handleSubmissionDone(msg submissionDoneMsg) {
	state := m.submitting
	if state == nil || state.generation != msg.generation {
		return
	}
	m.submitting = nil
	if msg.err != nil {
		m.push(fmt.Sprintf("%s %s failed: %s; draft kept for retry", glyphFailed(m.th), state.kind, msg.err.Error()))
		m.layout()
		return
	}
	m.commitDraft(state.draft, state.consumePaste)
	shown := draftLabel(state.draft)
	switch state.kind {
	case submissionTask:
		m.push(m.th.Style(theme.RoleTitle).Render("you ") + shown)
		m.inFlightTaskID = msg.taskID
		m.tasks.setTask(msg.taskID, "running")
		m.push(m.th.Style(theme.RoleMuted).Render("- task " + msg.taskID + " submitted"))
	case submissionSteer:
		m.push(m.th.Style(theme.RoleTitle).Render("you (steer) ") + shown)
		m.push(m.th.Style(theme.RoleMuted).Render("- steering queued for task " + msg.taskID))
	case submissionShell:
		m.push(m.th.Style(theme.RoleTitle).Render("you (shell) ") + state.target)
		m.push(m.th.Style(theme.RoleTitle).Render("shell") + "\n" + msg.result)
	}
	m.layout()
}

func (m *Model) commitDraft(draft promptDraft, consumePaste bool) {
	current := m.currentDraft()
	if current.Text == draft.Text && (!consumePaste || draftsEqual(current, draft)) {
		m.input.Reset()
		if consumePaste {
			m.pendingPaste = nil
		}
	}
	consumed := promptDraft{Text: draft.Text}
	if consumePaste {
		consumed.Paste = draft.Paste
	}
	m.recordHistory(consumed)
	m.historyPos = len(m.history)
	m.historyScratch = promptDraft{}
	m.layout()
}

func (m *Model) recordHistory(draft promptDraft) {
	draft.Paste = append([]string(nil), draft.Paste...)
	m.history = mergePromptHistory(nil, append(m.history, draft))
}

func (m *Model) atHistoryBoundary(direction int) bool {
	info := m.input.LineInfo()
	if direction < 0 {
		return m.input.Line() == 0 && info.RowOffset == 0
	}
	return m.input.Line() == m.input.LineCount()-1 && info.RowOffset+1 >= info.Height
}

func (m *Model) moveHistory(direction int) bool {
	if len(m.history) == 0 {
		return false
	}
	if m.historyPos < 0 || m.historyPos > len(m.history) {
		m.historyPos = len(m.history)
	}
	if direction < 0 {
		if m.historyPos == len(m.history) {
			m.historyScratch = m.currentDraft()
		}
		if m.historyPos == 0 {
			return true
		}
		m.historyPos--
		m.restoreDraft(m.history[m.historyPos])
		return true
	}
	if m.historyPos >= len(m.history) {
		return true
	}
	m.historyPos++
	if m.historyPos == len(m.history) {
		m.restoreDraft(m.historyScratch)
	} else {
		m.restoreDraft(m.history[m.historyPos])
	}
	return true
}

func (m *Model) restoreDraft(draft promptDraft) {
	m.input.SetValue(draft.Text)
	m.pendingPaste = append([]string(nil), draft.Paste...)
	m.closeSuggest()
	m.layout()
}

func (m *Model) slashCommand(text string) tea.Cmd {
	parts := strings.Fields(text)
	name := strings.TrimPrefix(parts[0], "/")
	switch name {
	case "help", "keys":
		m.showHelp()
		return nil
	case "search":
		if len(parts) < 2 {
			m.push("usage: /search <text>")
			return nil
		}
		q := strings.ToLower(strings.Join(parts[1:], " "))
		hits := 0
		for _, line := range m.tr.lines {
			if strings.Contains(strings.ToLower(line), q) {
				m.push("- " + line)
				hits++
			}
		}
		m.push(fmt.Sprintf("- transcript search: %d match(es)", hits))
		return nil
	case "recap":
		start := len(m.tr.lines) - 12
		if start < 0 {
			start = 0
		}
		m.push(m.th.Style(theme.RoleTitle).Render("recap") + "\n" + strings.Join(m.tr.lines[start:], "\n"))
		return nil
	case "mode":
		if len(parts) != 2 || (parts[1] != "build" && parts[1] != "plan") {
			m.push("usage: /mode <build|plan>")
			return nil
		}
		m.mode = parts[1]
		return m.querySurface("session.plan_mode", map[string]any{"session_id": m.sessionID, "on": m.mode == "plan"}, "mode "+m.mode)
	case "agents":
		return m.querySurface("agent.list", map[string]any{"session_id": m.sessionID}, "agents")
	case "checkpoints":
		return m.querySurface("session.checkpoint.list", map[string]any{"session_id": m.sessionID}, "checkpoints")
	default:
		m.push("unknown command /" + name + "; use /help")
		return nil
	}
}

func validSlashCommand(text string) bool {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false
	}
	switch strings.TrimPrefix(parts[0], "/") {
	case "help", "keys", "recap", "agents", "checkpoints":
		return true
	case "search":
		return len(parts) >= 2
	case "mode":
		return len(parts) == 2 && (parts[1] == "build" || parts[1] == "plan")
	default:
		return false
	}
}

func (m *Model) querySurface(method string, params map[string]any, label string) tea.Cmd {
	call := m.call
	return func() tea.Msg {
		if call == nil {
			return rpcErrMsg{err: errors.New("daemon not connected")}
		}
		var out any
		if err := call.Call(method, params, &out); err != nil {
			return rpcErrMsg{err: err}
		}
		raw, _ := json.MarshalIndent(out, "", "  ")
		return surfaceResultMsg{label: label, text: string(raw)}
	}
}

// handlePaste normalizes bracketed-paste content (terminals paste \r line
// endings — spike sharp edge). Multi-line payloads stay out of the textarea
// but are rendered beside it as visible, independently undoable draft items.
func (m *Model) handlePaste(msg tea.PasteMsg) tea.Cmd {
	content := strings.ReplaceAll(strings.ReplaceAll(msg.Content, "\r\n", "\n"), "\r", "\n")
	if n := strings.Count(content, "\n") + 1; n > 1 {
		m.pendingPaste = append(m.pendingPaste, content)
		m.layout()
		return nil
	}
	m.input.InsertString(content)
	// A paste can atomically change the whole trigger/query. Hide the old
	// selection immediately; refreshSuggestTrigger will either schedule the
	// new query or leave the panel closed.
	if m.suggest != nil {
		m.closeSuggest()
	}
	m.layout()
	return m.refreshSuggestTrigger()
}
