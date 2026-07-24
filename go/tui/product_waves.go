package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

// product_waves.go groups the remaining TUI product workflows and their Carina
// governance trade-offs:
//
// Wave A: plan approve + plan file, honest btw/commit, sandbox explain
// Wave B: settings actions that mutate (approve plan, effort shortcuts)
// Wave C: context refresh tick, richer tasks, inspect/welcome readiness
// Wave D: documented in docs/plans/tui-product-ux-closure.md

const runtimeStatusTickInterval = 45 * time.Second

type runtimeStatusTickMsg struct {
	generation uint64
}

func (m *Model) scheduleRuntimeStatusTick() tea.Cmd {
	if m.sessionID == "" || m.call == nil {
		return nil
	}
	// Unit tests drain BatchMsg synchronously; a 45s tea.Tick would hang drain.
	if testing.Testing() {
		return nil
	}
	gen := m.sessionGeneration
	return tea.Tick(runtimeStatusTickInterval, func(time.Time) tea.Msg {
		return runtimeStatusTickMsg{generation: gen}
	})
}

func (m *Model) planFilePath() string {
	root := m.workspaceRoot
	if root == "" {
		root, _ = os.Getwd()
	}
	id := m.sessionID
	if id == "" {
		id = "session"
	}
	// Keep path filesystem-safe without inventing a second session store.
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
	return filepath.Join(root, ".carina", "plans", safe+".md")
}

func (m *Model) ensurePlanFileScaffold() error {
	path := m.planFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	body := "# Plan\n\n" +
		"## Goal\n\n- \n\n" +
		"## Approach\n\n- \n\n" +
		"## Steps\n\n1. \n\n" +
		"## Risks\n\n- \n\n" +
		"## Done when\n\n- \n"
	return os.WriteFile(path, []byte(body), 0o644)
}

func (m *Model) readPlanFile() (string, error) {
	path := m.planFilePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (m *Model) approvePlan() tea.Cmd {
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return modeChangedMsg{sessionID: sessionID, mode: "build", err: errorsNew("daemon not connected")}
		}
		var out map[string]any
		err := call.Call("session.approve_plan", map[string]any{"session_id": sessionID}, &out)
		// Approve exits plan mode (daemon contract).
		return modeChangedMsg{sessionID: sessionID, mode: "build", err: err}
	}
}

// viewPlanSurface opens the plan review overlay with approve, save, and cancel actions.
// Falls back to a short transcript notice when another overlay already owns input.
func (m *Model) viewPlanSurface() {
	if m.approval != nil || m.question != nil || m.helpOpen || m.settings != nil {
		m.push(m.text(MsgPlanReviewBusyBlocked, nil))
		return
	}
	m.openPlanReview()
}

func (m *Model) enterPlanMode(followUp string) tea.Cmd {
	_ = m.ensurePlanFileScaffold()
	call, sessionID := m.call, m.sessionID
	path := m.planFilePath()
	return func() tea.Msg {
		if call == nil {
			return modeChangedMsg{sessionID: sessionID, mode: "plan", err: errorsNew("daemon not connected"), followUpPrompt: followUp}
		}
		err := call.Call("session.plan_mode", map[string]any{"session_id": sessionID, "on": true}, nil)
		// Enrich the follow-up so the agent writes the session plan file.
		if strings.TrimSpace(followUp) != "" {
			followUp = followUp + "\n\nWrite the working plan to this file (create/update markdown sections Goal/Approach/Steps/Risks/Done when):\n" + path +
				"\nDo not edit other files until the operator runs /approve-plan."
		}
		return modeChangedMsg{sessionID: sessionID, mode: "plan", err: err, followUpPrompt: followUp}
	}
}

// btwSideQuestion runs a side Q&A turn.
//
// Default (no flag): answer-only prompt on the current session (honest, no fork).
// With fork=true (/btw --fork or /side): session.fork then submit on the new
// session after switch when a completed
// checkpoint exists. Fork requires an idle completed task (daemon contract).
func (m *Model) btwSideQuestion(question string, fork bool) tea.Cmd {
	question = strings.TrimSpace(question)
	if question == "" {
		m.push(m.text(MsgUpdateUsageBtw, nil))
		return nil
	}
	if fork {
		if m.inFlightTaskID != "" || m.submitting != nil {
			m.push(m.text(MsgUpdateBtwForkBusy, nil))
			return nil
		}
		m.pendingSideQuestion = question
		m.armSidePane(question)
		m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgUpdateBtwForkStart, nil)))
		return m.forkSession("")
	}
	prompt := sideQuestionPrompt(question, false)
	m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgUpdateBtwStarted, nil)))
	return m.beginSubmissionSourceWithIntent(submissionTask, "", promptDraft{Text: prompt}, false, false)
}

func sideQuestionPrompt(question string, forked bool) string {
	header := "SIDE QUESTION (answer-only turn; not a side-session fork)."
	if forked {
		header = "SIDE QUESTION on a forked session lineage."
	}
	return strings.Join([]string{
		header,
		"Constraints:",
		"- Answer the operator's question briefly and directly.",
		"- Do not modify files, run shell commands, change git state, or alter the main plan.",
		"- Do not claim the main task is complete.",
		"- If you need code context, use read-only inspection only.",
		"",
		"Question:",
		question,
	}, "\n")
}

func (m *Model) flushPendingSideQuestion() tea.Cmd {
	q := strings.TrimSpace(m.pendingSideQuestion)
	if q == "" {
		return nil
	}
	if !m.newTaskReady() {
		return nil
	}
	m.pendingSideQuestion = ""
	m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgUpdateBtwForkReady, nil)))
	return m.beginSubmissionSourceWithIntent(submissionTask, "", promptDraft{Text: sideQuestionPrompt(q, true)}, false, false)
}

// commitWorkflow injects workspace.diff, then
// constrain the agent to a governed commit path.
func (m *Model) commitWorkflow(extra string) tea.Cmd {
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return operationalSurfaceMsg{sessionID: sessionID, kind: "diff", err: errorsNew("daemon not connected")}
		}
		var diff map[string]any
		diffErr := call.Call("workspace.diff", map[string]any{"session_id": sessionID}, &diff)
		var b strings.Builder
		b.WriteString("Git commit workflow (PromptCommand pattern; tools must stay within git status/diff/add/commit).\n")
		b.WriteString("Rules:\n")
		b.WriteString("- Inspect the working tree; draft a concise commit message.\n")
		b.WriteString("- Stage only relevant files; never commit secrets (.env, credentials, private keys).\n")
		b.WriteString("- Create one commit; do not push unless the operator explicitly asked.\n")
		b.WriteString("- If the tree is clean, say so and stop.\n\n")
		if extra = strings.TrimSpace(extra); extra != "" {
			b.WriteString("Operator notes:\n")
			b.WriteString(extra)
			b.WriteString("\n\n")
		}
		if diffErr != nil {
			b.WriteString("workspace.diff unavailable: ")
			b.WriteString(diffErr.Error())
			b.WriteString("\nRun git status/diff yourself with governed tools.\n")
		} else {
			b.WriteString("workspace.diff snapshot:\n")
			b.WriteString(formatDiffSnapshot(diff, 80))
		}
		return commitPromptReadyMsg{sessionID: sessionID, prompt: b.String()}
	}
}

type commitPromptReadyMsg struct {
	sessionID string
	prompt    string
}

func formatDiffSnapshot(diff map[string]any, maxFiles int) string {
	files, _ := diff["files"].([]any)
	if len(files) == 0 {
		return "(clean or no files reported)\n"
	}
	var b strings.Builder
	for i, raw := range files {
		if i >= maxFiles {
			fmt.Fprintf(&b, "… +%d more files\n", len(files)-i)
			break
		}
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%s %s\n", str(row["status"]), str(row["path"]))
		if d := str(row["diff"]); d != "" {
			lines := strings.Split(d, "\n")
			if len(lines) > 30 {
				lines = append(lines[:30], "…")
			}
			b.WriteString(strings.Join(lines, "\n"))
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m *Model) explainRuntimeSurface() {
	lines := []string{m.th.Style(theme.RoleTitle).Render(m.text(MsgExplainTitle, nil))}
	lines = append(lines,
		m.text(MsgExplainMode, MessageArgs{"mode": m.modeLabel()}),
		m.text(MsgExplainProfile, MessageArgs{"profile": stringOr(m.runtime.Profile, "unknown")}),
		m.text(MsgExplainSandbox, MessageArgs{"sandbox": stringOr(m.runtime.Sandbox, "unknown")}),
		m.text(MsgExplainApproval, MessageArgs{"approval": "product HITL=" + m.approvalModeLabel()}),
		"",
		m.text(MsgExplainSandboxWhy, nil),
		m.text(MsgExplainHowToChange, nil),
		m.text(MsgExplainAlwaysApprove, nil),
		m.text(MsgExplainApprovalModes, nil),
	)
	m.push(strings.Join(lines, "\n"))
}

func (m *Model) inspectSurface() tea.Cmd {
	call, sessionID := m.call, m.sessionID
	generation := m.operationalGeneration("inspect")
	return func() tea.Msg {
		if call == nil {
			return operationalSurfaceMsg{sessionID: sessionID, kind: "inspect", generation: generation, err: errorsNew("daemon not connected")}
		}
		out := map[string]any{
			"session_id": sessionID,
			"mode":       "inspect",
		}
		var doctor map[string]any
		if err := call.Call("daemon.doctor", map[string]any{}, &doctor); err != nil {
			out["doctor_error"] = err.Error()
		} else {
			out["doctor"] = doctor
		}
		var cfg map[string]any
		if err := call.Call("config.inventory", map[string]any{"session_id": sessionID}, &cfg); err == nil {
			out["config"] = cfg["effective"]
		}
		var skills map[string]any
		if err := call.Call("skill.inventory", map[string]any{"session_id": sessionID}, &skills); err == nil {
			out["skills_count"] = skills["count"]
		}
		var hooks map[string]any
		if err := call.Call("hook.inventory", map[string]any{"session_id": sessionID}, &hooks); err == nil {
			out["hooks_count"] = hooks["count"]
		}
		var mcp map[string]any
		if err := call.Call("mcp.inventory", map[string]any{}, &mcp); err == nil {
			out["mcp_count"] = mcp["count"]
		}
		return operationalSurfaceMsg{sessionID: sessionID, kind: "inspect", generation: generation, data: out, err: nil}
	}
}

func (m *Model) humanizeInspect(data map[string]any) []string {
	lines := []string{m.text(MsgInspectHeader, nil)}
	lines = append(lines, fmt.Sprintf("session: %v", data["session_id"]))
	if v, ok := data["doctor"]; ok {
		lines = append(lines, "doctor: ok")
		if dm, ok := v.(map[string]any); ok {
			// Keep compact — doctor can be large.
			for _, key := range []string{"status", "healthy", "ok", "version"} {
				if x, ok := dm[key]; ok {
					lines = append(lines, fmt.Sprintf("  %s: %v", key, x))
				}
			}
		}
	}
	if e, ok := data["doctor_error"]; ok {
		lines = append(lines, fmt.Sprintf("doctor: %v", e))
	}
	for _, key := range []string{"skills_count", "hooks_count", "mcp_count"} {
		if v, ok := data[key]; ok {
			lines = append(lines, fmt.Sprintf("%s: %v", key, v))
		}
	}
	if cfg, ok := data["config"].(map[string]any); ok {
		lines = append(lines, "runtime:")
		for _, key := range []string{"permission_profile", "plan_mode", "sandbox_commands", "approval_mode", "interactive_approval", "disable_always_approve", "model", "reasoning_effort"} {
			if v, ok := cfg[key]; ok {
				lines = append(lines, fmt.Sprintf("  %s: %v", key, v))
			}
		}
	}
	lines = append(lines, "", m.text(MsgInspectHint, nil))
	return lines
}

// showTasksSurface lists active tasks, queue, and schedules.
func (m *Model) showTasksSurface() {
	lines := []string{m.th.Style(theme.RoleTitle).Render(m.text(MsgTasksTitle, nil))}
	if tree := m.taskTreeLines(); len(tree) > 0 {
		lines = append(lines, tree...)
	} else {
		lines = append(lines, m.text(MsgTasksEmpty, nil))
	}
	if n := m.followUps.len(); n > 0 {
		lines = append(lines, "", m.countText(MsgStatusQueued, n, nil))
		for i, item := range m.followUps.drafts {
			if i >= 8 {
				break
			}
			lines = append(lines, fmt.Sprintf("  %d. %s", i+1, summarizeDraft(item)))
		}
	}
	lines = append(lines, "", m.text(MsgTasksLoopHint, nil))
	m.push(strings.Join(lines, "\n"))
	// Best-effort schedule list append.
}

func (m *Model) showTasksSurfaceAsync() tea.Cmd {
	m.showTasksSurface()
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return nil
		}
		var out map[string]any
		if err := call.Call("schedule.list", map[string]any{"session_id": sessionID}, &out); err != nil {
			return nil
		}
		return tasksScheduleMsg{sessionID: sessionID, data: out}
	}
}

type tasksScheduleMsg struct {
	sessionID string
	data      map[string]any
}

func (m *Model) handleTasksSchedule(msg tasksScheduleMsg) {
	if msg.sessionID != "" && msg.sessionID != m.sessionID {
		return
	}
	rows, _ := msg.data["schedules"].([]any)
	if rows == nil {
		// Some handlers return a bare array.
		if arr, ok := any(msg.data).([]any); ok {
			rows = arr
		}
	}
	// Also accept top-level list under common keys.
	for _, key := range []string{"items", "schedules", "loops"} {
		if rows != nil {
			break
		}
		if arr, ok := msg.data[key].([]any); ok {
			rows = arr
		}
	}
	if len(rows) == 0 {
		return
	}
	lines := []string{m.text(MsgTasksLoopsHeader, nil)}
	for i, raw := range rows {
		if i >= 12 {
			lines = append(lines, fmt.Sprintf("… +%d more", len(rows)-i))
			break
		}
		if row, ok := raw.(map[string]any); ok {
			lines = append(lines, fmt.Sprintf("  - %s · %s · %s",
				str(row["schedule_id"]), str(row["state"]), str(row["prompt"])))
		}
	}
	m.push(strings.Join(lines, "\n"))
}

// setApprovalMode sets product HITL mode: ask | always-approve | dont-ask | accept-edits.
func (m *Model) setApprovalMode(mode string) tea.Cmd {
	call, sessionID := m.call, m.sessionID
	mode = strings.TrimSpace(mode)
	return func() tea.Msg {
		if call == nil {
			return operationalSurfaceMsg{sessionID: sessionID, kind: "approval-mode", err: errorsNew("daemon not connected")}
		}
		var out map[string]any
		err := call.Call("daemon.set_interactive_approval", map[string]any{
			"mode": mode, "session_id": sessionID,
		}, &out)
		return approvalModeMsg{sessionID: sessionID, wantMode: mode, data: out, err: err}
	}
}

// setAlwaysApprove maps product always-approve on/off onto the three-way mode.
// ON => always-approve; OFF => ask. Deny rules, plan mode, and OS sandbox still apply.
func (m *Model) setAlwaysApprove(on bool) tea.Cmd {
	if on {
		return m.setApprovalMode("always-approve")
	}
	return m.setApprovalMode("ask")
}

type approvalModeMsg struct {
	sessionID string
	wantMode  string
	data      map[string]any
	err       error
}

// alwaysApproveMsg is kept as an alias shape for older call sites / tests.
type alwaysApproveMsg = approvalModeMsg

func (m *Model) handleAlwaysApprove(msg approvalModeMsg) {
	m.handleApprovalMode(msg)
}

func (m *Model) handleApprovalMode(msg approvalModeMsg) {
	if msg.sessionID != "" && msg.sessionID != m.sessionID {
		return
	}
	if msg.err != nil {
		m.push(m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.err.Error()}))
		return
	}
	mode := str(msg.data["approval_mode"])
	if mode == "" {
		mode = msg.wantMode
	}
	m.applyApprovalModeToRuntime(mode)
	switch mode {
	case "always-approve":
		m.push(m.th.Style(theme.RoleWarning).Render(m.text(MsgAlwaysApproveEnabled, nil)))
		if w := str(msg.data["warning"]); w != "" {
			m.push(m.th.Style(theme.RoleMuted).Render(w))
		} else {
			m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgAlwaysApproveWarning, nil)))
		}
	case "dont-ask":
		m.push(m.th.Style(theme.RoleWarning).Render(m.text(MsgDontAskEnabled, nil)))
		if w := str(msg.data["warning"]); w != "" {
			m.push(m.th.Style(theme.RoleMuted).Render(w))
		} else {
			m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgDontAskWarning, nil)))
		}
	case "accept-edits":
		m.push(m.th.Style(theme.RoleWarning).Render(m.text(MsgAcceptEditsEnabled, nil)))
		if w := str(msg.data["warning"]); w != "" {
			m.push(m.th.Style(theme.RoleMuted).Render(w))
		} else {
			m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgAcceptEditsWarning, nil)))
		}
	default:
		m.push(m.text(MsgApprovalModeAsk, nil))
	}
	m.layout()
}

func (m *Model) applyApprovalModeToRuntime(mode string) {
	switch mode {
	case "always-approve":
		m.runtime.InteractiveApprove = "off"
		m.runtime.ApprovalMode = "always-approve"
	case "dont-ask":
		m.runtime.InteractiveApprove = "dont-ask"
		m.runtime.ApprovalMode = "dont-ask"
	case "accept-edits":
		m.runtime.InteractiveApprove = "accept-edits"
		m.runtime.ApprovalMode = "accept-edits"
	default:
		m.runtime.InteractiveApprove = "on"
		m.runtime.ApprovalMode = "ask"
	}
}

func (m *Model) toggleAlwaysApprove() tea.Cmd {
	// always-approve → ask; anything else → always-approve.
	if m.approvalModeLabel() == "always-approve" {
		return m.setAlwaysApprove(false)
	}
	return m.setAlwaysApprove(true)
}

func (m *Model) approvalModeLabel() string {
	if m.runtime.ApprovalMode != "" {
		return m.runtime.ApprovalMode
	}
	switch m.runtime.InteractiveApprove {
	case "off":
		return "always-approve"
	case "dont-ask":
		return "dont-ask"
	case "accept-edits":
		return "accept-edits"
	case "on":
		return "ask"
	default:
		return "ask?"
	}
}

func (m *Model) extensionToggle(name string, enable bool) tea.Cmd {
	name = strings.TrimSpace(name)
	if name == "" {
		m.push(m.text(MsgUpdateUsageExtension, nil))
		return nil
	}
	method := "extension.disable"
	if enable {
		method = "extension.enable"
	}
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return operationalSurfaceMsg{sessionID: sessionID, kind: "extensions", err: errorsNew("daemon not connected")}
		}
		var out map[string]any
		err := call.Call(method, map[string]any{"name": name}, &out)
		if err != nil {
			return operationalSurfaceMsg{sessionID: sessionID, kind: "extensions", err: err}
		}
		return operationalSurfaceMsg{sessionID: sessionID, kind: "extensions", data: map[string]any{
			"action": method, "name": name, "result": out,
			"note": "admin-scope extension mutation; requires sufficient client scope",
		}, err: nil}
	}
}
