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

	"github.com/Nebutra/carina/go/tui/theme"
)

// runtimeStatus is the product chrome snapshot shown in the footer and settings shell.
// Fields are best-effort: exact when daemon reports them, empty when unknown.
type runtimeStatus struct {
	Profile            string
	Sandbox            string // on|off|unknown
	InteractiveApprove string // on|off|unknown
	ContextUsed        int
	ContextLimit       int
	ContextPercent     int
	ContextSource      string
	ContextAvailable   bool
	LastRefresh        time.Time
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
	Label  string
	Hint   string
	Run    func(*Model) tea.Cmd
	Route  string // optional slash routed through slashCommand
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
			{Label: m.text(MsgSettingsActionPlan, nil), Hint: "/mode plan", Route: "/mode plan"},
			{Label: m.text(MsgSettingsActionBuild, nil), Hint: "/mode build", Route: "/mode build"},
			{Label: m.text(MsgSettingsActionPermissions, nil), Hint: "/permissions", Route: "/permissions"},
			{Label: m.text(MsgSettingsActionSafeEdit, nil), Hint: "/permissions new safe-edit", Route: "/permissions new safe-edit"},
			{Label: m.text(MsgSettingsActionFullWorkspace, nil), Hint: "/permissions new full-workspace --yes", Route: "/permissions new full-workspace --yes"},
		}
	case m.settings != nil && m.settings.tab == settingsTabModel:
		return []settingsAction{
			{Label: m.text(MsgSettingsActionModelPicker, nil), Hint: "/model", Route: "/model"},
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
			{Label: m.text(MsgSettingsActionContext, nil), Hint: "/context", Route: "/context"},
			{Label: m.text(MsgSettingsActionUsage, nil), Hint: "/usage", Route: "/usage"},
			{Label: m.text(MsgSettingsActionCompactMode, nil), Hint: "/compact-mode", Run: func(m *Model) tea.Cmd {
				m.compactMode = !m.compactMode
				m.push(m.text(MsgUpdateCompactMode, MessageArgs{"state": map[bool]string{true: "on", false: "off"}[m.compactMode]}))
				m.layout()
				return nil
			}},
			{Label: m.text(MsgSettingsActionModelPicker, nil), Hint: "/model", Route: "/model"},
			{Label: m.text(MsgSettingsActionPlan, nil), Hint: "/mode plan", Route: "/mode plan"},
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
	model := m.model
	if model == "" {
		model = "default"
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
		m.text(MsgSettingsRowApproval, MessageArgs{"approval": stringOr(m.runtime.InteractiveApprove, "unknown")}),
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

func (m *Model) statusFooterLine() string {
	session := m.text(MsgStatusNotAttached, nil)
	if m.sessionID != "" {
		session = m.text(MsgStatusSession, MessageArgs{"id": shortID(m.sessionID)})
	}
	model := m.model
	if model == "" {
		model = "default"
	}
	if m.reasoningEffort != "" && m.reasoningEffort != "default" {
		model += "/" + m.reasoningEffort
	}
	activity := m.statusActivityText()
	// Append product chrome tokens that competitors surface continuously.
	extra := []string{}
	if p := strings.TrimSpace(m.runtime.Profile); p != "" {
		extra = append(extra, p)
	}
	if s := strings.TrimSpace(m.runtime.Sandbox); s != "" && s != "unknown" {
		extra = append(extra, "sbx:"+s)
	}
	if c := m.contextFooterToken(); c != "-" {
		extra = append(extra, "ctx:"+c)
	}
	if len(extra) > 0 {
		activity = strings.Join(extra, " · ") + " · " + activity
	}
	return m.text(MsgStatusFooter, MessageArgs{
		"session":  session,
		"mode":     m.modeLabel(),
		"model":    model,
		"activity": activity,
		"help":     primaryKeyLabel(m.keys.keys(KeyContextGlobal, ActionGlobalHelp)),
	})
}

func (m *Model) contextFooterToken() string {
	if m.runtime.ContextAvailable && m.runtime.ContextLimit > 0 {
		return fmt.Sprintf("%d%%", m.runtime.ContextPercent)
	}
	return "-"
}

func (m *Model) statusActivityText() string {
	activity := m.text(MsgStatusReady, nil)
	if m.editor != nil {
		activity = m.text(MsgStatusEditingDraft, nil)
	} else if m.submitting != nil {
		activity = m.text(MsgStatusSending, MessageArgs{"kind": string(m.submitting.kind)})
	} else if m.inFlightTaskID != "" {
		activity = m.text(MsgStatusRunning, MessageArgs{"task": shortID(m.inFlightTaskID)})
		if node := m.tasks.nodes[m.inFlightTaskID]; node != nil {
			requested, effective := node.RequestedModel, node.EffectiveModel
			if requested == "" {
				requested = "default"
			}
			if effective == "" {
				effective = "pending"
			}
			activity += " · " + m.text(MsgStatusRunningModel, MessageArgs{"requested": requested, "effective": effective})
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
	return activity
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
				if b, ok := effective["interactive_approval"].(bool); ok {
					out.approval = map[bool]string{true: "on", false: "off"}[b]
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
		}
		return out
	}
}

// errorsNew avoids importing errors solely for one path in this file while
// remaining compatible with the package-level errors import used elsewhere.
func errorsNew(msg string) error { return fmt.Errorf("%s", msg) }

type runtimeStatusMsg struct {
	sessionID        string
	generation       uint64
	profile          string
	sandbox          string
	approval         string
	mode             string
	model            string
	effort           string
	contextUsed      int
	contextLimit     int
	contextPercent   int
	contextSource    string
	contextAvailable bool
	err              error
}

func (m *Model) handleRuntimeStatus(msg runtimeStatusMsg) {
	if msg.sessionID != "" && msg.sessionID != m.sessionID {
		return
	}
	if msg.generation != 0 && msg.generation != m.sessionGeneration {
		return
	}
	if msg.err != nil {
		return
	}
	if msg.profile != "" {
		m.runtime.Profile = msg.profile
	}
	if msg.sandbox != "" {
		m.runtime.Sandbox = msg.sandbox
	}
	if msg.approval != "" {
		m.runtime.InteractiveApprove = msg.approval
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
	m.runtime.LastRefresh = m.now()
	m.layout()
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
			lines = append(lines, m.text(MsgContextCompactReady, MessageArgs{"checkpoint": str(compact["checkpoint_id"])}))
		} else if reason := str(compact["reason"]); reason != "" {
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
		keys := []string{"permission_profile", "plan_mode", "model", "reasoning_effort", "sandbox_commands", "interactive_approval", "safe_mode"}
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
		if b, ok := effective["interactive_approval"].(bool); ok {
			m.runtime.InteractiveApprove = map[bool]string{true: "on", false: "off"}[b]
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
	m.push(strings.Join(lines, "\n"))
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

func (m *Model) viewPlanSurface() {
	lines := []string{m.th.Style(theme.RoleTitle).Render(m.text(MsgViewPlanTitle, nil))}
	lines = append(lines, m.text(MsgViewPlanMode, MessageArgs{"mode": m.modeLabel()}))
	if m.modeLabel() == "plan" {
		lines = append(lines, m.text(MsgViewPlanActive, nil))
	} else {
		lines = append(lines, m.text(MsgViewPlanInactive, nil))
	}
	lines = append(lines, m.text(MsgViewPlanHint, nil))
	m.push(strings.Join(lines, "\n"))
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
