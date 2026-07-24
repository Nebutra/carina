package tui

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

const backtrackPrimeWindow = 500 * time.Millisecond

type backtrackPhase uint8

const (
	backtrackInactive backtrackPhase = iota
	backtrackPrimed
	backtrackSelecting
	backtrackSwitching
)

type backtrackState struct {
	Phase                backtrackPhase
	Generation           uint64
	PrimedAt             time.Time
	SourceSessionID      string
	SourceGeneration     uint64
	SelectedKey          string
	PendingDraft         promptDraft
	DestinationSessionID string
}

type backtrackPrimeExpiredMsg struct {
	Generation uint64
}

func (m *Model) primeBacktrack() tea.Cmd {
	if len(m.eligibleBacktrackTargets()) == 0 {
		m.setOperationalNoticeKind("backtrack", m.text(MsgBacktrackNoPrompts, nil), theme.RoleMuted)
		return nil
	}
	m.clearBacktrackSelection()
	m.backtrack.Generation++
	m.backtrack.Phase = backtrackPrimed
	m.backtrack.PrimedAt = m.now()
	m.backtrack.SourceSessionID = m.sessionID
	m.backtrack.SourceGeneration = m.sessionGeneration
	m.backtrack.SelectedKey = ""
	m.backtrack.PendingDraft = promptDraft{}
	m.backtrack.DestinationSessionID = ""
	m.setOperationalNoticeKind("backtrack", m.text(MsgUpdateRewindAgain, MessageArgs{
		"rewind": primaryKeyLabel(m.keys.keys(KeyContextChat, ActionChatRewind)),
	}), theme.RoleMuted)
	generation := m.backtrack.Generation
	return tea.Tick(backtrackPrimeWindow, func(time.Time) tea.Msg {
		return backtrackPrimeExpiredMsg{Generation: generation}
	})
}

func (m *Model) handleBacktrackPrimeExpired(msg backtrackPrimeExpiredMsg) {
	if m.backtrack.Phase != backtrackPrimed || msg.Generation != m.backtrack.Generation {
		return
	}
	m.cancelBacktrack()
}

func (m *Model) activateBacktrackSelection() bool {
	if m.backtrack.Phase != backtrackPrimed || m.now().Sub(m.backtrack.PrimedAt) > backtrackPrimeWindow ||
		m.backtrack.SourceSessionID != m.sessionID || m.backtrack.SourceGeneration != m.sessionGeneration {
		m.cancelBacktrack()
		return false
	}
	targets := m.eligibleBacktrackTargets()
	if len(targets) == 0 {
		m.cancelBacktrack()
		return false
	}
	m.backtrack.Phase = backtrackSelecting
	m.backtrack.SelectedKey = targets[len(targets)-1].EntryKey
	m.clearOperationalNoticeKind("backtrack")
	m.syncBacktrackSelection()
	return true
}

func (m *Model) handleBacktrackKey(key string) (tea.Cmd, bool) {
	if m.backtrack.Phase != backtrackSelecting {
		return nil, false
	}
	if !m.backtrackSourceCurrent() {
		m.cancelBacktrack()
		return nil, true
	}
	switch canonicalKey(key) {
	case "esc", "up", "left":
		m.moveBacktrackSelection(-1)
		return nil, true
	case "down", "right":
		m.moveBacktrackSelection(1)
		return nil, true
	case "enter":
		return m.beginBacktrackBranch(), true
	case "ctrl+c", "q":
		m.cancelBacktrack()
		return nil, true
	}
	if printableBacktrackKey(key) {
		m.cancelBacktrack()
		return nil, false
	}
	return nil, true
}

func printableBacktrackKey(key string) bool {
	return len([]rune(key)) == 1 && !strings.HasPrefix(key, "ctrl+") && !strings.HasPrefix(key, "alt+")
}

func (m *Model) eligibleBacktrackTargets() []userBacktrackTarget {
	all := m.tr.userBacktrackTargets()
	targets := make([]userBacktrackTarget, 0, len(all))
	for _, target := range all {
		if target.Branchable && !target.Steer {
			targets = append(targets, target)
		}
	}
	return targets
}

func (m *Model) moveBacktrackSelection(delta int) {
	targets := m.eligibleBacktrackTargets()
	if len(targets) == 0 {
		m.cancelBacktrack()
		return
	}
	selected := len(targets) - 1
	for i := range targets {
		if targets[i].EntryKey == m.backtrack.SelectedKey {
			selected = i
			break
		}
	}
	selected = maxInt(0, minInt(len(targets)-1, selected+delta))
	m.backtrack.SelectedKey = targets[selected].EntryKey
	m.syncBacktrackSelection()
}

func (m *Model) syncBacktrackSelection() {
	for i := range m.tr.entries {
		p := m.tr.entries[i].presentation
		if p != nil {
			p.Selected = m.backtrack.Phase == backtrackSelecting && m.tr.entries[i].key == m.backtrack.SelectedKey
		}
	}
	m.keepBacktrackSelectionVisible()
	m.layout()
}

func (m *Model) clearBacktrackSelection() {
	for i := range m.tr.entries {
		if p := m.tr.entries[i].presentation; p != nil {
			p.Selected = false
		}
	}
}

func (m *Model) keepBacktrackSelectionVisible() {
	start := 0
	selectedStart, selectedEnd := -1, -1
	for i := range m.tr.entries {
		entry := &m.tr.entries[i]
		end := start + len(entry.lines)
		if entry.key == m.backtrack.SelectedKey {
			selectedStart, selectedEnd = start, end
			break
		}
		start = end
	}
	if selectedStart < 0 {
		return
	}
	height := maxInt(m.vp.VisibleLineCount(), 1)
	top := m.vp.YOffset()
	switch {
	case selectedStart < top:
		m.vp.SetYOffset(selectedStart)
	case selectedEnd > top+height:
		m.vp.SetYOffset(maxInt(selectedEnd-height, 0))
	}
	m.followTail = m.vp.AtBottom()
}

func (m *Model) beginBacktrackBranch() tea.Cmd {
	target, ok := m.selectedBacktrackTarget()
	if !ok || !m.backtrackSourceCurrent() || m.sessionActionPending != "" || m.pendingSessionID != "" {
		m.cancelBacktrack()
		return nil
	}
	m.clearBacktrackSelection()
	m.backtrack.Phase = backtrackSwitching
	m.backtrack.PendingDraft = backtrackRestorableDraft(target.Draft)
	m.backtrack.DestinationSessionID = ""
	m.setOperationalNoticeKind("backtrack", m.text(MsgBacktrackSwitching, nil), theme.RoleInfo)
	m.layout()
	var cmd tea.Cmd
	if target.PreviousTaskID == "" {
		cmd = m.newSession()
	} else {
		cmd = m.forkSession(target.PreviousTaskID)
	}
	if cmd == nil {
		m.restoreBacktrackDraftAfterFailure()
	}
	return cmd
}

func (m *Model) beginBacktrackEntryEdit(entryKey string) tea.Cmd {
	if m.inFlightTaskID != "" || m.retrySubmission != nil || !draftEmpty(m.currentDraft()) ||
		m.sessionActionPending != "" || m.pendingSessionID != "" {
		m.setOperationalNoticeKind("backtrack", m.text(MsgBacktrackBusy, nil), theme.RoleWarning)
		return nil
	}
	targetFound := false
	for _, target := range m.eligibleBacktrackTargets() {
		if target.EntryKey == entryKey {
			targetFound = true
			break
		}
	}
	if !targetFound {
		return nil
	}
	m.backtrack.Generation++
	m.backtrack.Phase = backtrackSelecting
	m.backtrack.SourceSessionID = m.sessionID
	m.backtrack.SourceGeneration = m.sessionGeneration
	m.backtrack.SelectedKey = entryKey
	m.syncBacktrackSelection()
	return m.beginBacktrackBranch()
}

func backtrackRestorableDraft(draft promptDraft) promptDraft {
	draft = cloneDraft(draft)
	// Media references are scoped to the source session. Until the event stores
	// a reproducible local source or cross-session copy contract, restoring them
	// would display attachments that cannot be submitted safely in the branch.
	attachments := draft.Attachments[:0]
	for i := range draft.Attachments {
		attachment := draft.Attachments[i]
		if attachment.Digest == "" || (len(attachment.Data) == 0 && attachment.SourcePath == "") {
			continue
		}
		attachment.Ref = nil
		attachments = append(attachments, attachment)
	}
	draft.Attachments = attachments
	return draft
}

func (m *Model) selectedBacktrackTarget() (userBacktrackTarget, bool) {
	for _, target := range m.eligibleBacktrackTargets() {
		if target.EntryKey == m.backtrack.SelectedKey {
			return target, true
		}
	}
	return userBacktrackTarget{}, false
}

func (m *Model) backtrackSourceCurrent() bool {
	return m.backtrack.SourceSessionID == m.sessionID && m.backtrack.SourceGeneration == m.sessionGeneration
}

func (m *Model) noteBacktrackDestination(sessionID string) {
	if m.backtrack.Phase == backtrackSwitching {
		m.backtrack.DestinationSessionID = strings.TrimSpace(sessionID)
	}
}

func (m *Model) restoreBacktrackDraftAfterReady(sessionID string) bool {
	if m.backtrack.Phase != backtrackSwitching || sessionID == "" || sessionID != m.backtrack.DestinationSessionID {
		return false
	}
	draft := cloneDraft(m.backtrack.PendingDraft)
	m.cancelBacktrack()
	m.restoreDraft(draft)
	m.input.Focus()
	return true
}

func (m *Model) restoreBacktrackDraftAfterFailure() bool {
	if m.backtrack.Phase != backtrackSwitching {
		m.cancelBacktrack()
		return false
	}
	draft := cloneDraft(m.backtrack.PendingDraft)
	m.cancelBacktrack()
	m.restoreDraft(draft)
	m.input.Focus()
	return true
}

func (m *Model) cancelBacktrack() {
	generation := m.backtrack.Generation + 1
	m.clearBacktrackSelection()
	m.backtrack = backtrackState{Generation: generation}
	m.clearOperationalNoticeKind("backtrack")
	m.layout()
}
