package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

// runtimeStatus is the product chrome snapshot shown in the footer and settings shell.
// Fields are best-effort: exact when daemon reports them, empty when unknown.
type runtimeStatus struct {
	Profile            string
	Sandbox            string // on|off|unknown
	InteractiveApprove string // on|off|dont-ask|unknown (legacy mirror of ApprovalMode)
	ApprovalMode       string // ask|always-approve|dont-ask
	ContextUsed        int
	ContextLimit       int
	ContextPercent     int
	ContextSource      string
	ContextAvailable   bool
	CompactAvailable   bool
	CompactReason      string
	CompactCheckpoint  string
	CompactTaskID      string
	LastRefresh        time.Time
	DefaultModel       string
	ReasonerBackend    string
	ReasonerModel      string
	ReadinessReason    string
	ModelInventory     modelListResponse
	HasModelInventory  bool
}

type settingsTab int

const (
	settingsTabOverview settingsTab = iota
	settingsTabMode
	settingsTabModel
	settingsTabExtensions
)

type settingsShellState struct {
	tab    settingsTab
	cursor int
	scroll int
}

type settingsAction struct {
	Label string
	Hint  string
	Run   func(*Model) tea.Cmd
	Route string // optional slash routed through slashCommand
}

func (m *Model) openSettings(tab settingsTab) {
	m.closeSuggest()
	m.helpOpen = false
	m.settings = &settingsShellState{tab: tab}
	m.layout()
}

func (m *Model) closeSettings() {
	m.settings = nil
	m.layout()
}

func (m *Model) settingsTabs() []string {
	return []string{
		m.text(MsgSettingsTabOverview, nil),
		m.text(MsgSettingsTabMode, nil),
		m.text(MsgSettingsTabModel, nil),
		m.text(MsgSettingsTabExtensions, nil),
	}
}

func (m *Model) settingsActions() []settingsAction {
	switch {
	case m.settings != nil && m.settings.tab == settingsTabMode:
		return []settingsAction{
			{Label: m.text(MsgSettingsActionPlan, nil), Hint: "/plan", Route: "/plan"},
			{Label: m.text(MsgSettingsActionBuild, nil), Hint: "/mode build", Route: "/mode build"},
			{Label: m.text(MsgSettingsActionApprovePlan, nil), Hint: "/approve-plan", Run: func(m *Model) tea.Cmd { return m.approvePlan() }},
			{Label: m.text(MsgSettingsActionViewPlan, nil), Hint: "/view-plan", Route: "/view-plan"},
			{Label: m.text(MsgSettingsActionExplain, nil), Hint: "/explain", Route: "/explain"},
			{Label: m.text(MsgSettingsActionAlwaysApprove, nil), Hint: "/always-approve", Run: func(m *Model) tea.Cmd { return m.toggleAlwaysApprove() }},
			{Label: m.text(MsgSettingsActionPermissions, nil), Hint: "/permissions", Route: "/permissions"},
			{Label: m.text(MsgSettingsActionSafeEdit, nil), Hint: "/permissions new safe-edit", Route: "/permissions new safe-edit"},
			{Label: m.text(MsgSettingsActionFullWorkspace, nil), Hint: "/permissions new full-workspace --yes", Route: "/permissions new full-workspace --yes"},
		}
	case m.settings != nil && m.settings.tab == settingsTabModel:
		return []settingsAction{
			{Label: m.text(MsgSettingsActionModelPicker, nil), Hint: "/model", Route: "/model"},
			{Label: m.text(MsgSettingsActionRefresh, nil), Hint: "reload status", Run: func(m *Model) tea.Cmd { return m.refreshRuntimeStatus() }},
			{Label: m.text(MsgSettingsActionDoctor, nil), Hint: "/doctor", Route: "/doctor"},
			{Label: m.text(MsgSettingsActionEffort, nil), Hint: "/effort", Route: "/effort"},
			{Label: m.text(MsgSettingsActionKeymap, nil), Hint: "/keymap", Route: "/keymap"},
		}
	case m.settings != nil && m.settings.tab == settingsTabExtensions:
		return []settingsAction{
			{Label: m.text(MsgSettingsActionSkills, nil), Hint: "/skills", Route: "/skills"},
			{Label: m.text(MsgSettingsActionHooks, nil), Hint: "/hooks", Route: "/hooks"},
			{Label: m.text(MsgSettingsActionMCP, nil), Hint: "/mcp", Route: "/mcp"},
			{Label: m.text(MsgSettingsActionExtensions, nil), Hint: "/extensions", Route: "/extensions"},
			{Label: m.text(MsgSettingsActionDoctor, nil), Hint: "/doctor", Route: "/doctor"},
		}
	default:
		return []settingsAction{
			{Label: m.text(MsgSettingsActionRefresh, nil), Hint: "reload status", Run: func(m *Model) tea.Cmd { return m.refreshRuntimeStatus() }},
			{Label: m.text(MsgSettingsActionInspect, nil), Hint: "/inspect", Route: "/inspect"},
			{Label: m.text(MsgSettingsActionExplain, nil), Hint: "/explain", Route: "/explain"},
			{Label: m.text(MsgSettingsActionContext, nil), Hint: "/context", Route: "/context"},
			{Label: m.text(MsgSettingsActionUsage, nil), Hint: "/usage", Route: "/usage"},
			{Label: m.text(MsgSettingsActionCompactMode, nil), Hint: "/compact-mode", Run: func(m *Model) tea.Cmd {
				m.compactMode = !m.compactMode
				m.push(m.text(MsgUpdateCompactMode, MessageArgs{"state": map[bool]string{true: "on", false: "off"}[m.compactMode]}))
				m.layout()
				return nil
			}},
			{Label: m.text(MsgSettingsActionModelPicker, nil), Hint: "/model", Route: "/model"},
			{Label: m.text(MsgSettingsActionPlan, nil), Hint: "/plan", Route: "/plan"},
		}
	}
}

func (m *Model) settingsKey(key string) (tea.Cmd, bool) {
	if m.settings == nil {
		return nil, false
	}
	actions := m.settingsActions()
	if m.settings.cursor >= len(actions) {
		m.settings.cursor = maxInt(len(actions)-1, 0)
	}
	switch {
	case m.keys.matches(KeyContextPager, ActionPagerClose, key),
		m.keys.matches(KeyContextGlobal, ActionGlobalHelp, key),
		key == "esc":
		m.closeSettings()
		return m.resumeQueuedAfterTransient(), true
	case key == "left" || key == "h":
		if m.settings.tab > settingsTabOverview {
			m.settings.tab--
			m.settings.cursor = 0
		}
	case key == "right" || key == "l" || key == "tab":
		if m.settings.tab < settingsTabExtensions {
			m.settings.tab++
			m.settings.cursor = 0
		}
	case m.keys.matches(KeyContextPager, ActionPagerUp, key), key == "up" || key == "k":
		if m.settings.cursor > 0 {
			m.settings.cursor--
		}
	case m.keys.matches(KeyContextPager, ActionPagerDown, key), key == "down" || key == "j":
		if m.settings.cursor+1 < len(actions) {
			m.settings.cursor++
		}
	case key == "enter":
		if len(actions) == 0 {
			return nil, true
		}
		action := actions[m.settings.cursor]
		m.closeSettings()
		if action.Run != nil {
			return action.Run(m), true
		}
		if action.Route != "" {
			return m.slashCommand(action.Route), true
		}
		return nil, true
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) settingsOverlayView() string {
	if m.settings == nil {
		return ""
	}
	contentWidth := maxInt(m.width-8, 1)
	tabs := m.settingsTabs()
	var tabLine strings.Builder
	for i, name := range tabs {
		if i > 0 {
			tabLine.WriteString("  ")
		}
		label := name
		if settingsTab(i) == m.settings.tab {
			label = m.th.Style(theme.RoleTitle).Render("[" + name + "]")
		} else {
			label = m.th.Style(theme.RoleMuted).Render(name)
		}
		tabLine.WriteString(label)
	}
	lines := []string{
		m.th.Style(theme.RoleWarning).Render(m.text(MsgSettingsTitle, nil)),
		tabLine.String(),
		"",
	}
	lines = append(lines, m.settingsOverviewLines(contentWidth)...)
	lines = append(lines, "")
	actions := m.settingsActions()
	for i, action := range actions {
		prefix := "  "
		style := m.th.Style(theme.RoleText)
		if i == m.settings.cursor {
			prefix = "> "
			style = m.th.Style(theme.RoleTitle)
		}
		lines = append(lines, fitRenderedLine(style.Render(prefix+action.Label+"  "+m.th.Style(theme.RoleMuted).Render(action.Hint)), contentWidth))
	}
	footer := m.text(MsgSettingsFooter, MessageArgs{
		"close": m.keys.label(KeyContextPager, ActionPagerClose),
	})
	lines = append(lines, "", fitRenderedLine(m.th.Style(theme.RoleMuted).Render(footer), contentWidth))
	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1)
	if color := m.th.Color(theme.RoleWarning); color != nil {
		style = style.BorderForeground(color)
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (m *Model) settingsOverviewLines(width int) []string {
	model, isModel := m.runtimeModelLabel()
	if model == "" {
		model = "n/a"
	} else if !isModel {
		model = "backend:" + model
	}
	effort := m.reasoningEffort
	if effort == "" {
		effort = "default"
	}
	rows := []string{
		m.text(MsgSettingsRowSession, MessageArgs{"session": shortID(m.sessionID)}),
		m.text(MsgSettingsRowMode, MessageArgs{"mode": m.modeLabel()}),
		m.text(MsgSettingsRowModel, MessageArgs{"model": model, "effort": effort}),
		m.text(MsgSettingsRowProfile, MessageArgs{"profile": stringOr(m.runtime.Profile, "unknown")}),
		m.text(MsgSettingsRowSandbox, MessageArgs{"sandbox": stringOr(m.runtime.Sandbox, "unknown")}),
		m.text(MsgSettingsRowApproval, MessageArgs{"approval": stringOr(m.approvalModeLabel(), "unknown")}),
		m.text(MsgSettingsRowContext, MessageArgs{"context": m.contextStatusLabel()}),
		m.text(MsgSettingsRowCompact, MessageArgs{"state": map[bool]string{true: "on", false: "off"}[m.compactMode]}),
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, fitRenderedLine(m.th.Style(theme.RoleMuted).Render(row), width))
	}
	return out
}

func (m *Model) modeLabel() string {
	if m.mode == "plan" {
		return "plan"
	}
	if m.mode == "" {
		return "build"
	}
	return m.mode
}

func (m *Model) contextStatusLabel() string {
	if !m.runtime.ContextAvailable {
		if m.runtime.ContextSource != "" {
			return m.runtime.ContextSource
		}
		return "n/a"
	}
	if m.runtime.ContextLimit > 0 {
		return fmt.Sprintf("%d%% (%d/%d · %s)", m.runtime.ContextPercent, m.runtime.ContextUsed, m.runtime.ContextLimit, stringOr(m.runtime.ContextSource, "provider"))
	}
	return fmt.Sprintf("%d tokens · %s", m.runtime.ContextUsed, stringOr(m.runtime.ContextSource, "provider"))
}

type statusFooterItem struct {
	text string
	role theme.Role
}

func statusJoin(items []statusFooterItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.text) != "" {
			parts = append(parts, item.text)
		}
	}
	return strings.Join(parts, " · ")
}

func (m *Model) renderStatusItems(items []statusFooterItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.text) != "" {
			parts = append(parts, m.th.Style(item.role).Render(item.text))
		}
	}
	return strings.Join(parts, m.th.Style(theme.RoleMuted).Render(" · "))
}

func (m *Model) statusFooterView(width int) string {
	if width <= 0 {
		return ""
	}
	model, isModel := m.runtimeModelLabel()
	if isModel && model != "" && m.reasoningEffort != "" && m.reasoningEffort != "default" {
		model += "/" + m.reasoningEffort
	}
	mode := statusFooterItem{text: m.modeLabel(), role: theme.RoleMuted}
	workspace := statusFooterItem{role: theme.RoleInfo}
	if root := strings.TrimSpace(m.workspaceRoot); root != "" {
		name := filepath.Base(filepath.Clean(root))
		if name != "." && name != string(filepath.Separator) {
			workspace.text = name
		}
	}
	modelItem := statusFooterItem{role: theme.RoleInfo}
	if model != "" {
		if isModel {
			modelItem.text = "model:" + model
		} else {
			modelItem.text = "backend:" + model
		}
	}
	profile := statusFooterItem{role: theme.RoleWarning}
	if value := strings.TrimSpace(m.runtime.Profile); value != "" {
		profile.text = "profile:" + value
	}
	sandbox := statusFooterItem{role: theme.RoleMuted}
	if value := strings.TrimSpace(m.runtime.Sandbox); value != "" && value != "unknown" {
		sandbox.text = "sandbox:" + value
	}
	approval := statusFooterItem{role: theme.RoleWarning}
	if am := m.approvalModeLabel(); am == "always-approve" {
		approval.text = "always-approve"
	} else if am == "dont-ask" {
		approval.text = "dont-ask"
	} else if am == "accept-edits" {
		approval.text = "accept-edits"
	} else if am == "ask" {
		approval.role = theme.RoleMuted
		approval.text = "ask"
	}
	context := statusFooterItem{role: theme.RoleMuted}
	if value := m.contextFooterToken(); value != "-" {
		context.text = "ctx:" + value
	}
	activity := m.statusActivityItem()
	modelHint := statusFooterItem{text: "/model", role: theme.RoleMuted}
	settingsHint := statusFooterItem{text: m.text(MsgStatusSettingsHint, MessageArgs{"key": primaryKeyLabel(m.keys.keys(KeyContextGlobal, ActionGlobalSettings))}), role: theme.RoleMuted}
	helpHint := statusFooterItem{text: m.text(MsgStatusHelpHint, MessageArgs{"key": primaryKeyLabel(m.keys.keys(KeyContextGlobal, ActionGlobalHelp))}), role: theme.RoleMuted}

	// Each row is a complete, intentional fallback. This avoids the common
	// terminal failure mode where one long string is truncated at an arbitrary
	// byte and leaves the important state off-screen.
	variants := []struct {
		left, right []statusFooterItem
	}{
		{[]statusFooterItem{workspace, mode, modelItem, profile, sandbox, approval}, []statusFooterItem{activity, context, modelHint, settingsHint, helpHint}},
		{[]statusFooterItem{workspace, mode, modelItem, profile, sandbox, approval}, []statusFooterItem{activity, context, modelHint, helpHint}},
		{[]statusFooterItem{workspace, mode, modelItem, profile, approval}, []statusFooterItem{activity, context, modelHint, helpHint}},
		{[]statusFooterItem{workspace, mode, modelItem, approval}, []statusFooterItem{activity, modelHint, helpHint}},
		{[]statusFooterItem{workspace, modelItem}, []statusFooterItem{activity, modelHint, helpHint}},
		{[]statusFooterItem{workspace, modelItem}, []statusFooterItem{activity, modelHint}},
		{[]statusFooterItem{workspace}, []statusFooterItem{activity}},
		{nil, []statusFooterItem{activity}},
	}
	for _, variant := range variants {
		leftText, rightText := statusJoin(variant.left), statusJoin(variant.right)
		gap := 0
		if leftText != "" && rightText != "" {
			gap = 2
		}
		if ansi.StringWidth(leftText)+gap+ansi.StringWidth(rightText) > width {
			continue
		}
		left, right := m.renderStatusItems(variant.left), m.renderStatusItems(variant.right)
		spaces := width - ansi.StringWidth(left) - ansi.StringWidth(right)
		if left == "" {
			spaces = width - ansi.StringWidth(right)
		}
		if right == "" {
			spaces = 0
		}
		return fitRenderedLine(left+strings.Repeat(" ", maxInt(spaces, 0))+right, width)
	}
	// At operational widths keep the model addressable even when activity has
	// accumulated goal/queue/attention badges. Truncate the transient side,
	// never the configuration anchor. Truly tiny terminals fall back to the
	// activity alone because two illegible fragments are worse than one signal.
	anchor := workspace
	if anchor.text == "" {
		anchor = modelItem
	}
	anchorWidth := ansi.StringWidth(anchor.text)
	if anchorWidth == 0 {
		return fitRenderedLine(m.th.Style(activity.role).Render(activity.text), width)
	}
	if width >= 32 && width-anchorWidth-2 >= 6 {
		left := m.renderStatusItems([]statusFooterItem{anchor})
		rightWidth := width - ansi.StringWidth(left) - 2
		right := fitRenderedLine(m.th.Style(activity.role).Render(activity.text), rightWidth)
		spaces := width - ansi.StringWidth(left) - ansi.StringWidth(right)
		return fitRenderedLine(left+strings.Repeat(" ", maxInt(spaces, 2))+right, width)
	}
	return fitRenderedLine(m.th.Style(activity.role).Render(activity.text), width)
}

func (m *Model) contextFooterToken() string {
	if m.runtime.ContextAvailable && m.runtime.ContextLimit > 0 {
		tok := fmt.Sprintf("%d%%", m.runtime.ContextPercent)
		if m.runtime.CompactAvailable {
			tok += " compact"
		}
		return tok
	}
	if m.runtime.CompactAvailable {
		return "compact"
	}
	return "-"
}

func (m *Model) statusActivityText() string {
	state := m.conversationSnapshot()
	activity := m.text(MsgStatusReady, nil)
	if m.editor != nil {
		activity = m.text(MsgStatusEditingDraft, nil)
	} else if state.Activity == activitySubmitting {
		kind := submissionTask
		if m.submitting != nil {
			kind = m.submitting.kind
		}
		activity = m.text(MsgStatusSending, MessageArgs{"kind": string(kind)})
	} else {
		switch state.Activity {
		case activityRunning:
			activity = m.text(MsgStatusRunning, MessageArgs{"task": shortID(state.Evidence.ActiveTaskID)})
		case activityWaitingApproval:
			activity = m.text(MsgAttentionApproval, nil)
		case activityWaitingQuestion:
			activity = m.text(MsgAttentionInput, nil)
		case activityInterrupted:
			activity = m.text(MsgTaskStatusInterrupted, nil)
		default:
			if state.Outcome != outcomeNone {
				activity = m.taskStatusText(state.Outcome.taskStatus())
			} else {
				switch state.Readiness {
				case readinessChecking:
					activity = m.text(MsgStatusChecking, nil)
				case readinessBlocked:
					activity = m.text(MsgStatusBlocked, nil)
				case readinessUnavailable:
					activity = m.text(MsgStatusNotAttached, nil)
				}
			}
		}
	}
	if m.unseenLines > 0 {
		activity += " · " + m.countText(MsgStatusNew, m.unseenLines, nil)
	}
	if n := m.followUps.len(); n > 0 {
		activity += " · " + m.countText(MsgStatusQueued, n, nil)
	}
	if m.unreadAttention > 0 {
		activity += " · " + m.countText(MsgStatusAttention, m.unreadAttention, nil)
	}
	if m.chord.hint != "" {
		activity += " · " + m.text(MsgStatusChord, MessageArgs{"hint": m.chord.hint})
	}
	if m.goal != nil {
		goal := m.text(MsgStatusGoal, MessageArgs{"status": m.goal.Status})
		if m.goal.TokenBudget > 0 {
			goal += fmt.Sprintf(" %d/%d", m.goal.TokensUsed, m.goal.TokenBudget)
		}
		activity += " · " + goal
	}
	if shell := m.shellModeStatusSuffix(); shell != "" {
		activity += " · " + shell
	}
	return activity
}

func (m *Model) statusActivityItem() statusFooterItem {
	state := m.conversationSnapshot()
	role := theme.RoleSuccess
	switch {
	case state.Outcome == outcomeDegraded || state.Outcome == outcomeFailed || state.Outcome == outcomeCancelled:
		role = theme.RoleError
	case state.Activity == activityWaitingApproval || state.Activity == activityWaitingQuestion || state.Activity == activityInterrupted:
		role = theme.RoleWarning
	case state.Activity == activityRunning || state.Activity == activitySubmitting || m.editor != nil:
		role = theme.RoleInfo
	case state.Readiness == readinessChecking:
		role = theme.RoleInfo
	case state.Readiness == readinessUnavailable || state.Readiness == readinessBlocked:
		role = theme.RoleError
	}
	if m.unreadAttention > 0 || m.chord.hint != "" {
		role = theme.RoleWarning
	}
	return statusFooterItem{text: m.statusActivityText(), role: role}
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func stringOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func (m *Model) cycleInteractionMode() tea.Cmd {
	next := "plan"
	if m.modeLabel() == "plan" {
		next = "build"
	}
	return m.changeMode(next)
}

func (m *Model) refreshRuntimeStatus() tea.Cmd {
	call, sessionID := m.call, m.sessionID
	generation := m.sessionGeneration
	return func() tea.Msg {
		if call == nil {
			return runtimeStatusMsg{sessionID: sessionID, generation: generation, err: errorsNew("daemon not connected")}
		}
		out := runtimeStatusMsg{sessionID: sessionID, generation: generation}
		var sess map[string]any
		if err := call.Call("session.get", map[string]any{"session_id": sessionID}, &sess); err == nil {
			out.profile = str(sess["permission_profile"])
			if plan, ok := sess["plan_mode"].(bool); ok && plan {
				out.mode = "plan"
			}
			if model := str(sess["next_model"]); model != "" {
				out.model = model
			}
			if effort := str(sess["next_reasoning_effort"]); effort != "" {
				out.effort = effort
			}
		}
		var cfg map[string]any
		if err := call.Call("config.inventory", map[string]any{"session_id": sessionID}, &cfg); err == nil {
			if effective, ok := cfg["effective"].(map[string]any); ok {
				if out.profile == "" {
					out.profile = str(effective["permission_profile"])
				}
				if b, ok := effective["sandbox_commands"].(bool); ok {
					out.sandbox = map[bool]string{true: "on", false: "off"}[b]
				}
				if mode := str(effective["approval_mode"]); mode != "" {
					out.approval = mode
				} else if b, ok := effective["interactive_approval"].(bool); ok {
					out.approval = map[bool]string{true: "ask", false: "always-approve"}[b]
				}
				if b, ok := effective["plan_mode"].(bool); ok {
					if b {
						out.mode = "plan"
					} else if out.mode == "" {
						out.mode = "build"
					}
				}
			}
		}
		var ctx map[string]any
		if err := call.Call("context.summary", map[string]any{"session_id": sessionID}, &ctx); err == nil {
			if modelCtx, ok := ctx["model_context_tokens"].(map[string]any); ok {
				out.contextSource = str(modelCtx["measurement"])
				if reason := str(modelCtx["reason"]); reason != "" && out.contextSource == "" {
					out.contextSource = reason
				}
				if avail, ok := modelCtx["available"].(bool); ok {
					out.contextAvailable = avail
				}
				out.contextUsed = intFromAny(modelCtx["tokens"])
				out.contextLimit = intFromAny(modelCtx["limit_tokens"])
				out.contextPercent = intFromAny(modelCtx["used_percent"])
			}
			if compact, ok := ctx["compact"].(map[string]any); ok {
				if avail, ok := compact["available"].(bool); ok {
					out.compactAvailable = avail
				}
				out.compactReason = str(compact["reason"])
				out.compactCheckpoint = str(compact["checkpoint_id"])
			}
			if task, ok := ctx["task"].(map[string]any); ok {
				out.compactTaskID = str(task["task_id"])
			}
		}
		var inventory modelListResponse
		if err := call.Call("model.list", map[string]any{}, &inventory); err != nil {
			out.inventoryErr = err
		} else {
			out.inventory = inventory
			out.inventoryLoaded = true
		}
		return out
	}
}

// errorsNew avoids importing errors solely for one path in this file while
// remaining compatible with the package-level errors import used elsewhere.
func errorsNew(msg string) error { return fmt.Errorf("%s", msg) }

type runtimeStatusMsg struct {
	sessionID         string
	generation        uint64
	profile           string
	sandbox           string
	approval          string
	mode              string
	model             string
	effort            string
	contextUsed       int
	contextLimit      int
	contextPercent    int
	contextSource     string
	contextAvailable  bool
	compactAvailable  bool
	compactReason     string
	compactCheckpoint string
	compactTaskID     string
	inventory         modelListResponse
	inventoryLoaded   bool
	inventoryErr      error
	err               error
}

// Context pressure thresholds (Grok-style ~85% auto-compact when
// session.checkpoint.compact is available — idle task + checkpoint, not mid-execution).
const (
	contextPressureWarning  = 80
	contextPressureCompact  = 85
	contextPressureCritical = 90
)

func (m *Model) handleRuntimeStatus(msg runtimeStatusMsg) tea.Cmd {
	if msg.sessionID != "" && msg.sessionID != m.sessionID {
		return nil
	}
	if msg.generation != 0 && msg.generation != m.sessionGeneration {
		return nil
	}
	if msg.err != nil {
		m.runtime.ReadinessReason = msg.err.Error()
		m.applyConversation(conversationTransition{Kind: transitionReadiness, Readiness: readinessUnavailable, EventType: "runtime.status", Status: msg.err.Error()})
		m.layout()
		return nil
	}
	if msg.profile != "" {
		m.runtime.Profile = msg.profile
	}
	if msg.sandbox != "" {
		m.runtime.Sandbox = msg.sandbox
	}
	if msg.approval != "" {
		m.applyApprovalModeToRuntime(msg.approval)
	}
	if msg.mode != "" {
		m.mode = msg.mode
	}
	if msg.model != "" && !m.modelPinned {
		m.model = msg.model
	}
	if msg.effort != "" {
		m.reasoningEffort = msg.effort
	}
	m.runtime.ContextUsed = msg.contextUsed
	m.runtime.ContextLimit = msg.contextLimit
	m.runtime.ContextPercent = msg.contextPercent
	m.runtime.ContextSource = msg.contextSource
	m.runtime.ContextAvailable = msg.contextAvailable
	m.runtime.CompactAvailable = msg.compactAvailable
	m.runtime.CompactReason = msg.compactReason
	m.runtime.CompactCheckpoint = msg.compactCheckpoint
	m.runtime.CompactTaskID = msg.compactTaskID
	if msg.inventoryLoaded {
		m.applyModelInventory(msg.inventory)
	} else if msg.inventoryErr != nil {
		m.runtime.ReadinessReason = msg.inventoryErr.Error()
		m.applyConversation(conversationTransition{Kind: transitionReadiness, Readiness: readinessUnavailable, EventType: "model.inventory", Status: msg.inventoryErr.Error()})
	}
	m.runtime.LastRefresh = m.now()
	cmd := m.applyContextPressurePolicy()
	side := m.flushPendingSideQuestion()
	m.layout()
	if cmd == nil {
		return side
	}
	if side == nil {
		return cmd
	}
	return tea.Batch(cmd, side)
}

// applyContextPressurePolicy nudges the operator and, when safe, auto-compacts.
// Safe = daemon reported compact.available (idle task + persisted checkpoint).
func (m *Model) applyContextPressurePolicy() tea.Cmd {
	if !m.runtime.ContextAvailable || m.runtime.ContextLimit <= 0 {
		return nil
	}
	pct := m.runtime.ContextPercent
	switch {
	case pct >= contextPressureCompact && m.runtime.CompactAvailable && m.contextNudgeLevel < 3:
		m.contextNudgeLevel = 3
		m.push(m.th.Style(theme.RoleWarning).Render(m.text(MsgContextAutoCompact, MessageArgs{
			"percent": pct, "checkpoint": m.runtime.CompactCheckpoint,
		})))
		params := map[string]any{"session_id": m.sessionID}
		if m.runtime.CompactTaskID != "" {
			params["task_id"] = m.runtime.CompactTaskID
		}
		return m.queryOperationalSurface("compact", "session.checkpoint.compact", params)
	case pct >= contextPressureCritical && m.contextNudgeLevel < 2:
		m.contextNudgeLevel = 2
		reason := m.runtime.CompactReason
		if reason == "" {
			reason = "compact requires an idle task with a persisted checkpoint"
		}
		m.push(m.th.Style(theme.RoleWarning).Render(m.text(MsgContextPressureCritical, MessageArgs{
			"percent": pct, "reason": reason,
		})))
	case pct >= contextPressureWarning && m.contextNudgeLevel < 1:
		m.contextNudgeLevel = 1
		m.push(m.th.Style(theme.RoleMuted).Render(m.text(MsgContextPressureWarning, MessageArgs{"percent": pct})))
	case pct < contextPressureWarning:
		m.contextNudgeLevel = 0
	}
	return nil
}

func intFromAny(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	default:
		return 0
	}
}

// humanizeOperationalSurface turns daemon inventory maps into operator-facing
// summaries. Details remain available but are capped so the transcript stays quiet.
func (m *Model) humanizeOperationalSurface(kind string, data map[string]any) []string {
	switch kind {
	case "context":
		return m.humanizeContext(data)
	case "config":
		return m.humanizeConfig(data)
	case "permissions":
		return m.humanizePermissions(data)
	case "skills":
		return m.humanizeNamedList(data, "skills", "name", "description")
	case "hooks":
		return m.humanizeNamedList(data, "hooks", "event", "source")
	case "extensions":
		return m.humanizeNamedList(data, "extensions", "name", "source")
	case "mcp":
		return m.humanizeMCP(data)
	case "status":
		return m.humanizeSessionStatus(data)
	case "inspect":
		return m.humanizeInspect(data)
	case "agents":
		return m.humanizeAgents(data)
	case "always-approve":
		return compactMapLines(data, "")
	default:
		lines := compactMapLines(data, "")
		return capLines(lines, 40)
	}
}

func (m *Model) humanizeContext(data map[string]any) []string {
	lines := []string{m.text(MsgContextSummaryHeader, nil)}
	if modelCtx, ok := data["model_context_tokens"].(map[string]any); ok {
		if avail, _ := modelCtx["available"].(bool); avail {
			used := intFromAny(modelCtx["tokens"])
			limit := intFromAny(modelCtx["limit_tokens"])
			percent := intFromAny(modelCtx["used_percent"])
			src := str(modelCtx["measurement"])
			bar := contextBar(percent, 24)
			lines = append(lines, fmt.Sprintf("%s  %d%%  %d/%d", bar, percent, used, limit))
			if src != "" {
				lines = append(lines, m.text(MsgContextSource, MessageArgs{"source": src}))
			}
			if remaining := intFromAny(modelCtx["remaining_tokens"]); remaining > 0 {
				lines = append(lines, m.text(MsgContextRemaining, MessageArgs{"remaining": remaining}))
			}
			// Refresh footer snapshot from the same read.
			m.runtime.ContextAvailable = true
			m.runtime.ContextUsed = used
			m.runtime.ContextLimit = limit
			m.runtime.ContextPercent = percent
			m.runtime.ContextSource = src
		} else {
			reason := str(modelCtx["reason"])
			if reason == "" {
				reason = "unavailable"
			}
			lines = append(lines, m.text(MsgContextUnavailable, MessageArgs{"reason": reason}))
			m.runtime.ContextAvailable = false
			m.runtime.ContextSource = reason
		}
	}
	if compact, ok := data["compact"].(map[string]any); ok {
		if avail, _ := compact["available"].(bool); avail {
			m.runtime.CompactAvailable = true
			m.runtime.CompactCheckpoint = str(compact["checkpoint_id"])
			lines = append(lines, m.text(MsgContextCompactReady, MessageArgs{"checkpoint": str(compact["checkpoint_id"])}))
		} else if reason := str(compact["reason"]); reason != "" {
			m.runtime.CompactAvailable = false
			m.runtime.CompactReason = reason
			lines = append(lines, m.text(MsgContextCompactBlocked, MessageArgs{"reason": reason}))
		}
	}
	lines = append(lines, "", m.text(MsgOperationalDetails, nil))
	lines = append(lines, capLines(compactMapLines(data, ""), 24)...)
	return lines
}

func contextBar(percent, width int) string {
	if width < 8 {
		width = 8
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := percent * width / 100
	return "[" + strings.Repeat("█", filled) + strings.Repeat("·", width-filled) + "]"
}

func (m *Model) humanizeConfig(data map[string]any) []string {
	lines := []string{m.text(MsgConfigSummaryHeader, nil)}
	if effective, ok := data["effective"].(map[string]any); ok {
		keys := []string{"permission_profile", "plan_mode", "model", "reasoning_effort", "sandbox_commands", "approval_mode", "interactive_approval", "disable_always_approve", "safe_mode"}
		for _, key := range keys {
			if v, ok := effective[key]; ok {
				lines = append(lines, fmt.Sprintf("%s: %v", key, v))
			}
		}
		if profile := str(effective["permission_profile"]); profile != "" {
			m.runtime.Profile = profile
		}
		if b, ok := effective["sandbox_commands"].(bool); ok {
			m.runtime.Sandbox = map[bool]string{true: "on", false: "off"}[b]
		}
		if mode := str(effective["approval_mode"]); mode != "" {
			m.applyApprovalModeToRuntime(mode)
		} else if b, ok := effective["interactive_approval"].(bool); ok {
			m.applyApprovalModeToRuntime(map[bool]string{true: "ask", false: "always-approve"}[b])
		}
	}
	if mutation := str(data["mutation"]); mutation != "" {
		lines = append(lines, "", m.th.Style(theme.RoleMuted).Render(mutation))
	}
	lines = append(lines, "", m.text(MsgConfigHintSettings, nil))
	return lines
}

func (m *Model) humanizePermissions(data map[string]any) []string {
	lines := []string{m.text(MsgPermissionsSummaryHeader, nil)}
	if profile := str(data["profile"]); profile != "" {
		lines = append(lines, m.text(MsgPermissionsProfile, MessageArgs{"profile": profile}))
		m.runtime.Profile = profile
	}
	if source := str(data["source"]); source != "" {
		lines = append(lines, m.text(MsgPermissionsSource, MessageArgs{"source": source}))
	}
	if mutation := str(data["mutation"]); mutation != "" {
		lines = append(lines, m.th.Style(theme.RoleMuted).Render(mutation))
	}
	if choices, ok := data["choices"].([]any); ok {
		lines = append(lines, m.text(MsgPermissionsChoices, nil))
		for _, raw := range choices {
			if row, ok := raw.(map[string]any); ok {
				lines = append(lines, fmt.Sprintf("  - %s (%s)", str(row["name"]), str(row["risk"])))
			}
		}
	}
	return lines
}

func (m *Model) humanizeSessionStatus(data map[string]any) []string {
	lines := []string{m.text(MsgSessionStatusHeader, nil)}
	for _, key := range []string{"session_id", "status", "permission_profile", "plan_mode", "next_model", "next_reasoning_effort", "workspace_root"} {
		if v, ok := data[key]; ok && fmt.Sprint(v) != "" {
			lines = append(lines, fmt.Sprintf("%s: %v", key, v))
		}
	}
	if profile := str(data["permission_profile"]); profile != "" {
		m.runtime.Profile = profile
	}
	return lines
}

func (m *Model) humanizeAgents(data map[string]any) []string {
	lines := []string{m.text(MsgAgentsSummaryHeader, nil)}
	agents, _ := data["agents"].([]any)
	if len(agents) == 0 {
		// Some handlers return a bare list under different keys.
		for _, key := range []string{"items", "available"} {
			if arr, ok := data[key].([]any); ok {
				agents = arr
				break
			}
		}
	}
	if len(agents) == 0 {
		lines = append(lines, m.text(MsgOperationalEmpty, nil))
		return lines
	}
	for i, raw := range agents {
		if i >= 32 {
			lines = append(lines, fmt.Sprintf("… +%d more", len(agents)-i))
			break
		}
		row, ok := raw.(map[string]any)
		if !ok {
			lines = append(lines, fmt.Sprintf("  - %v", raw))
			continue
		}
		name := str(row["name"])
		if name == "" {
			name = str(row["id"])
		}
		desc := str(row["description"])
		profile := str(row["profile"])
		extra := nonEmpty(profile, str(row["source"]))
		line := "  - " + name
		if len(extra) > 0 {
			line += " · " + strings.Join(extra, " · ")
		}
		if desc != "" {
			line += " — " + desc
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", m.text(MsgAgentsHint, nil))
	return lines
}

func (m *Model) humanizeMCP(data map[string]any) []string {
	lines := []string{}
	if count, ok := data["count"]; ok {
		lines = append(lines, fmt.Sprintf("count: %v", count))
	}
	servers, _ := data["servers"].([]any)
	if len(servers) == 0 {
		lines = append(lines, m.text(MsgOperationalEmpty, nil))
		return lines
	}
	for _, raw := range servers {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := str(row["name"])
		health := str(row["health"])
		if health == "" {
			health = str(row["status"])
		}
		line := "  - " + name
		if health != "" {
			line += " · " + health
		}
		lines = append(lines, line)
		if tools, ok := row["tools"].([]any); ok {
			for _, toolRaw := range tools {
				tool, ok := toolRaw.(map[string]any)
				if !ok {
					continue
				}
				tname := str(tool["name"])
				tdesc := str(tool["description"])
				if tdesc != "" {
					lines = append(lines, fmt.Sprintf("      · %s — %s", tname, tdesc))
				} else {
					lines = append(lines, fmt.Sprintf("      · %s", tname))
				}
			}
		}
	}
	return lines
}

func (m *Model) humanizeNamedList(data map[string]any, listKey, nameKey, detailKey string) []string {
	lines := []string{}
	if count, ok := data["count"]; ok {
		lines = append(lines, fmt.Sprintf("count: %v", count))
	}
	if mutation := str(data["mutation"]); mutation != "" {
		lines = append(lines, m.th.Style(theme.RoleMuted).Render(mutation))
	}
	list, _ := data[listKey].([]any)
	if len(list) == 0 {
		// Try common alternate keys.
		for _, alt := range []string{"items", "servers", "extensions", "hooks", "skills"} {
			if alt == listKey {
				continue
			}
			if candidate, ok := data[alt].([]any); ok {
				list = candidate
				break
			}
		}
	}
	if len(list) == 0 {
		lines = append(lines, m.text(MsgOperationalEmpty, nil))
		return lines
	}
	for i, raw := range list {
		if i >= 24 {
			lines = append(lines, fmt.Sprintf("… +%d more", len(list)-i))
			break
		}
		row, ok := raw.(map[string]any)
		if !ok {
			lines = append(lines, fmt.Sprintf("  - %v", raw))
			continue
		}
		name := str(row[nameKey])
		if name == "" {
			name = str(row["name"])
		}
		detail := str(row[detailKey])
		if detail == "" {
			detail = str(row["description"])
		}
		extra := ""
		if inv, ok := row["user_invocable"].(bool); ok && inv {
			extra = "  → /" + name
		}
		if detail != "" {
			lines = append(lines, fmt.Sprintf("  - %s — %s%s", name, detail, extra))
		} else {
			lines = append(lines, fmt.Sprintf("  - %s%s", name, extra))
		}
	}
	return lines
}

func capLines(lines []string, max int) []string {
	if len(lines) <= max {
		return lines
	}
	return append(lines[:max], fmt.Sprintf("… truncated %d lines", len(lines)-max))
}

func (m *Model) exportTranscript(path string) tea.Cmd {
	if path == "" {
		path = filepath.Join(m.workspaceRoot, "carina-transcript-"+time.Now().Format("20060102-150405")+".md")
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.workspaceRoot, path)
	}
	body := m.tr.plainExport()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		m.push(m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}))
		return nil
	}
	m.push(m.text(MsgUpdateExportDone, MessageArgs{"path": path}))
	return nil
}

func summarizeDraft(d promptDraft) string {
	text := strings.TrimSpace(d.Text)
	if text == "" {
		return "(empty)"
	}
	if len(text) > 80 {
		return text[:80] + "…"
	}
	return text
}

func (m *Model) rememberNote(content string) tea.Cmd {
	content = strings.TrimSpace(content)
	if content == "" {
		m.push(m.text(MsgUpdateUsageRemember, nil))
		return nil
	}
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return operationalSurfaceMsg{sessionID: sessionID, kind: "memory", err: errorsNew("daemon not connected")}
		}
		var out map[string]any
		err := call.Call("memory.write", map[string]any{
			"session_id": sessionID,
			"target":     "memory",
			"action":     "add",
			"content":    content,
		}, &out)
		return operationalSurfaceMsg{sessionID: sessionID, kind: "memory", data: out, err: err}
	}
}

func (m *Model) initProjectRules() tea.Cmd {
	root := m.workspaceRoot
	if root == "" {
		root, _ = os.Getwd()
	}
	path := filepath.Join(root, "AGENTS.md")
	if _, err := os.Stat(path); err == nil {
		m.push(m.text(MsgUpdateInitExists, MessageArgs{"path": path}))
		return nil
	}
	body := "# AGENTS.md\n\nProject instructions for coding agents.\n\n## Build & test\n\n- Prefer small, reviewable changes.\n- Run the smallest relevant tests before claiming done.\n\n## Safety\n\n- Do not commit secrets.\n- Do not force-push shared branches without confirmation.\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		m.push(m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}))
		return nil
	}
	m.push(m.text(MsgUpdateInitCreated, MessageArgs{"path": path}))
	return nil
}

// resolveDynamicSlash also accepts user-invocable skills so discovery equals execution
// (Grok/CC skill-as-slash pattern).
func (m *Model) resolveDynamicSlashWithSkills(text string) tea.Cmd {
	call, sid := m.call, m.sessionID
	draft := m.currentDraft()
	if strings.TrimSpace(draft.Text) != strings.TrimSpace(text) {
		draft = promptDraft{Text: text}
	}
	name := strings.TrimPrefix(strings.Fields(text)[0], "/")
	return func() tea.Msg {
		if call == nil {
			return dynamicSlashResolvedMsg{draft: draft, err: errorsNew("daemon not connected")}
		}
		var out struct {
			Commands []struct {
				Name   string `json:"name"`
				Source string `json:"source"`
			} `json:"commands"`
		}
		err := call.Call("command.list", map[string]any{"session_id": sid}, &out)
		found := false
		if err == nil {
			for _, c := range out.Commands {
				if c.Name == name {
					found = true
					break
				}
			}
		}
		if !found {
			var skills map[string]any
			if skillErr := call.Call("skill.inventory", map[string]any{"session_id": sid}, &skills); skillErr == nil {
				if rows, ok := skills["skills"].([]any); ok {
					for _, raw := range rows {
						row, ok := raw.(map[string]any)
						if !ok {
							continue
						}
						if str(row["name"]) == name {
							if inv, ok := row["user_invocable"].(bool); ok && !inv {
								continue
							}
							found = true
							// Expand to an explicit skill invocation prompt for the agent.
							draft.Text = fmt.Sprintf("Use the skill %q.\n\nOriginal command: %s", name, text)
							break
						}
					}
				}
			}
		}
		return dynamicSlashResolvedMsg{draft: draft, found: found, err: err}
	}
}

// sortedKeys is used by tests and stable summaries.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
