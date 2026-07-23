package tui

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

type sessionListItem struct {
	SessionID        string `json:"session_id"`
	Name             string `json:"name"`
	WorkspaceRoot    string `json:"workspace_root"`
	Status           string `json:"status"`
	ParentID         string `json:"parent_id"`
	ForkedFromTaskID string `json:"forked_from_task_id"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
	LatestTaskID     string `json:"latest_task_id"`
	TaskRevision     int64  `json:"task_revision"`
	TaskStatus       string `json:"task_status"`
	Summary          string `json:"summary"`
	Continuity       struct {
		Activity string `json:"activity"`
		Outcome  string `json:"outcome"`
		Progress string `json:"progress"`
		Recovery struct {
			Disposition  string          `json:"disposition"`
			Reason       string          `json:"reason"`
			CheckpointID string          `json:"checkpoint_id"`
			Proofs       map[string]bool `json:"proofs"`
		} `json:"recovery"`
		Interruption *struct {
			Kind             string `json:"kind"`
			Certainty        string `json:"certainty"`
			BillingUncertain bool   `json:"billing_uncertain"`
		} `json:"interruption"`
		RecoveryGeneration int64 `json:"recovery_generation"`
	} `json:"continuity"`
}

// SessionListItem is the workspace-navigation session shape returned by the
// application coordinator after it validates a destination runtime.
type SessionListItem = sessionListItem

type WorkspaceListItem struct {
	Root      string
	Name      string
	RuntimeID string
	Current   bool
	Error     string
}

type WorkspaceDestination struct {
	Target   ConnectionTarget
	Sessions []SessionListItem
}

type sessionPickerScope int

const (
	sessionScopeCurrent sessionPickerScope = iota
	sessionScopeAll
)

type sessionPickerStage int

const (
	sessionStageSessions sessionPickerStage = iota
	sessionStageWorkspaces
)

func (m *Model) sessionStatusLabel(status string) string {
	switch strings.ToLower(status) {
	case "active":
		return m.text(MsgSessionStatusActive, nil)
	case "paused":
		return m.text(MsgSessionStatusPaused, nil)
	case "closed":
		return m.text(MsgSessionStatusClosed, nil)
	default:
		return status
	}
}

func (m *Model) sessionAge(value string) string {
	created, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	d := time.Since(created)
	if d < time.Minute {
		return m.text(MsgSessionAgeNow, nil)
	}
	if d < time.Hour {
		return m.text(MsgSessionAgeMinutes, MessageArgs{"count": int(d.Minutes())})
	}
	if d < 24*time.Hour {
		return m.text(MsgSessionAgeHours, MessageArgs{"count": int(d.Hours())})
	}
	return m.text(MsgSessionAgeDays, MessageArgs{"count": int(d.Hours() / 24)})
}

type sessionPickerState struct {
	generation       uint64
	loading          bool
	loadError        bool
	selected, scroll int
	items            []sessionListItem
	scope            sessionPickerScope
	stage            sessionPickerStage
	workspaces       []WorkspaceListItem
	destination      ConnectionTarget
	status           string
}
type sessionListMsg struct {
	generation uint64
	items      []sessionListItem
	err        error
}
type sessionActionMsg struct {
	generation uint64
	action     string
	session    sessionListItem
	err        error
}
type workspaceListMsg struct {
	generation uint64
	items      []WorkspaceListItem
	err        error
}
type workspaceSessionsMsg struct {
	generation  uint64
	destination WorkspaceDestination
	err         error
}
type workspaceResumeMsg struct {
	generation uint64
	target     ConnectionTarget
	journal    submissionJournal
	token      uint64
	err        error
}

func (m *Model) sessionSwitchBlocker() (MessageID, bool) {
	switch {
	case !draftEmpty(m.currentDraft()):
		return MsgSessionSwitchDraft, true
	case m.inFlightTaskID != "":
		return MsgSessionSwitchTask, true
	case m.submitting != nil:
		return MsgSessionSwitchSubmission, true
	case m.retrySubmission != nil:
		return MsgSessionSwitchRetry, true
	case m.followUps.len() > 0:
		return MsgSessionSwitchQueue, true
	case m.approval != nil || m.question != nil:
		return MsgSessionSwitchGovernance, true
	case m.editor != nil:
		return MsgSessionSwitchEditor, true
	case m.goal != nil && m.goal.Status != "completed":
		return MsgSessionSwitchGoal, true
	default:
		return "", false
	}
}

func (m *Model) openSessionPicker() tea.Cmd {
	m.closeSuggest()
	m.sessionOpGen++
	s := &sessionPickerState{generation: m.sessionOpGen, loading: true, scope: sessionScopeCurrent, stage: sessionStageSessions, status: m.text(MsgSessionPickerLoading, nil)}
	m.sessionPicker = s
	m.layout()
	call, gen := m.call, s.generation
	return func() tea.Msg {
		if call == nil {
			return sessionListMsg{generation: gen, err: errors.New("daemon not connected")}
		}
		var out []sessionListItem
		err := call.Call("session.list", map[string]any{}, &out)
		return sessionListMsg{generation: gen, items: out, err: err}
	}
}

func (m *Model) handleSessionList(msg sessionListMsg) {
	s := m.sessionPicker
	if s == nil || s.generation != msg.generation {
		return
	}
	s.loading = false
	if msg.err != nil {
		s.loadError = true
		s.status = m.text(MsgSessionPickerFailed, MessageArgs{"error": msg.err.Error()})
		return
	}
	s.loadError = false
	s.items = nil
	for _, item := range msg.items {
		if item.SessionID != m.sessionID && item.Status != "closed" &&
			cleanWorkspaceRoot(item.WorkspaceRoot) == cleanWorkspaceRoot(m.workspaceRoot) {
			s.items = append(s.items, item)
		}
	}
	sort.SliceStable(s.items, func(i, j int) bool {
		left, right := sessionAttentionRank(s.items[i]), sessionAttentionRank(s.items[j])
		if left != right {
			return left < right
		}
		return s.items[i].UpdatedAt > s.items[j].UpdatedAt
	})
	if len(s.items) == 0 {
		s.status = m.text(MsgSessionPickerEmpty, nil)
	} else {
		s.status = m.text(MsgSessionPickerHelp, nil)
	}
	s.clamp(m.sessionPickerPageHeight())
}

func (m *Model) openWorkspacePicker() tea.Cmd {
	s := m.sessionPicker
	if s == nil {
		return nil
	}
	m.sessionOpGen++
	s.generation = m.sessionOpGen
	s.scope, s.stage = sessionScopeAll, sessionStageWorkspaces
	s.loading, s.loadError = true, false
	s.selected, s.scroll = 0, 0
	s.status = m.text(MsgSessionWorkspaceLoading, nil)
	list, gen := m.listWorkspaces, s.generation
	return func() tea.Msg {
		if list == nil {
			return workspaceListMsg{generation: gen, err: errors.New("workspace navigation is unavailable")}
		}
		items, err := list()
		return workspaceListMsg{generation: gen, items: items, err: err}
	}
}

func (m *Model) handleWorkspaceList(msg workspaceListMsg) {
	s := m.sessionPicker
	if s == nil || s.generation != msg.generation || s.stage != sessionStageWorkspaces {
		return
	}
	s.loading = false
	if msg.err != nil {
		s.loadError = true
		s.status = m.text(MsgSessionWorkspaceFailed, MessageArgs{"error": msg.err.Error()})
		return
	}
	s.workspaces = msg.items
	s.status = m.text(MsgSessionWorkspaceHelp, nil)
	if len(s.workspaces) == 0 {
		s.status = m.text(MsgSessionWorkspaceEmpty, nil)
	}
	s.clamp(m.sessionPickerPageHeight())
}

func (m *Model) loadWorkspaceSessions(item WorkspaceListItem) tea.Cmd {
	s := m.sessionPicker
	if s == nil {
		return nil
	}
	if item.Error != "" {
		s.status = item.Error
		return nil
	}
	m.sessionOpGen++
	s.generation = m.sessionOpGen
	s.loading, s.loadError = true, false
	s.status = m.text(MsgSessionWorkspaceConnecting, MessageArgs{"workspace": workspaceDisplayName(item)})
	load, root, gen := m.loadWorkspace, item.Root, s.generation
	return func() tea.Msg {
		if load == nil {
			return workspaceSessionsMsg{generation: gen, err: errors.New("workspace navigation is unavailable")}
		}
		destination, err := load(root)
		return workspaceSessionsMsg{generation: gen, destination: destination, err: err}
	}
}

func (m *Model) handleWorkspaceSessions(msg workspaceSessionsMsg) {
	s := m.sessionPicker
	if s == nil || s.generation != msg.generation {
		return
	}
	s.loading = false
	if msg.err != nil {
		s.loadError = true
		s.status = m.text(MsgSessionWorkspaceFailed, MessageArgs{"error": msg.err.Error()})
		return
	}
	s.stage = sessionStageSessions
	s.destination = cloneConnectionTarget(msg.destination.Target)
	s.items = nil
	for _, item := range msg.destination.Sessions {
		if item.Status != "closed" {
			s.items = append(s.items, item)
		}
	}
	s.selected, s.scroll = 0, 0
	s.status = m.text(MsgSessionPickerHelpAll, nil)
	if len(s.items) == 0 {
		s.status = m.text(MsgSessionPickerEmpty, nil)
	}
	s.clamp(m.sessionPickerPageHeight())
}

func workspaceDisplayName(item WorkspaceListItem) string {
	if strings.TrimSpace(item.Name) != "" {
		return item.Name
	}
	name := filepath.Base(filepath.Clean(item.Root))
	if name == "." || name == string(filepath.Separator) {
		return item.Root
	}
	return name
}

func sessionAttentionRank(item sessionListItem) int {
	switch item.Continuity.Recovery.Disposition {
	case "blocked", "review_required":
		return 0
	case "resume_checkpoint", "retry", "continue":
		return 1
	}
	if item.TaskStatus == "running" || item.TaskStatus == "waiting_approval" {
		return 2
	}
	return 3
}

// Reserve room for the selected session's recovery evidence. The list remains
// navigable in short terminals while the recommended action stays visible.
func (m *Model) sessionPickerPageHeight() int { return maxInt(m.height-16, 1) }
func (s *sessionPickerState) itemCount() int {
	if s.stage == sessionStageWorkspaces {
		return len(s.workspaces)
	}
	return len(s.items)
}
func (s *sessionPickerState) clamp(page int) {
	count := s.itemCount()
	if count == 0 {
		s.selected, s.scroll = 0, 0
		return
	}
	s.selected = clampInt(s.selected, 0, count-1)
	if s.selected < s.scroll {
		s.scroll = s.selected
	}
	if s.selected >= s.scroll+page {
		s.scroll = s.selected - page + 1
	}
	s.scroll = clampInt(s.scroll, 0, maxInt(count-page, 0))
}

func (m *Model) sessionPickerKey(key string) (tea.Cmd, bool) {
	s := m.sessionPicker
	if s == nil {
		return nil, false
	}
	switch key {
	case "esc":
		if m.sessionActionPending != "" || m.pendingSessionID != "" {
			s.status = m.text(MsgSessionActionResolving, nil)
			return nil, true
		}
		if s.scope == sessionScopeAll && s.stage == sessionStageSessions {
			s.stage = sessionStageWorkspaces
			s.items = nil
			s.selected, s.scroll = 0, 0
			s.status = m.text(MsgSessionWorkspaceHelp, nil)
			return nil, true
		}
		m.sessionPicker = nil
		m.layout()
		return m.resumeQueuedAfterTransient(), true
	case "tab":
		if s.loading || m.pendingSessionID != "" {
			return nil, true
		}
		if s.scope == sessionScopeCurrent {
			return m.openWorkspacePicker(), true
		}
		return m.openSessionPicker(), true
	case "r":
		if m.pendingSessionID != "" {
			if m.switchSession != nil {
				_ = m.switchSession(m.pendingSessionID)
			}
			s.loading = true
			s.status = m.text(MsgSessionSwitching, MessageArgs{"session": m.pendingSessionID})
			return nil, true
		}
		if !s.loading && (s.loadError || len(s.items) == 0) {
			return m.openSessionPicker(), true
		}
	case "b":
		if m.pendingSessionID != "" && m.previousSessionID != "" {
			target := m.previousSessionID
			if err := m.submissions.transfer(target); err != nil {
				s.status = m.text(MsgSessionSwitchLeaseBlocked, MessageArgs{"error": err.Error()})
				return nil, true
			}
			m.pendingSessionID = target
			m.pendingWorkspaceRoot = m.previousWorkspaceRoot
			if m.switchSession != nil {
				_ = m.switchSession(target)
			}
			s.loading = true
			s.status = m.text(MsgSessionSwitching, MessageArgs{"session": target})
			return nil, true
		}
	case "up", "k":
		s.selected--
	case "down", "j":
		s.selected++
	case "enter":
		if s.loading || s.itemCount() == 0 {
			return nil, true
		}
		if s.stage == sessionStageWorkspaces {
			return m.loadWorkspaceSessions(s.workspaces[s.selected]), true
		}
		if s.scope == sessionScopeAll {
			return m.resumeWorkspaceSession(s.items[s.selected]), true
		}
		return m.resumeSession(s.items[s.selected].SessionID), true
	}
	s.clamp(m.sessionPickerPageHeight())
	return nil, true
}

func (m *Model) sessionPickerView() string {
	s := m.sessionPicker
	if s == nil {
		return ""
	}
	width := maxInt(m.width-4, 1)
	title := m.text(MsgSessionPickerTitle, nil)
	if s.scope == sessionScopeAll {
		title = m.text(MsgSessionWorkspaceTitle, nil)
	}
	lines := []string{m.th.Style(theme.RoleTitle).Render(title), ""}
	if s.loading {
		lines = append(lines, s.status)
	} else {
		page := m.sessionPickerPageHeight()
		s.clamp(page)
		end := minInt(s.scroll+page, s.itemCount())
		if s.stage == sessionStageWorkspaces {
			for i := s.scroll; i < end; i++ {
				item := s.workspaces[i]
				prefix := "  "
				if i == s.selected {
					prefix = "> "
				}
				label := workspaceDisplayName(item) + "  " + item.Root
				if item.Current {
					label += "  " + m.text(MsgSessionWorkspaceCurrent, nil)
				}
				if item.Error != "" {
					label += "  " + m.text(MsgSessionWorkspaceInvalid, nil)
				}
				line := fitRenderedLine(prefix+label, width)
				if i == s.selected {
					line = m.th.Style(theme.RoleTitle).Render(line)
				}
				lines = append(lines, line)
			}
			lines = append(lines, "", fitRenderedLine(s.status, width))
			return renderSessionPickerBox(m, lines)
		}
		for i := s.scroll; i < end; i++ {
			it := s.items[i]
			prefix := "  "
			if i == s.selected {
				prefix = "> "
			}
			name := it.Name
			if name == "" {
				name = it.SessionID
			}
			workspace := filepath.Base(filepath.Clean(it.WorkspaceRoot))
			label := name + "  " + m.sessionStatusLabel(it.Status)
			if workspace != "." && workspace != string(filepath.Separator) && workspace != "" {
				label += "  " + workspace
			}
			if age := m.sessionAge(it.CreatedAt); age != "" {
				label += "  " + age
			}
			if it.TaskStatus != "" {
				label += "  " + m.taskStatusText(normalizeTaskStatus(it.TaskStatus))
			}
			if width >= 40 && it.ParentID != "" {
				label += "  " + m.text(MsgSessionPickerForkOf, MessageArgs{"parent": it.ParentID})
				if it.ForkedFromTaskID != "" {
					label += " " + m.text(MsgSessionPickerForkTask, MessageArgs{"task": it.ForkedFromTaskID})
				}
			}
			line := fitRenderedLine(prefix+label, width)
			if i == s.selected {
				line = m.th.Style(theme.RoleTitle).Render(line)
			}
			lines = append(lines, line)
		}
		if len(s.items) > 0 {
			lines = append(lines, "")
			lines = append(lines, m.sessionContinuityDetail(s.items[s.selected], width)...)
		}
		lines = append(lines, "", fitRenderedLine(s.status, width))
	}
	return renderSessionPickerBox(m, lines)
}

func renderSessionPickerBox(m *Model, lines []string) string {
	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1)
	if c := m.th.Color(theme.RoleTitle); c != nil {
		style = style.BorderForeground(c)
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (m *Model) sessionContinuityDetail(it sessionListItem, width int) []string {
	c := it.Continuity
	lines := []string{fitRenderedLine(m.text(MsgSessionPickerEvidence, MessageArgs{
		"outcome": c.Outcome, "progress": c.Progress, "recovery": c.Recovery.Disposition,
	}), width)}
	if c.Interruption != nil {
		billing := ""
		if c.Interruption.BillingUncertain {
			billing = m.text(MsgSessionPickerBillingUncertain, nil)
		}
		lines = append(lines, fitRenderedLine(m.text(MsgSessionPickerInterruption, MessageArgs{
			"kind": c.Interruption.Kind, "certainty": c.Interruption.Certainty, "billing": billing,
		}), width))
	}
	proofs := make([]string, 0, len(c.Recovery.Proofs))
	for _, name := range []string{"checkpoint", "effect_replay", "workspace_anchor", "external_effects"} {
		if passed, ok := c.Recovery.Proofs[name]; ok {
			mark := "x"
			if passed {
				mark = "+"
			}
			proofs = append(proofs, mark+" "+name)
		}
	}
	if len(proofs) > 0 {
		lines = append(lines, fitRenderedLine(m.text(MsgSessionPickerProofs, MessageArgs{"proofs": strings.Join(proofs, " · ")}), width))
	}
	if c.Recovery.CheckpointID != "" {
		lines = append(lines, fitRenderedLine(m.text(MsgSessionPickerCheckpoint, MessageArgs{"checkpoint": c.Recovery.CheckpointID, "generation": c.RecoveryGeneration, "revision": it.TaskRevision}), width))
	}
	if strings.TrimSpace(c.Recovery.Reason) != "" {
		lines = append(lines, fitRenderedLine(m.text(MsgSessionPickerReason, MessageArgs{"reason": c.Recovery.Reason}), width))
	}
	return append(lines, fitRenderedLine(m.text(MsgSessionPickerRecommended, MessageArgs{"action": m.sessionRecoveryAction(it)}), width))
}

func (m *Model) sessionRecoveryAction(it sessionListItem) string {
	switch it.Continuity.Recovery.Disposition {
	case "blocked", "review_required":
		return m.text(MsgSessionPickerActionReview, nil)
	case "resume_checkpoint":
		return m.text(MsgSessionPickerActionAuto, nil)
	case "retry":
		return m.text(MsgSessionPickerActionRetry, nil)
	case "continue":
		return m.text(MsgSessionPickerActionContinue, nil)
	default:
		return m.text(MsgSessionPickerActionInspect, nil)
	}
}

func (m *Model) beginSessionAction(action, method string, params map[string]any) tea.Cmd {
	if m.sessionActionPending != "" || m.pendingSessionID != "" {
		m.push(m.text(MsgSessionActionResolving, nil))
		return nil
	}
	if blocker, blocked := m.sessionSwitchBlocker(); blocked {
		m.push(m.text(MsgSessionSwitchBlocked, MessageArgs{"reason": m.text(blocker, nil)}))
		return nil
	}
	m.sessionOpGen++
	m.sessionActionPending = action
	gen := m.sessionOpGen
	call := m.call
	return func() tea.Msg {
		if call == nil {
			return sessionActionMsg{generation: gen, action: action, err: errors.New("daemon not connected")}
		}
		var out sessionListItem
		err := call.Call(method, params, &out)
		return sessionActionMsg{generation: gen, action: action, session: out, err: err}
	}
}
func (m *Model) newSession() tea.Cmd {
	return m.newSessionWithProfile("safe-edit")
}
func (m *Model) newSessionWithProfile(profile string) tea.Cmd {
	return m.beginSessionAction("new", "session.create", map[string]any{"workspace_root": m.workspaceRoot, "profile": profile})
}
func (m *Model) forkSession(taskID string) tea.Cmd {
	p := map[string]any{"session_id": m.sessionID}
	if taskID != "" {
		p["last_task_id"] = taskID
	}
	return m.beginSessionAction("fork", "session.fork", p)
}
func (m *Model) resumeSession(id string) tea.Cmd {
	return m.beginSessionAction("resume", "session.resume", map[string]any{"session_id": id})
}

func (m *Model) resumeWorkspaceSession(item sessionListItem) tea.Cmd {
	if m.sessionActionPending != "" || m.pendingSessionID != "" {
		return nil
	}
	if blocker, blocked := m.sessionSwitchBlocker(); blocked {
		if m.sessionPicker != nil {
			m.sessionPicker.status = m.text(MsgSessionSwitchBlocked, MessageArgs{"reason": m.text(blocker, nil)})
		}
		return nil
	}
	if m.sessionPicker == nil || m.resumeWorkspace == nil || m.prepareTarget == nil || m.commitTarget == nil {
		return nil
	}
	m.sessionOpGen++
	gen := m.sessionOpGen
	m.sessionPicker.generation = gen
	m.sessionPicker.loading = true
	m.sessionPicker.status = m.text(MsgSessionSwitching, MessageArgs{"session": item.SessionID})
	base := cloneConnectionTarget(m.sessionPicker.destination)
	resume, prepare := m.resumeWorkspace, m.prepareTarget
	return func() tea.Msg {
		target, err := resume(base, item.SessionID)
		if err != nil {
			return workspaceResumeMsg{generation: gen, err: err}
		}
		journal := newSubmissionJournal(target.StateDir, target.WorkspaceRoot)
		if err := journal.acquire(target.SessionID); err != nil {
			return workspaceResumeMsg{generation: gen, err: err}
		}
		token, err := prepare(target)
		if err != nil {
			journal.close()
			return workspaceResumeMsg{generation: gen, err: err}
		}
		return workspaceResumeMsg{generation: gen, target: target, journal: journal, token: token}
	}
}

func (m *Model) handleWorkspaceResume(msg workspaceResumeMsg) {
	if msg.generation != m.sessionOpGen {
		if msg.token != 0 && m.abortTarget != nil {
			m.abortTarget(msg.token)
		}
		msg.journal.close()
		return
	}
	s := m.sessionPicker
	if msg.err != nil {
		if s != nil {
			s.loading = false
			s.loadError = true
			s.status = m.text(MsgSessionSwitchRecover, MessageArgs{"error": msg.err.Error()})
		}
		return
	}
	if blocker, blocked := m.sessionSwitchBlocker(); blocked {
		if m.abortTarget != nil {
			m.abortTarget(msg.token)
		}
		msg.journal.close()
		if s != nil {
			s.loading = false
			s.status = m.text(MsgSessionSwitchBlocked, MessageArgs{"reason": m.text(blocker, nil)})
		}
		return
	}
	if err := m.commitTarget(msg.token); err != nil {
		if m.abortTarget != nil {
			m.abortTarget(msg.token)
		}
		msg.journal.close()
		if s != nil {
			s.loading = false
			s.status = m.text(MsgSessionSwitchRecover, MessageArgs{"error": err.Error()})
		}
		return
	}
	target := cloneConnectionTarget(msg.target)
	m.pendingTarget = &target
	m.pendingSubmissions = &msg.journal
	m.pendingPreparedToken = msg.token
	m.previousSessionID, m.previousWorkspaceRoot = m.sessionID, m.workspaceRoot
	m.pendingSessionID = target.SessionID
	m.pendingWorkspaceRoot = target.WorkspaceRoot
}

func (m *Model) handleSessionAction(msg sessionActionMsg) {
	if msg.generation != m.sessionOpGen {
		return
	}
	m.sessionActionPending = ""
	if msg.err != nil {
		m.pendingSideQuestion = ""
		if msg.action == "fork" {
			m.sidePane = nil
		}
		m.push(m.text(MsgSessionActionFailed, MessageArgs{"error": msg.err.Error()}))
		return
	}
	if msg.session.SessionID == "" {
		m.push(m.text(MsgSessionActionInvalid, nil))
		return
	}
	if m.switchSession == nil {
		m.push(m.text(MsgSessionSwitchUnavailable, nil))
		return
	}
	oldSession := m.sessionID
	m.previousSessionID, m.previousWorkspaceRoot = oldSession, m.workspaceRoot
	if err := m.submissions.transfer(msg.session.SessionID); err != nil {
		m.push(m.text(MsgSessionSwitchLeaseBlocked, MessageArgs{"error": err.Error()}))
		return
	}
	if err := m.switchSession(msg.session.SessionID); err != nil {
		_ = m.submissions.transfer(oldSession)
		m.push(m.text(MsgSessionSwitchFailed, MessageArgs{"error": err.Error()}))
		return
	}
	m.pendingSessionID = msg.session.SessionID
	m.pendingWorkspaceRoot = msg.session.WorkspaceRoot
	if msg.action == "fork" && m.sidePane != nil {
		m.noteSideSession(msg.session.SessionID)
	}
	if m.sessionPicker == nil {
		m.sessionPicker = &sessionPickerState{generation: m.sessionOpGen}
	}
	m.sessionPicker.loading = true
	m.sessionPicker.status = m.text(MsgSessionSwitching, MessageArgs{"session": msg.session.SessionID})
	m.push(m.text(MsgSessionSwitching, MessageArgs{"session": msg.session.SessionID}))
	m.layout()
}
