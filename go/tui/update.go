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
		if kp.String() != "ctrl+c" {
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
		return m, nil

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

	case taskSubmittedMsg:
		m.inFlightTaskID = msg.taskID
		m.tasks.setTask(msg.taskID, "running")
		m.push(m.th.Style(theme.RoleMuted).Render("- task " + msg.taskID + " submitted"))
		m.layout()
		return m, nil

	case taskSteeredMsg:
		m.push(m.th.Style(theme.RoleMuted).Render("- steering queued for task " + msg.taskID))
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
		return m, m.handlePaste(msg)

	case tea.KeyPressMsg:
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
	if key == "ctrl+c" {
		return m.ctrlC(), true
	}
	if cmd, handled := m.questionKey(key); handled {
		return cmd, true
	}
	if m.approval != nil {
		switch key {
		case "y", "1":
			return m.resolveApproval("once", true), true
		case "2":
			return m.resolveApproval("session", true), true
		case "3":
			return m.resolveApproval("project", true), true
		case "n", "4":
			return m.resolveApproval("deny", false), true
		case "esc":
			id := m.approval.DecisionID
			m.nextQueuedApproval()
			m.push(m.th.Style(theme.RoleMuted).Render(
				"- approval prompt dismissed; decision " + id + " is still pending server-side"))
			return nil, true
		}
		return nil, true // the overlay owns the keyboard while open
	}
	// The mention/slash suggestion panel is a transient, non-modal aid: it
	// never takes the keyboard away from the approval/question overlays
	// (those are already handled and returned above, so reaching here means
	// neither is open) and only intercepts a small set of keys itself.
	if m.suggest != nil {
		if cmd, handled := m.suggestKey(key); handled {
			return cmd, true
		}
	}
	switch key {
	case "pgup":
		m.vp.PageUp()
		m.followTail = false
		return nil, true
	case "pgdown":
		m.vp.PageDown()
		if m.vp.AtBottom() {
			m.followTail = true
			m.unseenLines = 0
		}
		return nil, true
	case "alt+home":
		m.vp.GotoTop()
		m.followTail = false
		return nil, true
	case "alt+end":
		m.vp.GotoBottom()
		m.followTail = true
		m.unseenLines = 0
		return nil, true
	case "ctrl+o":
		if m.tr.toggleLastCollapsible(m.th, m.transcriptWidth()) {
			m.vp.SetContentLines(m.tr.lines)
			if m.followTail {
				m.vp.GotoBottom()
			}
		}
		return nil, true
	case "?":
		m.showHelp()
		return nil, true
	}
	if key == "enter" {
		return m.submit(), true
	}
	return nil, false
}

// suggestKey handles keys while the mention/slash suggestion panel is open.
// Selection uses number keys 1-9 (mirroring the existing approval-overlay
// numeric-selection convention: y/1/2/3/n/4 in the block above), matching
// the panel's own displayed numbering. esc closes the panel without
// modifying the input, mirroring the approval esc-dismiss convention. Any
// other key besides the trigger's own editing keys is treated as "the
// operator moved on" and closes the panel without swallowing the
// keystroke — it still falls through to the textarea.
func (m *Model) suggestKey(key string) (tea.Cmd, bool) {
	switch key {
	case "esc":
		m.closeSuggest()
		return nil, true
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(key[0] - '1')
		if idx < len(m.suggest.Matches) {
			m.applySuggestSelection(idx)
			return nil, true
		}
		// Out of range for the current match count: not a selection: let it
		// fall through as ordinary typed input instead of silently eating a
		// digit the operator meant to type.
		return nil, false
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
	m.push(hint)
	return nil
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
	text := strings.TrimSpace(m.input.Value())
	paste := m.pendingPaste
	if text == "" && len(paste) == 0 {
		return nil
	}
	m.input.Reset()
	m.pendingPaste = nil
	m.layout()
	if strings.HasPrefix(text, "/") {
		return m.slashCommand(text)
	}
	if strings.HasPrefix(text, "!") {
		return m.shellCommand(strings.TrimSpace(strings.TrimPrefix(text, "!")))
	}
	prompt := text
	if len(paste) > 0 {
		prompt = strings.TrimSpace(text + "\n" + strings.Join(paste, "\n"))
	}
	shown := text
	if shown == "" {
		shown = "[pasted content]"
	}
	if m.inFlightTaskID != "" {
		taskID := m.inFlightTaskID
		m.push(m.th.Style(theme.RoleTitle).Render("you (steer) ") + shown)
		return m.steerTask(taskID, prompt)
	}
	m.push(m.th.Style(theme.RoleTitle).Render("you ") + shown)
	call, sid := m.call, m.sessionID
	if call == nil {
		m.push(fmt.Sprintf("%s not connected: the instruction was not sent", glyphFailed(m.th)))
		return nil
	}
	return func() tea.Msg {
		var out struct {
			TaskID string `json:"task_id"`
		}
		if err := call.Call("task.submit", map[string]any{
			"session_id": sid,
			"prompt":     prompt,
		}, &out); err != nil {
			return rpcErrMsg{err: err}
		}
		return taskSubmittedMsg{taskID: out.TaskID}
	}
}

func (m *Model) showHelp() {
	m.push(m.th.Style(theme.RoleTitle).Render("commands") + "\n" +
		"  /help                 commands and keybindings\n" +
		"  /agents               available agent modes\n" +
		"  /checkpoints          rewind points for this session\n" +
		"  /search <text>         search visible transcript\n" +
		"  /recap                 compact current-session recap\n" +
		"  /mode <build|plan>     show or change interaction mode\n" +
		"  !<command>             governed shell command\n" +
		"  @<path|agent>          reference a workspace path or agent - suggestions appear as you type\n" +
		"  /<command>             at the start of the line - suggestions appear as you type\n" +
		"  1-9 to pick a suggestion · esc to dismiss the panel\n" +
		"  pgup/pgdown scroll · alt+home/end jump · ctrl+o expand · ctrl+c cancel")
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
func (m *Model) shellCommand(command string) tea.Cmd {
	if command == "" {
		m.push("usage: !<command>")
		return nil
	}
	argv := strings.Fields(command)
	call := m.call
	sid := m.sessionID
	m.push(m.th.Style(theme.RoleTitle).Render("you (shell) ") + command)
	return func() tea.Msg {
		if call == nil {
			return rpcErrMsg{err: errors.New("daemon not connected")}
		}
		var out any
		if err := call.Call("command.exec", map[string]any{"session_id": sid, "argv": argv}, &out); err != nil {
			return rpcErrMsg{err: err}
		}
		raw, _ := json.MarshalIndent(out, "", "  ")
		return surfaceResultMsg{label: "shell", text: string(raw)}
	}
}

func (m *Model) steerTask(taskID, prompt string) tea.Cmd {
	call := m.call
	return func() tea.Msg {
		if call == nil {
			return rpcErrMsg{err: errors.New("daemon not connected")}
		}
		if err := call.Call("task.steer", map[string]any{
			"task_id": taskID,
			"message": prompt,
		}, nil); err != nil {
			return rpcErrMsg{err: err}
		}
		return taskSteeredMsg{taskID: taskID}
	}
}

// handlePaste normalizes bracketed-paste content (terminals paste \r line
// endings — spike sharp edge) and collapses multi-line pastes to a visible
// notice; the content is held and folded into the next submission.
func (m *Model) handlePaste(msg tea.PasteMsg) tea.Cmd {
	content := strings.ReplaceAll(strings.ReplaceAll(msg.Content, "\r\n", "\n"), "\r", "\n")
	if n := strings.Count(content, "\n") + 1; n > 1 {
		m.pendingPaste = append(m.pendingPaste, content)
		m.push(m.th.Style(theme.RoleMuted).Render(fmt.Sprintf("[Pasted %d lines]", n)))
		return nil
	}
	m.input.InsertString(content)
	m.layout()
	return nil
}
