package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-shellwords"

	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui/mathimage"
	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

// ctrlCWindow is the double-press window for the cascading interrupt (P1.4):
// the first Ctrl-C cancels the in-flight task, a second within this window
// exits.
const ctrlCWindow = 2 * time.Second

// Update implements tea.Model and flushes renderer-owned terminal protocol
// side effects after the state transition that discovered them.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.update(msg)
	raw := mathimage.Drain()
	if raw == "" {
		return model, cmd
	}
	return model, tea.Batch(cmd, tea.Raw(raw))
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	switch msg.(type) {
	case tea.PasteMsg:
		m.lastCtrlC = time.Time{}
		m.ctrlCHint = ""
		m.rewindPrimed = false
		m.clearChord()
	}
	var (
		composerBefore composerSnapshot
		composerKey    tea.KeyPressMsg
		trackComposer  bool
	)

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		mathimage.ResetTransport()
		if m.componentRuntime != nil {
			m.componentRuntime.InvalidateGeometry()
		}
		m.componentFrame = ui.Frame{}
		m.layout()
		return m, tea.Raw(ansi.WindowOp(ansi.RequestCellSizeWinOp))

	case uv.CellSizeEvent:
		if !mathimage.SetCellSize(msg.Width, msg.Height) {
			return m, nil
		}
		// Cell metrics are part of image layout even when the terminal's cell
		// grid width is unchanged, so force presentation and component reflow.
		m.tr.renderedWidth = 0
		if m.componentRuntime != nil {
			m.componentRuntime.InvalidateGeometry()
		}
		m.componentFrame = ui.Frame{}
		m.layout()
		return m, nil

	case tea.BlurMsg:
		m.terminalBlurredNow()
		return m, nil

	case tea.FocusMsg:
		m.terminalFocusedNow()
		return m, nil

	case SessionReadyMsg:
		wasSwitching := m.pendingSessionID != ""
		if m.pendingSessionID != "" && msg.SessionID != m.pendingSessionID {
			return m, nil
		}
		if m.pendingTarget != nil && m.pendingSubmissions != nil && !sameConnectionTarget(msg.Target, *m.pendingTarget) {
			// Session IDs are scoped to a runtime and may collide across state
			// directories. A late readiness signal from the source must not adopt
			// the destination journal/root while retaining the source RPC client.
			return m, nil
		}
		if msg.Generation != 0 && msg.Generation < m.sessionGeneration {
			return m, nil
		}
		crossRuntime := m.pendingTarget != nil && m.pendingSubmissions != nil
		preparedToken := m.pendingPreparedToken
		reconnected := !crossRuntime && m.sessionID == msg.SessionID && m.conn == ConnConnected
		if m.sessionID != "" && (m.sessionID != msg.SessionID || crossRuntime) {
			if !crossRuntime {
				if err := m.submissions.transfer(msg.SessionID); err != nil {
					m.submissionLeaseErr = err
					m.setOperationalNotice(m.text(MsgUpdateReadOnly, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}), theme.RoleError)
					return m, nil
				}
			}
			m.resetSessionProjection()
		}
		m.sessionID = msg.SessionID
		if crossRuntime {
			m.submissions.close()
			m.submissions = *m.pendingSubmissions
			m.pendingSubmissions = nil
			m.socket = m.pendingTarget.Socket
			m.workspaceRoot = m.pendingTarget.WorkspaceRoot
			m.stateDir = m.pendingTarget.StateDir
			m.pendingTarget = nil
			m.pendingPreparedToken = 0
		} else if m.pendingWorkspaceRoot != "" {
			m.workspaceRoot = m.pendingWorkspaceRoot
			m.treeCache, m.treeCacheRoot = nil, ""
			m.treeCacheAt = time.Time{}
		}
		if msg.Target.Socket != "" {
			m.socket = msg.Target.Socket
		}
		if msg.Target.WorkspaceRoot != "" {
			m.workspaceRoot = msg.Target.WorkspaceRoot
		}
		if msg.Target.StateDir != "" {
			m.stateDir = msg.Target.StateDir
		}
		m.treeCache, m.treeCacheRoot = nil, ""
		m.treeCacheAt = time.Time{}
		m.pendingSessionID = ""
		m.pendingWorkspaceRoot = ""
		m.previousSessionID, m.previousWorkspaceRoot = "", ""
		if wasSwitching {
			m.sessionPicker = nil
		}
		if msg.Generation != 0 {
			m.sessionGeneration = msg.Generation
		}
		if crossRuntime && preparedToken != 0 && m.acknowledgeTarget != nil {
			m.acknowledgeTarget(preparedToken)
		}
		m.call = msg.Call
		var firstConnection tea.Cmd
		if !m.firstConnectionSeen {
			m.firstConnectionSeen = true
			if callback := m.onFirstConnection; callback != nil {
				call := msg.Call
				firstConnection = func() tea.Msg {
					callback(call)
					return nil
				}
			}
		}
		_ = persistLastActiveSession(m.stateDir, m.workspaceRoot, msg.SessionID)
		m.conn = ConnConnected
		m.applyConversation(conversationTransition{Kind: transitionConnected, SessionID: msg.SessionID, EventType: "session.ready"})
		m.attempt = 0
		if reconnected && m.submissionLeaseErr == nil {
			return m, m.refreshRuntimeStatus()
		}
		if reconnected {
			// A secondary TUI may already be attached in read-only mode. A fresh
			// readiness signal is its opportunity to acquire a lease released by
			// the former writer, without replaying attach/history initialization.
			m.submissionLeaseErr = m.submissions.acquire(msg.SessionID)
			if m.submissionLeaseErr != nil {
				m.setOperationalNotice(m.text(MsgUpdateReadOnly, MessageArgs{"glyph": glyphFailed(m.th), "error": m.submissionLeaseErr.Error()}), theme.RoleError)
			}
			return m, m.refreshRuntimeStatus()
		}
		m.setOperationalNotice(m.text(MsgUpdateAttached, MessageArgs{"session": msg.SessionID}), theme.RoleMuted)
		if crossRuntime {
			m.submissionLeaseErr = nil
		} else {
			m.submissionLeaseErr = m.submissions.acquire(msg.SessionID)
		}
		if m.submissionLeaseErr != nil {
			m.setOperationalNotice(m.text(MsgUpdateReadOnly, MessageArgs{"glyph": glyphFailed(m.th), "error": m.submissionLeaseErr.Error()}), theme.RoleError)
		}
		reconcile := m.restoreSubmissionJournal()
		history := m.loadRecentHistory(msg.Call)
		status := m.refreshRuntimeStatus()
		tick := m.scheduleRuntimeStatusTick()
		side := m.flushPendingSideQuestion()
		cmds := []tea.Cmd{firstConnection, history, status, tick, side}
		if reconcile != nil {
			cmds = append([]tea.Cmd{reconcile}, cmds...)
		}
		return m, tea.Batch(cmds...)

	case modelPreferenceMsg:
		if (msg.sessionID != "" && msg.sessionID != m.sessionID) || (msg.generation != 0 && msg.generation != m.sessionGeneration) {
			return m, nil
		}
		m.handleModelPreference(msg)
		if !msg.loaded && msg.err == nil {
			return m, m.refreshRuntimeStatus()
		}
		return m, nil
	case sessionListMsg:
		m.handleSessionList(msg)
		m.layout()
		return m, nil
	case workspaceListMsg:
		m.handleWorkspaceList(msg)
		m.layout()
		return m, nil
	case workspaceSessionsMsg:
		m.handleWorkspaceSessions(msg)
		m.layout()
		return m, nil
	case workspaceResumeMsg:
		m.handleWorkspaceResume(msg)
		m.layout()
		return m, nil
	case sessionActionMsg:
		m.handleSessionAction(msg)
		return m, nil
	case sessionRenameMsg:
		m.handleSessionRename(msg)
		return m, nil

	case TaskActiveMsg:
		if m.pendingSessionID != "" {
			return m, nil
		}
		if msg.Generation != 0 && (msg.SessionID != m.sessionID || msg.Generation != m.sessionGeneration) {
			return m, nil
		}
		if msg.TaskID != "" && msg.TaskID != m.inFlightTaskID {
			if node := m.tasks.nodes[msg.TaskID]; node != nil && terminalTaskStatus(node.Status) {
				return m, nil
			}
			m.inFlightTaskID = msg.TaskID
			m.applyConversation(conversationTransition{Kind: transitionRunning, TaskID: msg.TaskID, EventType: "task.active", Status: "running"})
			m.tasks.setTask(msg.TaskID, "running")
			m.setOperationalNotice(m.text(MsgUpdateActiveRestored, MessageArgs{"task": msg.TaskID}), theme.RoleInfo)
			m.layout()
		}
		return m, nil

	case ConnLostMsg:
		if m.pendingSessionID != "" {
			if msg.SessionID == m.pendingSessionID && msg.Generation == m.sessionGeneration+1 {
				if m.sessionPicker == nil {
					m.sessionPicker = &sessionPickerState{generation: m.sessionOpGen}
				}
				m.sessionPicker.loading = false
				m.sessionPicker.loadError = true
				m.sessionPicker.status = m.text(MsgSessionSwitchRecover, MessageArgs{"error": msg.Err.Error()})
				m.layout()
			}
			return m, nil
		}
		if msg.Generation != 0 && (msg.SessionID != m.sessionID || msg.Generation != m.sessionGeneration) {
			return m, nil
		}
		m.conn = ConnLost
		m.applyConversation(conversationTransition{Kind: transitionUnavailable, EventType: "connection.lost"})
		m.setOperationalNotice(fmt.Sprintf("%s %s", glyphFailed(m.th), microcopy.Degrade(
			microcopy.DegradeDaemonUnreachable,
			microcopy.Args{"socket": m.socket},
			microcopy.WithLocale(m.locale), microcopy.WithPlain(true),
		)), theme.RoleError)
		return m, nil

	case ReconnectingMsg:
		if m.pendingSessionID != "" || (msg.Generation != 0 && (msg.SessionID != m.sessionID || msg.Generation != m.sessionGeneration)) {
			return m, nil
		}
		m.conn = ConnReconnecting
		m.applyConversation(conversationTransition{Kind: transitionUnavailable, EventType: "connection.reconnecting"})
		m.attempt = msg.Attempt
		return m, nil

	case ConnRestoredMsg:
		if m.pendingSessionID != "" {
			return m, nil
		}
		if msg.Generation != 0 && (msg.SessionID != m.sessionID || msg.Generation != m.sessionGeneration) {
			return m, nil
		}
		m.conn = ConnConnected
		m.applyConversation(conversationTransition{Kind: transitionConnected, SessionID: msg.SessionID, EventType: "connection.restored"})
		m.attempt = 0
		m.setOperationalNotice(m.text(MsgUpdateReconnected, nil), theme.RoleSuccess)
		return m, nil

	case EventMsg:
		if m.pendingSessionID != "" {
			return m, nil
		}
		if msg.Generation != 0 && (msg.SessionID != m.sessionID || msg.Generation != m.sessionGeneration) {
			return m, nil
		}
		return m, tea.Batch(m.handleEvent(msg.Raw), m.artifactPreviewCommands(msg.Raw))

	case artifactPreviewMsg:
		m.handleArtifactPreview(msg)
		return m, nil

	case submissionDoneMsg:
		return m, m.handleSubmissionDone(msg)

	case cancelDoneMsg:
		if msg.err != nil {
			m.setOperationalNotice(m.text(MsgUpdateCancelFailed, MessageArgs{"glyph": glyphFailed(m.th), "task": msg.taskID, "error": msg.err.Error()}), theme.RoleError)
			return m, nil
		}
		cancelledActive := m.inFlightTaskID == msg.taskID
		if cancelledActive {
			m.inFlightTaskID = ""
		}
		m.applyConversation(conversationTransition{Kind: transitionTerminal, TaskID: msg.taskID, EventType: "task.cancel", Status: "cancelled", Outcome: outcomeCancelled})
		m.tasks.setTask(msg.taskID, "cancelled")
		m.setOperationalNotice(m.text(MsgUpdateCancelRecorded, MessageArgs{"task": msg.taskID}), theme.RoleMuted)
		if cancelledActive {
			m.restoreQueuedDrafts("task cancellation")
		}
		m.layout()
		if cancelledActive && m.goal != nil && m.goal.Status == "active" {
			return m, m.goalCall("pause", "goal.pause", map[string]any{})
		}
		return m, nil

	case goalRPCMsg:
		if msg.sessionID != "" && msg.sessionID != m.sessionID {
			return m, nil
		}
		m.handleGoalRPC(msg)
		return m, nil

	case approvalDoneMsg:
		m.handleApprovalDone(msg)
		m.syncConversationLocalActivity("approval.rpc.done")
		m.layout()
		return m, m.maybeSubmitNextQueued()

	case questionDoneMsg:
		m.handleQuestionDone(msg)
		m.syncConversationLocalActivity("question.rpc.done")
		m.layout()
		return m, m.maybeSubmitNextQueued()

	case historyLoadedMsg:
		m.handleHistoryLoaded(msg)
		return m, nil

	case KeymapReloadMsg:
		m.handleKeymapReload(msg)
		return m, nil

	case keymapUpdatedMsg:
		m.handleKeymapUpdated(msg)
		return m, nil

	case checkpointListMsg:
		m.handleCheckpointList(msg)
		return m, nil

	case checkpointPreviewMsg:
		m.handleCheckpointPreview(msg)
		return m, nil

	case checkpointRestoreMsg:
		m.handleCheckpointRestore(msg)
		return m, nil

	case checkpointResumeMsg:
		m.handleCheckpointResume(msg)
		return m, nil

	case modelListMsg:
		m.handleModelList(msg)
		return m, nil

	case chordTimeoutMsg:
		m.handleChordTimeout(msg)
		return m, nil

	case rpcErrMsg:
		m.push(m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.err.Error()}))
		return m, nil
	case surfaceResultMsg:
		if msg.sessionID != "" && msg.sessionID != m.sessionID {
			return m, nil
		}
		m.push(m.th.Style(theme.RoleTitle).Render(msg.label) + "\n" + msg.text)
		return m, m.maybeSubmitNextQueued()

	case canonicalSurfaceMsg:
		m.handleCanonicalSurface(msg)
		return m, m.maybeSubmitNextQueued()

	case operationalSurfaceMsg:
		if msg.sessionID != "" && msg.sessionID != m.sessionID {
			return m, nil
		}
		m.handleOperationalSurface(msg)
		return m, m.maybeSubmitNextQueued()
	case runtimeStatusMsg:
		return m, m.handleRuntimeStatus(msg)
	case alwaysApproveMsg:
		m.handleAlwaysApprove(msg)
		return m, m.refreshRuntimeStatus()
	case planReviewDoneMsg:
		m.handlePlanReviewDone(msg)
		return m, m.refreshRuntimeStatus()
	case runtimeStatusTickMsg:
		if msg.generation != 0 && msg.generation != m.sessionGeneration {
			return m, nil
		}
		if m.sessionID == "" || m.call == nil {
			return m, nil
		}
		return m, tea.Batch(m.refreshRuntimeStatus(), m.scheduleRuntimeStatusTick())
	case commitPromptReadyMsg:
		if msg.sessionID != "" && msg.sessionID != m.sessionID {
			return m, nil
		}
		return m, m.beginSubmissionSourceWithIntent(submissionTask, "", promptDraft{Text: msg.prompt}, false, false)
	case tasksScheduleMsg:
		m.handleTasksSchedule(msg)
		return m, nil
	case dynamicSlashResolvedMsg:
		return m, m.handleDynamicSlash(msg)

	case workspaceDiffMsg:
		m.handleWorkspaceDiff(msg)
		return m, nil

	case externalEditorDoneMsg:
		return m, m.handleExternalEditorDone(msg)

	case clipboardDoneMsg:
		m.handleClipboardDone(msg)
		return m, m.maybeSubmitNextQueued()

	case attachmentLoadMsg:
		return m, m.handleAttachmentLoad(msg)

	case attachmentUploadMsg:
		return m, m.handleAttachmentUpload(msg)

	case attachmentGraphicsFlushMsg:
		return m, nil

	case suggestDebounceMsg:
		return m, m.handleSuggestDebounce(msg)

	case suggestResultMsg:
		m.handleSuggestResult(msg)
		return m, nil

	case modeChangedMsg:
		if msg.sessionID != "" && msg.sessionID != m.sessionID {
			return m, nil
		}
		if msg.err != nil {
			m.push(m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.err.Error()}))
			return m, nil
		}
		m.mode = msg.mode
		m.push(m.text(MsgUpdateMode, MessageArgs{"mode": msg.mode}))
		m.layout()
		refresh := m.refreshRuntimeStatus()
		if strings.TrimSpace(msg.followUpPrompt) != "" {
			return m, tea.Batch(refresh, m.beginSubmissionSourceWithIntent(submissionTask, "", promptDraft{Text: msg.followUpPrompt}, false, false))
		}
		return m, tea.Batch(refresh, m.maybeSubmitNextQueued())

	case loopResultMsg:
		if msg.sessionID != "" && msg.sessionID != m.sessionID {
			return m, nil
		}
		m.handleLoopResult(msg)
		return m, m.maybeSubmitNextQueued()

	case tea.PasteMsg:
		m.pasteBurst.reset()
		if m.question != nil {
			if len(m.question.Options) == 0 && !m.question.Resolving {
				m.appendQuestionText(msg.Content)
			}
			return m, nil
		}
		// Governance overlays exclusively own input while open, including while
		// their RPC is resolving. Never let a terminal paste mutate the hidden
		// composer behind the modal.
		if m.approval != nil || m.editor != nil ||
			m.helpOpen || m.transcriptPager != nil || m.checkpointPicker != nil || m.modelPicker != nil || m.sessionPicker != nil || m.keymapEditor != nil {
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
		if m.submitting != nil && msg.Content != "" && !submissionHasIndependentComposer(m.submitting) {
			m.beginSubmissionTypeAhead()
		}
		before := m.composerSnapshot()
		pasteCount := len(m.pendingPaste)
		cmd := m.handlePaste(msg)
		if len(m.pendingPaste) > pasteCount {
			m.composerExternalMutation()
		} else {
			m.recordComposerEdit(before, composerEditPaste)
		}
		return m, cmd

	case tea.MouseMotionMsg:
		if cmd, handled := m.dispatchGovernanceMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchPrimaryOverlayMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchNavigatorMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchOperationalMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchAttachmentMouse(msg); handled {
			return m, cmd
		}
		return m, nil

	case tea.MouseClickMsg:
		if cmd, handled := m.dispatchGovernanceMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchPrimaryOverlayMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchNavigatorMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchOperationalMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchAttachmentMouse(msg); handled {
			return m, cmd
		}
		return m, nil

	case tea.MouseReleaseMsg:
		if cmd, handled := m.dispatchGovernanceMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchPrimaryOverlayMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchNavigatorMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchOperationalMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchAttachmentMouse(msg); handled {
			return m, cmd
		}
		return m, nil

	case tea.MouseWheelMsg:
		if cmd, handled := m.dispatchGovernanceMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchPrimaryOverlayMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchNavigatorMouse(msg); handled {
			return m, cmd
		}
		if cmd, handled := m.dispatchOperationalMouse(msg); handled {
			return m, cmd
		}
		return m, m.handleMouseWheel(msg)

	case tea.KeyPressMsg:
		if m.editor != nil {
			m.pasteBurst.reset()
			m.maintainConfirmationStateForKey(msg.String())
			return m, nil
		}
		resolved, chordCmd, chordConsumed := m.resolveChordKey(msg.String())
		if chordConsumed {
			m.pasteBurst.reset()
			return m, chordCmd
		}
		m.maintainConfirmationStateForKey(resolved)
		if resolved != msg.String() {
			m.pasteBurst.reset()
			if cmd, handled := m.handleKey(resolved); handled {
				return m, cmd
			}
			before := m.composerSnapshot()
			synthetic := tea.KeyPressMsg{Text: resolved}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(synthetic)
			m.snapVerticalGraphemeEditorKey(resolved)
			m.layout()
			m.recordComposerEdit(before, composerEditOther)
			return m, tea.Batch(cmd, m.refreshSuggestTrigger())
		}
		if m.question != nil && len(m.question.Options) == 0 {
			m.pasteBurst.reset()
			cmd, _ := m.questionKeyText(msg.String(), msg.Key().Text)
			return m, cmd
		}
		if m.historySearch != nil && m.approval == nil && m.question == nil {
			m.pasteBurst.reset()
			return m, m.historySearchKeyPress(msg)
		}
		composerSurfaceAvailable := m.approval == nil && m.question == nil &&
			m.historySearch == nil && m.activePrimaryOverlayKind() == primaryOverlayNone &&
			m.transcriptPager == nil && m.sessionPicker == nil
		if composerSurfaceAvailable && m.submitting != nil && !submissionHasIndependentComposer(m.submitting) &&
			m.keyStartsSubmissionTypeAhead(msg) {
			m.beginSubmissionTypeAhead()
		}
		submissionBlocksComposer := m.submitting != nil && !submissionHasIndependentComposer(m.submitting)
		if m.approval != nil || m.question != nil || m.historySearch != nil ||
			submissionBlocksComposer || m.activePrimaryOverlayKind() != primaryOverlayNone ||
			m.transcriptPager != nil || m.sessionPicker != nil {
			m.pasteBurst.reset()
		} else {
			composerBefore = m.composerSnapshot()
			composerKey = msg
			trackComposer = true
			if cmd, handled := m.handlePasteBurstKey(msg); handled {
				m.recordComposerEdit(composerBefore, composerEditTyping)
				return m, cmd
			}
		}
		if cmd, handled := m.handleKey(msg.String()); handled {
			return m, cmd
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if key, ok := msg.(tea.KeyPressMsg); ok {
		m.snapVerticalGraphemeEditorKey(key.String())
		// Fallback: some terminals only deliver "!" via Text after input.Update.
		if key.Text == "!" || key.String() == "!" {
			m.absorbLoneBangIfNeeded()
		}
	}
	m.layout()
	if trackComposer {
		m.recordComposerEdit(composerBefore, composerKeyEditKind(composerKey))
	}
	return m, tea.Batch(cmd, m.refreshSuggestTrigger())
}

// maintainConfirmationStateForKey runs after chord resolution so confirmation
// gestures follow semantic actions rather than the physical final key in a
// chord. A consumed prefix keeps the current state until the chord resolves or
// times out; any resolved, unrelated action disarms it immediately.
func (m *Model) maintainConfirmationStateForKey(key string) {
	if !m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
		m.lastCtrlC = time.Time{}
		m.ctrlCHint = ""
	}
	if !m.keys.matches(KeyContextChat, ActionChatRewind, key) {
		m.rewindPrimed = false
	}
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
func (m *Model) handleEvent(ev map[string]any) tea.Cmd {
	hadApproval := m.approval != nil
	hadQuestion := m.question != nil
	// Streaming assistant output bypasses the per-event projection: deltas
	// accumulate into a per-message source whose stable/tail split owns its
	// own transcript entries (stream.go).
	if str(ev["type"]) == "ModelOutputDelta" {
		m.handleStreamEvent(ev)
		return nil
	}
	m.reduceConversationEvent(ev)
	attentionCmd := m.noteAttention(ev)
	m.pushEvent(ev)
	m.tasks.observeEvent(ev)
	m.observeQuestionResolution(ev)
	m.observeApprovalResolution(ev)
	cmd := m.handleTaskTerminalEvent(ev)
	switch str(ev["type"]) {
	case "permission.request":
		m.breakComposerUndoGroup()
		m.openApproval(ev)
	case "user.question":
		m.breakComposerUndoGroup()
		m.openQuestion(ev)
	}
	m.syncConversationGovernance()
	m.layout()
	overlayClosed := (hadApproval && m.approval == nil) || (hadQuestion && m.question == nil)
	if cmd == nil && overlayClosed {
		cmd = m.maybeSubmitNextQueued()
	}
	return tea.Batch(cmd, attentionCmd)
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
	if m.activePrimaryOverlayKind() != primaryOverlayNone {
		return m.dispatchPrimaryOverlayKey(key)
	}
	if m.sessionPicker != nil {
		return m.dispatchNavigatorKey(key)
	}
	// Reverse search owns every key while visible. In particular Ctrl+C must
	// restore the exact draft instead of arming the global exit cascade.
	if m.historySearch != nil {
		return m.historySearchKey(key)
	}
	if m.transcriptPager != nil {
		return m.transcriptPagerKey(key)
	}
	if cmd, handled := m.attachmentKey(key); handled {
		return cmd, true
	}
	// The mention/slash suggestion panel is a transient, non-modal aid: it
	// never takes the keyboard away from the approval/question overlays
	// (those are already handled and returned above, so reaching here means
	// neither is open) and only intercepts a small set of keys itself.
	if m.suggest != nil && m.calculateLayout().suggestLines > 0 {
		before := m.composerSnapshot()
		if cmd, handled := m.suggestKey(key); handled {
			m.recordComposerEdit(before, composerEditOther)
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			m.breakComposerUndoGroup()
			m.closeSuggest()
			m.lastCtrlC = time.Time{}
			m.ctrlCHint = ""
			m.layout()
			return nil, true
		}
	}
	if m.inFlightTaskID != "" && m.keys.matches(KeyContextChat, ActionChatInterrupt, key) {
		m.breakComposerUndoGroup()
		m.rewindPrimed = false
		taskID := m.inFlightTaskID
		m.applyConversation(conversationTransition{Kind: transitionInterrupted, TaskID: taskID, EventType: "operator.interrupt", Status: "interrupted"})
		m.push(microcopy.Degrade(microcopy.DegradeInterruptedByUser, nil,
			microcopy.WithLocale(m.locale), microcopy.WithPlain(m.plain())))
		return m.cancelTask(taskID), true
	}
	if m.submitting != nil {
		switch {
		case m.keys.matches(KeyContextGlobal, ActionGlobalHelp, key):
			m.breakComposerUndoGroup()
			m.showHelp()
			return nil, true
		case m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key):
			m.breakComposerUndoGroup()
			return tea.ClearScreen, true
		case m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key):
			m.breakComposerUndoGroup()
			return m.ctrlC(), true
		}
		if !submissionHasIndependentComposer(m.submitting) {
			// Preserve the exact submitted draft and caret until input explicitly
			// starts a distinct next draft.
			return nil, true
		}
		switch {
		case m.keys.matches(KeyContextComposer, ActionComposerSubmit, key),
			m.keys.matches(KeyContextComposer, ActionComposerSubmitNew, key),
			m.keys.matches(KeyContextComposer, ActionComposerQueue, key),
			m.keys.matches(KeyContextComposer, ActionComposerRecallQueue, key),
			m.keys.matches(KeyContextComposer, ActionComposerExternalEditor, key),
			m.keys.matches(KeyContextComposer, ActionComposerHistorySearch, key),
			m.keys.matches(KeyContextComposer, ActionComposerHistoryPrevious, key),
			m.keys.matches(KeyContextComposer, ActionComposerHistoryNext, key):
			return nil, true
		case m.keys.matches(KeyContextComposer, ActionComposerUndo, key):
			if m.undoLatestPendingPaste() {
				return nil, true
			}
			m.undoComposer()
			return nil, true
		case m.keys.matches(KeyContextComposer, ActionComposerRedo, key):
			m.redoComposer()
			return nil, true
		default:
			// Textarea editing belongs to the independent next draft. Enter and
			// other submission commands above remain deduplicated until the ACK.
			if cmd, handled := m.handleGraphemeEditorKey(key); handled {
				return cmd, true
			}
			return nil, false
		}
	}
	// Sticky shell mode (Grok `!`): Esc on empty leaves mode before rewind.
	if m.tryExitShellModeFromKey(key) {
		m.breakComposerUndoGroup()
		return nil, true
	}
	// Empty normal prompt + `!` enters shell mode without inserting the character.
	if m.tryEnterShellModeFromKey(key, "") {
		m.breakComposerUndoGroup()
		return nil, true
	}
	if m.keys.matches(KeyContextChat, ActionChatRewind, key) && m.inFlightTaskID == "" &&
		m.retrySubmission == nil && historyDraftKey(m.currentDraft()) == "" {
		m.breakComposerUndoGroup()
		if m.rewindPrimed {
			return m.openCheckpointPicker(), true
		}
		m.rewindPrimed = true
		m.setOperationalNotice(m.text(MsgUpdateRewindAgain, MessageArgs{
			"rewind": primaryKeyLabel(m.keys.keys(KeyContextChat, ActionChatRewind)),
		}), theme.RoleMuted)
		return nil, true
	}
	if cmd, handled := m.handleWorkspaceKey(key); handled {
		return cmd, true
	}
	if m.keys.matches(KeyContextComposer, ActionComposerHistorySearch, key) {
		m.breakComposerUndoGroup()
		return nil, m.beginHistorySearch()
	}
	if m.keys.matches(KeyContextComposer, ActionComposerUndo, key) {
		if m.undoLatestPendingPaste() {
			return nil, true
		}
		m.undoComposer()
		return nil, true
	}
	if m.keys.matches(KeyContextComposer, ActionComposerRedo, key) {
		m.redoComposer()
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
		m.breakComposerUndoGroup()
		return m.submitWithIntent(false), true
	}
	if m.keys.matches(KeyContextComposer, ActionComposerSubmitNew, key) {
		m.breakComposerUndoGroup()
		return m.submitWithIntent(true), true
	}
	if m.keys.matches(KeyContextComposer, ActionComposerNewline, key) {
		return nil, false
	}
	if cmd, handled := m.handleGraphemeEditorKey(key); handled {
		return cmd, true
	}

	switch {
	case transcriptKeyAllowed(ActionPagerPageUp, canonicalKey(key)) &&
		m.keys.matches(KeyContextPager, ActionPagerPageUp, key):
		m.breakComposerUndoGroup()
		m.vp.PageUp()
		m.followTail = false
		return nil, true
	case transcriptKeyAllowed(ActionPagerPageDown, canonicalKey(key)) &&
		m.keys.matches(KeyContextPager, ActionPagerPageDown, key):
		m.breakComposerUndoGroup()
		m.vp.PageDown()
		if m.vp.AtBottom() {
			m.followTail = true
			m.unseenLines = 0
		}
		return nil, true
	case transcriptKeyAllowed(ActionPagerTop, canonicalKey(key)) &&
		m.keys.matches(KeyContextPager, ActionPagerTop, key):
		m.breakComposerUndoGroup()
		m.vp.GotoTop()
		m.followTail = false
		return nil, true
	case transcriptKeyAllowed(ActionPagerBottom, canonicalKey(key)) &&
		m.keys.matches(KeyContextPager, ActionPagerBottom, key):
		m.breakComposerUndoGroup()
		m.vp.GotoBottom()
		m.followTail = true
		m.unseenLines = 0
		return nil, true
	case transcriptKeyAllowed(ActionPagerToggleDetail, canonicalKey(key)) &&
		m.keys.matches(KeyContextPager, ActionPagerToggleDetail, key):
		m.breakComposerUndoGroup()
		if m.tr.toggleLastCollapsible(m.th, m.transcriptWidth()) {
			m.vp.SetContentLines(m.tr.lines)
			if m.followTail {
				m.vp.GotoBottom()
			}
		}
		return nil, true
	case m.keys.matches(KeyContextGlobal, ActionGlobalHelp, key):
		m.breakComposerUndoGroup()
		m.showHelp()
		return nil, true
	case m.keys.matches(KeyContextGlobal, ActionGlobalModeCycle, key):
		// Grok uses Shift+Tab for permission/plan mode cycling. Carina cycles
		// only the governed build↔plan interaction mode (no silent YOLO).
		m.breakComposerUndoGroup()
		return m.cycleInteractionMode(), true
	case m.keys.matches(KeyContextGlobal, ActionGlobalSettings, key):
		m.breakComposerUndoGroup()
		m.openSettings(settingsTabOverview)
		return nil, true
	case m.keys.matches(KeyContextGlobal, ActionGlobalTranscript, key):
		m.breakComposerUndoGroup()
		m.openTranscriptPager()
		return nil, true
	case m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key):
		m.breakComposerUndoGroup()
		return tea.ClearScreen, true
	case m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key):
		m.breakComposerUndoGroup()
		return m.ctrlC(), true
	case m.keys.matches(KeyContextGlobal, ActionGlobalExit, key):
		m.breakComposerUndoGroup()
		return m.ctrlD()
	}
	return nil, false
}

func (m *Model) handleMouseWheel(msg tea.MouseWheelMsg) tea.Cmd {
	if cmd, handled := m.dispatchPrimaryOverlayMouse(msg); handled {
		return cmd
	}
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
	if m.transcriptPager != nil {
		m.transcriptPager.scrollBy(delta)
		m.clampTranscriptPagerScroll(m.transcriptPagerLines())
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
	interruptKey := primaryKeyLabel(m.keys.keys(KeyContextGlobal, ActionGlobalInterrupt))
	armed := !m.lastCtrlC.IsZero() && now.Sub(m.lastCtrlC) <= ctrlCWindow
	m.lastCtrlC = now
	if armed {
		m.ctrlCHint = ""
		return tea.Quit
	}
	if m.submitting != nil {
		m.setOperationalNotice(m.text(MsgUpdateSubmissionAck, MessageArgs{"interrupt": interruptKey}), theme.RoleMuted)
		return nil
	}
	hintText := m.text(MsgUpdateExitHint, MessageArgs{"interrupt": interruptKey})
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
		m.setOperationalNotice(hintText, theme.RoleMuted)
		return m.cancelTask(tid)
	}
	if m.retrySubmission != nil {
		m.setOperationalNotice(m.text(MsgUpdateUnknownSubmission, MessageArgs{
			"new": m.keys.label(KeyContextComposer, ActionComposerSubmitNew), "interrupt": interruptKey,
		}), theme.RoleWarning)
		m.setOperationalNotice(hintText, theme.RoleMuted)
		return nil
	}
	if draft := m.currentDraft(); !draftEmpty(draft) {
		recoverable := historyDraftKey(draft) != ""
		if recoverable {
			m.recordHistory(draft)
		}
		m.historyPos = len(m.history)
		m.historyScratch = promptDraft{}
		m.input.Reset()
		m.pendingPrefix = nil
		m.pendingPaste = nil
		m.attachments = nil
		m.clearAttachmentInteraction()
		m.queueRecallPending = false
		m.resetComposerUndo()
		m.closeSuggest()
		m.layout()
		message := m.text(MsgUpdateDraftCleared, nil)
		if recoverable {
			message = m.text(MsgUpdateDraftClearedRecover, nil)
		}
		m.setOperationalNotice(message+" · "+hintText, theme.RoleMuted)
		return m.maybeSubmitNextQueued()
	}
	m.setOperationalNotice(hintText, theme.RoleMuted)
	return nil
}

func (m *Model) ctrlD() (tea.Cmd, bool) {
	if m.approval != nil || m.question != nil || m.planReview != nil || m.helpOpen || m.checkpointPicker != nil || m.modelPicker != nil || m.sessionPicker != nil || m.keymapEditor != nil {
		return nil, true
	}
	if draft := m.currentDraft(); !draftEmpty(draft) {
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
	return m.submitWithIntent(false)
}

func (m *Model) submitWithIntent(forceNew bool) tea.Cmd {
	draft := m.currentDraft()
	if m.pendingSessionID != "" {
		m.setOperationalNotice(m.text(MsgSessionSwitching, MessageArgs{"session": m.pendingSessionID}), theme.RoleInfo)
		return nil
	}
	text := strings.TrimSpace(draft.Text)
	paste := draft.Paste
	if text == "" && len(draft.Prefix) == 0 && len(paste) == 0 {
		return nil
	}
	if len(draft.Prefix) == 0 && text == "/editor" {
		original := cloneDraft(draft)
		originalSnapshot := m.composerSnapshot()
		draft.Text = ""
		m.input.Reset()
		cmd := m.beginExternalEditorWithSnapshot(draft, originalSnapshot)
		if m.editor == nil {
			m.restoreDraft(original)
			return nil
		}
		return cmd
	}
	// Sticky shell mode: every submit is a governed shell command (Grok bash mode).
	// Slash commands are intentionally disabled while sticky shell is active so
	// `!/tmp` is not confused with product slash routing.
	if m.inShellMode() && len(draft.Prefix) == 0 {
		command, ok := shellCommandFromDraft(text, true)
		if !ok {
			m.push(m.text(MsgUpdateUsageShell, nil))
			return nil
		}
		hist := normalizeDraftForShellHistory(draft, command)
		cmd := m.submitShell(hist, command)
		if cmd != nil {
			m.queueRecallPending = false
		}
		return cmd
	}
	if len(draft.Prefix) == 0 && strings.HasPrefix(text, "/") {
		valid := validSlashCommand(text)
		if valid && slashRequiresNewTask(text) && !m.retryingExistingSubmission(draftPrompt(draft), forceNew) && !m.newTaskReady() {
			m.blockNewTaskForReadiness()
			m.layout()
			return nil
		}
		if valid {
			// Consume the command snapshot before dispatch. Session-switch guards
			// must distinguish the command being executed from a separate unsent
			// draft, and async failures remain recoverable through prompt history.
			m.queueRecallPending = false
			m.commitDraft(draft, false)
		}
		cmd := m.slashCommand(text)
		if valid {
			if cmd == nil && !m.helpOpen && m.transcriptPager == nil &&
				m.checkpointPicker == nil && m.modelPicker == nil && m.sessionPicker == nil && m.keymapEditor == nil {
				return m.maybeSubmitNextQueued()
			}
		}
		return cmd
	}
	// One-shot shell: `!cmd` in normal mode still works without sticky mode.
	if len(draft.Prefix) == 0 && strings.HasPrefix(text, "!") {
		command, ok := shellCommandFromDraft(text, false)
		if !ok {
			// Lone `!` enters sticky mode instead of erroring.
			if strings.TrimSpace(text) == "!" {
				m.enterShellMode()
				m.input.Reset()
				return nil
			}
			m.push(m.text(MsgUpdateUsageShell, nil))
			return nil
		}
		hist := normalizeDraftForShellHistory(draft, command)
		cmd := m.submitShell(hist, command)
		if cmd != nil {
			m.queueRecallPending = false
		}
		return cmd
	}
	if m.retrySubmission != nil {
		cmd := m.beginSubmissionSourceWithIntent(submissionTask, "", draft, false, forceNew)
		if cmd != nil {
			m.queueRecallPending = false
		}
		return cmd
	}
	if m.inFlightTaskID != "" {
		m.queueRecallPending = false
		return m.beginSubmission(submissionSteer, m.inFlightTaskID, draft)
	}
	cmd := m.beginSubmissionSourceWithIntent(submissionTask, "", draft, false, forceNew)
	if cmd != nil {
		m.queueRecallPending = false
	}
	return cmd
}

func (m *Model) currentDraft() promptDraft {
	return promptDraft{
		Prefix:      append([]string(nil), m.pendingPrefix...),
		Text:        m.input.Value(),
		Paste:       append([]string(nil), m.pendingPaste...),
		Attachments: cloneAttachments(m.attachments),
	}
}

func draftsEqual(a, b promptDraft) bool {
	if a.Text != b.Text || a.Model != b.Model || a.Agent != b.Agent || a.Mode != b.Mode || a.ReasoningEffort != b.ReasoningEffort || len(a.Prefix) != len(b.Prefix) || len(a.Paste) != len(b.Paste) || len(a.Attachments) != len(b.Attachments) {
		return false
	}
	for i := range a.Prefix {
		if a.Prefix[i] != b.Prefix[i] {
			return false
		}
	}
	for i := range a.Paste {
		if a.Paste[i] != b.Paste[i] {
			return false
		}
	}
	for i := range a.Attachments {
		if a.Attachments[i].ID != b.Attachments[i].ID || a.Attachments[i].Digest != b.Attachments[i].Digest ||
			a.Attachments[i].TextOffset != b.Attachments[i].TextOffset {
			return false
		}
		if (a.Attachments[i].Ref == nil) != (b.Attachments[i].Ref == nil) {
			return false
		}
		if a.Attachments[i].Ref != nil && *a.Attachments[i].Ref != *b.Attachments[i].Ref {
			return false
		}
	}
	return true
}

func draftPrompt(draft promptDraft) string {
	parts := append([]string(nil), draft.Prefix...)
	text := strings.TrimSpace(draft.Text)
	if text != "" {
		parts = append(parts, text)
	}
	parts = append(parts, draft.Paste...)
	return strings.Join(parts, "\n")
}

func draftLabel(draft promptDraft) string {
	if text := strings.TrimSpace(draft.Text); text != "" {
		return text
	}
	if len(draft.Prefix) > 0 {
		return "[restored content]"
	}
	if len(draft.Attachments) > 0 {
		return "[image attachment]"
	}
	return "[pasted content]"
}

func (m *Model) beginSubmission(kind submissionKind, target string, draft promptDraft) tea.Cmd {
	return m.beginSubmissionSource(kind, target, draft, false)
}

func (m *Model) beginSubmissionSource(kind submissionKind, target string, draft promptDraft, fromQueue bool) tea.Cmd {
	return m.beginSubmissionSourceWithIntent(kind, target, draft, fromQueue, false)
}

func (m *Model) beginSubmissionSourceWithIntent(kind submissionKind, target string, draft promptDraft, fromQueue, forceNew bool) tea.Cmd {
	if m.pendingSessionID != "" {
		m.setOperationalNotice(m.text(MsgSessionSwitching, MessageArgs{"session": m.pendingSessionID}), theme.RoleInfo)
		return nil
	}
	if m.submitting != nil {
		return nil
	}
	var shellArgv []string
	if kind == submissionShell {
		var err error
		shellArgv, err = shellwords.Parse(target)
		if err != nil || len(shellArgv) == 0 {
			if err == nil {
				err = errors.New("command is empty")
			}
			m.setOperationalNotice(m.text(MsgUpdateCommandParseFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}), theme.RoleError)
			if fromQueue {
				m.restoreQueuedDrafts("invalid queued command")
			}
			m.layout()
			return nil
		}
	}
	call := m.call
	if call == nil {
		m.breakComposerUndoGroup()
		m.setOperationalNotice(m.text(MsgUpdateDisconnectedDraft, MessageArgs{"glyph": glyphFailed(m.th)}), theme.RoleError)
		return nil
	}
	if kind == submissionTask && draftNeedsAttachmentUpload(draft) {
		return m.beginAttachmentUpload(kind, target, draft, fromQueue, forceNew)
	}
	m.breakComposerUndoGroup()
	draft = cloneDraft(draft)
	m.submissionGen++
	clientID := ""
	prompt := draftPrompt(draft)
	if strings.TrimSpace(prompt) == "" && len(draft.Attachments) > 0 {
		prompt = "Analyze the attached image or images."
	}
	envelope := submissionEnvelope{
		prompt: prompt, model: draft.Model, agent: draft.Agent, mode: draft.Mode,
		reasoningEffort: draft.ReasoningEffort, inputMediaRefs: draftMediaReferences(draft),
	}
	if envelope.model == "" {
		envelope.model = m.model
	}
	if envelope.reasoningEffort == "" {
		envelope.reasoningEffort = m.reasoningEffort
	}
	if envelope.mode == "" {
		envelope.mode = "background"
	}
	if kind == submissionTask {
		retryingExisting := m.retryingExistingSubmission(prompt, forceNew)
		if !retryingExisting && !m.newTaskReady() {
			m.blockNewTaskForReadiness()
			m.layout()
			return nil
		}
		if m.submissionLeaseErr != nil {
			m.setOperationalNotice(m.text(MsgUpdateSubmissionUnavailable, MessageArgs{"glyph": glyphFailed(m.th), "error": m.submissionLeaseErr.Error()}), theme.RoleError)
			m.layout()
			return nil
		}
		if retry := m.retrySubmission; retry != nil && !forceNew {
			if retry.prompt != prompt {
				m.setOperationalNotice(m.text(MsgUpdatePendingSubmission, MessageArgs{
					"glyph": glyphFailed(m.th), "new": m.keys.label(KeyContextComposer, ActionComposerSubmitNew),
				}), theme.RoleWarning)
				m.layout()
				return nil
			}
			clientID = retry.clientID
			envelope = submissionEnvelope{
				prompt: retry.prompt, model: retry.model, agent: retry.agent, mode: retry.mode,
				reasoningEffort: retry.reasoningEffort, inputMediaRefs: draftMediaReferences(retry.draft),
			}
		} else {
			clientID = newClientSubmissionID(m.submissionGen, m.now().UnixNano())
		}
		retry := submissionRetry{clientID: clientID, prompt: envelope.prompt, draft: cloneDraft(draft), model: envelope.model, agent: envelope.agent, mode: envelope.mode, reasoningEffort: envelope.reasoningEffort}
		if err := m.submissions.save(m.sessionID, retry); err != nil {
			m.setOperationalNotice(m.text(MsgUpdateRecoverySaveFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}), theme.RoleError)
			m.layout()
			return nil
		}
		m.retrySubmission = &retry
	}
	state := &submissionState{
		generation:   m.submissionGen,
		kind:         kind,
		target:       target,
		draft:        cloneDraft(draft),
		consumePaste: kind != submissionShell,
		fromQueue:    fromQueue,
		clientID:     clientID,
		envelope:     envelope,
	}
	m.submitting = state
	m.applyConversation(conversationTransition{Kind: transitionSubmitting, TaskID: target, EventType: "submission.started", Status: string(kind)})
	m.closeSuggest()
	m.layout()
	sid, generation, wire := m.sessionID, state.generation, state.envelope
	return func() tea.Msg {
		switch kind {
		case submissionSteer:
			err := call.Call("task.steer", map[string]any{"task_id": target, "message": prompt}, nil)
			return submissionDoneMsg{generation: generation, taskID: target, err: err}
		case submissionShell:
			var out any
			err := call.Call("command.exec", map[string]any{"session_id": sid, "argv": shellArgv}, &out)
			raw, _ := json.MarshalIndent(out, "", "  ")
			return submissionDoneMsg{generation: generation, result: string(raw), err: err}
		default:
			params := map[string]any{
				"session_id": sid, "prompt": wire.prompt, "client_submission_id": clientID,
			}
			if wire.model != "" {
				params["model"] = wire.model
			}
			if wire.reasoningEffort != "" {
				params["reasoning_effort"] = wire.reasoningEffort
			}
			if wire.agent != "" {
				params["agent"] = wire.agent
			}
			if wire.mode != "" {
				params["mode"] = wire.mode
			}
			if len(wire.inputMediaRefs) > 0 {
				params["input_media_refs"] = wire.inputMediaRefs
			}
			var out struct {
				TaskID string `json:"task_id"`
				Status string `json:"status"`
			}
			err := call.Call("task.submit", params, &out)
			if err == nil && out.TaskID == "" {
				err = errors.New("daemon returned an empty task_id")
			}
			return submissionDoneMsg{generation: generation, taskID: out.TaskID, status: out.Status, err: err}
		}
	}
}

func (m *Model) retryingExistingSubmission(prompt string, forceNew bool) bool {
	return m.retrySubmission != nil && !forceNew && m.retrySubmission.prompt == prompt
}

func (m *Model) submitShell(draft promptDraft, command string) tea.Cmd {
	if command == "" {
		m.push(m.text(MsgUpdateUsageShell, nil))
		return nil
	}
	return m.beginSubmission(submissionShell, command, draft)
}

func (m *Model) handleSubmissionDone(msg submissionDoneMsg) tea.Cmd {
	state := m.submitting
	if state == nil || state.generation != msg.generation {
		return nil
	}
	m.submitting = nil
	if msg.err != nil {
		if state.kind == submissionSteer && m.inFlightTaskID != "" {
			m.applyConversation(conversationTransition{Kind: transitionRunning, TaskID: m.inFlightTaskID, EventType: "submission.failed", Status: "running"})
		} else {
			m.applyConversation(conversationTransition{Kind: transitionIdle, EventType: "submission.failed"})
		}
		m.clearEarlyTerminals(state.generation)
		if state.kind == submissionTask {
			m.retrySubmission = &submissionRetry{
				clientID: state.clientID, prompt: state.envelope.prompt, draft: cloneDraft(state.draft),
				model: state.envelope.model, agent: state.envelope.agent, mode: state.envelope.mode, reasoningEffort: state.envelope.reasoningEffort,
			}
			m.setOperationalNotice(m.text(MsgUpdateTaskNotAcknowledged, MessageArgs{
				"glyph": glyphFailed(m.th), "error": msg.err.Error(), "key": state.clientID,
			}), theme.RoleError)
			if state.fromQueue {
				m.recallQueuedSubmissionForRetry()
			}
		} else {
			m.setOperationalNotice(m.text(MsgUpdateSubmissionFailed, MessageArgs{
				"glyph": glyphFailed(m.th), "kind": state.kind, "error": msg.err.Error(),
			}), theme.RoleError)
			if state.fromQueue {
				m.restoreQueuedDrafts("automatic submission failure")
			}
		}
		m.restoreFailedSubmission(state)
		m.layout()
		return nil
	}
	if state.fromQueue {
		queued, ok := m.followUps.front()
		if !ok || !draftsEqual(queued, state.draft) {
			// The submitted queue entry has independent ownership while its ACK is
			// pending. Restore every owned draft rather than guessing from current
			// composer text.
			if !ok {
				m.followUps.enqueue(state.draft)
			}
			m.restoreQueuedDrafts("queue ordering failure")
			m.setOperationalNotice(m.text(MsgUpdateQueueChanged, MessageArgs{"glyph": glyphFailed(m.th)}), theme.RoleError)
			return nil
		}
		_, _ = m.followUps.popFront()
	}
	earlySuccessful, earlyTerminal := m.takeEarlyTerminal(msg.taskID, state.generation)
	if terminal, successful := taskStatusTerminal(msg.status); terminal {
		earlyTerminal, earlySuccessful = true, successful
		m.tasks.setTask(msg.taskID, strings.ToLower(strings.TrimSpace(msg.status)))
		if m.inFlightTaskID == msg.taskID {
			m.inFlightTaskID = ""
		}
		m.applyConversation(conversationTransition{Kind: transitionTerminal, TaskID: msg.taskID, EventType: "submission.terminal", Status: msg.status, Outcome: normalizeConversationOutcome(msg.status)})
	}
	if state.clientID != "" && m.retrySubmission != nil && m.retrySubmission.clientID == state.clientID {
		m.retrySubmission = nil
		if err := m.submissions.clear(m.sessionID, state.clientID); err != nil {
			m.setOperationalNotice(m.text(MsgUpdateRecoveryClearFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}), theme.RoleError)
		}
	}
	if submissionHasIndependentComposer(state) {
		m.commitBackgroundDraft(state.draft, state.consumePaste)
	} else {
		m.commitDraft(state.draft, state.consumePaste)
	}
	shown := draftLabel(state.draft)
	switch state.kind {
	case submissionTask:
		m.push(m.th.Style(theme.RoleTitle).Render(m.text(MsgUpdateYou, nil)) + shown)
		if !earlyTerminal {
			m.inFlightTaskID = msg.taskID
			m.applyConversation(conversationTransition{Kind: transitionRunning, TaskID: msg.taskID, EventType: "submission.accepted", Status: "running"})
			m.tasks.setTask(msg.taskID, "running")
		}
	case submissionSteer:
		m.applyConversation(conversationTransition{Kind: transitionRunning, TaskID: msg.taskID, EventType: "steer.accepted", Status: "running"})
		m.push(m.th.Style(theme.RoleTitle).Render(m.text(MsgUpdateYouSteer, nil)) + shown)
		m.setOperationalNotice(m.text(MsgUpdateSteeringQueued, MessageArgs{"task": msg.taskID}), theme.RoleInfo)
	case submissionShell:
		m.applyConversation(conversationTransition{Kind: transitionIdle, EventType: "shell.completed"})
		m.push(m.th.Style(theme.RoleTitle).Render(m.text(MsgUpdateYouShell, nil)) + state.target)
		m.push(m.th.Style(theme.RoleTitle).Render(m.text(MsgUpdateShell, nil)) + "\n" + msg.result)
	}
	m.layout()
	if earlyTerminal {
		if !earlySuccessful {
			m.restoreQueuedDrafts("task failure")
			return nil
		}
		return m.maybeSubmitNextQueued()
	}
	if (state.kind == submissionSteer || state.fromQueue) && m.inFlightTaskID == "" {
		return m.maybeSubmitNextQueued()
	}
	return nil
}

func (m *Model) commitBackgroundDraft(draft promptDraft, consumePaste bool) {
	consumed := promptDraft{Prefix: append([]string(nil), draft.Prefix...), Text: draft.Text}
	if consumePaste {
		consumed.Paste = draft.Paste
	}
	m.recordHistoryPreservingNavigation(consumed)
	m.layout()
}

func (m *Model) recordHistoryPreservingNavigation(draft promptDraft) {
	oldLen, oldPos := len(m.history), m.historyPos
	scratch := cloneDraft(m.historyScratch)
	var navigated *promptDraft
	if oldPos >= 0 && oldPos < oldLen {
		copy := cloneDraft(m.history[oldPos])
		navigated = &copy
	}
	m.recordHistory(draft)
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
	m.historyScratch = scratch
}

func (m *Model) takeEarlyTerminal(taskID string, generation int) (bool, bool) {
	terminal, ok := m.earlyTerminals[taskID]
	if !ok || terminal.generation != generation {
		m.clearEarlyTerminals(generation)
		return false, false
	}
	m.clearEarlyTerminals(generation)
	return terminal.successful, true
}

func (m *Model) clearEarlyTerminals(generation int) {
	for taskID, terminal := range m.earlyTerminals {
		if terminal.generation == generation {
			delete(m.earlyTerminals, taskID)
		}
	}
}

func (m *Model) commitDraft(draft promptDraft, consumePaste bool) {
	current := m.currentDraft()
	if current.Text == draft.Text && (!consumePaste || draftsEqual(current, draft)) {
		m.input.Reset()
		if consumePaste {
			m.pendingPrefix = nil
			m.pendingPaste = nil
			m.attachments = nil
			m.clearAttachmentInteraction()
		}
	}
	consumed := promptDraft{Prefix: append([]string(nil), draft.Prefix...), Text: draft.Text}
	if consumePaste {
		consumed.Paste = draft.Paste
		consumed.Attachments = cloneAttachments(draft.Attachments)
	}
	m.recordHistory(consumed)
	m.historyPos = len(m.history)
	m.historyScratch = promptDraft{}
	m.resetComposerUndo()
	m.layout()
}

func (m *Model) recordHistory(draft promptDraft) {
	draft.Prefix = append([]string(nil), draft.Prefix...)
	draft.Paste = append([]string(nil), draft.Paste...)
	draft.Attachments = cloneAttachments(draft.Attachments)
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
	before := m.composerSnapshot()
	m.breakComposerUndoGroup()
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
		m.recordComposerEdit(before, composerEditOther)
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
	m.recordComposerEdit(before, composerEditOther)
	return true
}

func (m *Model) restoreDraft(draft promptDraft) {
	draft = m.restoreDraftMode(draft)
	m.input.SetValue(draft.Text)
	m.pendingPrefix = append([]string(nil), draft.Prefix...)
	m.pendingPaste = append([]string(nil), draft.Paste...)
	m.attachments = cloneAttachments(draft.Attachments)
	m.clearAttachmentInteraction()
	m.closeSuggest()
	m.applyComposerChrome()
	m.layout()
}

func (m *Model) resetSessionProjection() {
	m.releaseSessionGraphics()
	m.tr = transcript{}
	m.activityGroups = nil
	m.liveTools = ui.NewLiveToolRegistry()
	m.tasks = taskGraph{}
	m.inFlightTaskID = ""
	m.applyConversation(conversationTransition{Kind: transitionReset})
	m.pausedRestore = nil
	m.approval, m.question = nil, nil
	m.approvalQueue = nil
	m.approvalSeen = make(map[string]bool)
	m.approvalResolved = make(map[string]bool)
	m.approvalPending = make(map[string]approvalResolutionSnapshot)
	m.approvalOrder = make(map[string]uint64)
	m.approvalNextSeq, m.approvalOutcomeSeq = 0, 0
	m.questionSeen = make(map[string]bool)
	m.questionResolved = make(map[string]bool)
	m.questionQueue = nil
	m.history = nil
	m.historyPos = 0
	m.historyScratch = promptDraft{}
	m.historyLoadGen++
	m.canonicalGen++
	m.goal = nil
	if !m.modelPinned {
		m.model, m.reasoningEffort = "", ""
	}
	m.unseenLines, m.unreadAttention = 0, 0
	m.attentionSeen = nil
	m.attentionOrder = nil
	m.transcriptPager, m.checkpointPicker, m.modelPicker, m.sessionPicker = nil, nil, nil, nil
	m.closeSuggest()
	m.vp.SetContent("")
}

func (m *Model) slashCommand(text string) tea.Cmd {
	parts := strings.Fields(text)
	name := strings.TrimPrefix(parts[0], "/")
	switch name {
	case "attach":
		if len(parts) < 2 {
			m.setOperationalNoticeKind("attachment", m.text(MsgAttachmentFailed, MessageArgs{"error": "path is required"}), theme.RoleError)
			return nil
		}
		path := strings.Join(parts[1:], " ")
		resolved, ok := pastedImagePath(path, m.workspaceRoot)
		if !ok {
			m.setOperationalNoticeKind("attachment", m.text(MsgAttachmentFailed, MessageArgs{"error": "path is not a supported image"}), theme.RoleError)
			return nil
		}
		return m.attachImage(resolved)
	case "help", "keys":
		m.showHelp()
		return nil
	case "search":
		if len(parts) < 2 {
			m.push(m.text(MsgUpdateUsageSearch, nil))
			return nil
		}
		if m.call == nil {
			q := strings.ToLower(strings.Join(parts[1:], " "))
			hits := 0
			for _, line := range m.tr.lines {
				if strings.Contains(strings.ToLower(line), q) {
					m.push("- " + line)
					hits++
				}
			}
			m.push(m.countText(MsgUpdateSearchMatches, hits, nil))
			return nil
		}
		return m.queryCanonicalSurface(canonicalSearch, strings.Join(parts[1:], " "))
	case "recap":
		if m.call == nil {
			start := maxInt(len(m.tr.lines)-12, 0)
			m.push(m.th.Style(theme.RoleTitle).Render(m.text(MsgUpdateRecap, nil)) + "\n" + strings.Join(m.tr.lines[start:], "\n"))
			return nil
		}
		return m.queryCanonicalSurface(canonicalRecap, "")
	case "status":
		return m.openOperationalSurface("status", "session.get", map[string]any{"session_id": m.sessionID})
	case "permissions":
		if len(parts) == 3 && parts[1] == "new" && parts[2] == "safe-edit" {
			return m.newSessionWithProfile("safe-edit")
		}
		if len(parts) == 4 && parts[1] == "new" && parts[2] == "full-workspace" && parts[3] == "--yes" {
			return m.newSessionWithProfile("full-workspace")
		}
		if len(parts) != 1 {
			m.push(m.text(MsgUpdateUnknownCommand, MessageArgs{"command": "permissions; use /permissions or /permissions new <safe-edit|full-workspace> [--yes]"}))
			return nil
		}
		return m.openOperationalSurface("permissions", "profile.inventory", map[string]any{"session_id": m.sessionID})
	case "context":
		return m.openOperationalSurface("context", "context.summary", map[string]any{"session_id": m.sessionID})
	case "compact":
		if len(parts) != 1 {
			m.push(m.text(MsgUpdateUsageCompact, nil))
			return nil
		}
		params := map[string]any{"session_id": m.sessionID}
		if m.pausedRestore != nil && m.pausedRestore.TaskID != "" {
			params["task_id"] = m.pausedRestore.TaskID
		}
		return m.openOperationalSurface("compact", "session.checkpoint.compact", params)
	case "config", "settings":
		if len(parts) >= 2 {
			switch parts[1] {
			case "model", "effort", "mode", "permissions":
				return m.slashCommand("/" + strings.Join(parts[1:], " "))
			case "keymap":
				if len(parts) == 2 {
					m.openKeymapEditor()
					return nil
				}
			case "raw":
				return m.openOperationalSurface("config", "config.inventory", map[string]any{"session_id": m.sessionID})
			}
			m.push(m.text(MsgUpdateUnknownCommand, MessageArgs{"command": "config; use /config [model|effort|mode|permissions|keymap|raw]"}))
			return nil
		}
		// Grok/CC open a settings shell instead of dumping inventory into chat.
		m.openSettings(settingsTabOverview)
		return m.refreshRuntimeStatus()
	case "doctor":
		return m.openOperationalSurface("doctor", "daemon.doctor", map[string]any{})
	case "usage", "cost":
		return m.openOperationalSurface("usage", "usage.cost", map[string]any{"session_id": m.sessionID})
	case "review":
		return m.beginSubmissionSourceWithIntent(submissionTask, "", promptDraft{Text: text}, false, false)
	case "commit":
		extra := ""
		if len(parts) > 1 {
			extra = strings.Join(parts[1:], " ")
		}
		return m.commitWorkflow(extra)
	case "btw", "side":
		args := parts[1:]
		fork := name == "side"
		if len(args) > 0 && (args[0] == "--fork" || args[0] == "-f") {
			fork = true
			args = args[1:]
		}
		if len(args) == 0 {
			m.push(m.text(MsgUpdateUsageBtw, nil))
			return nil
		}
		return m.btwSideQuestion(strings.Join(args, " "), fork)
	case "side-close", "close-side", "main":
		return m.closeSidePane()
	case "plan":
		if len(parts) == 1 {
			_ = m.ensurePlanFileScaffold()
			return m.changeMode("plan")
		}
		return m.enterPlanMode(strings.Join(parts[1:], " "))
	case "build":
		return m.changeMode("build")
	case "approve-plan", "approve_plan":
		return m.approvePlan()
	case "view-plan", "show-plan", "plan-view":
		m.viewPlanSurface()
		return nil
	case "explain":
		m.explainRuntimeSurface()
		return nil
	case "always-approve", "always_approve", "yolo":
		// Product always-approve with mandatory warning on enable.
		if len(parts) == 1 {
			return m.toggleAlwaysApprove()
		}
		switch parts[1] {
		case "on", "true", "1", "enable":
			return m.setAlwaysApprove(true)
		case "off", "false", "0", "disable":
			return m.setAlwaysApprove(false)
		case "toggle":
			return m.toggleAlwaysApprove()
		default:
			m.push(m.text(MsgUpdateUsageAlwaysApprove, nil))
			return nil
		}
	case "approval-mode", "approval_mode":
		if len(parts) == 1 {
			m.push(m.text(MsgApprovalModeCurrent, MessageArgs{"mode": m.approvalModeLabel()}))
			return nil
		}
		switch strings.ToLower(parts[1]) {
		case "ask", "interactive":
			return m.setApprovalMode("ask")
		case "always-approve", "always_approve", "alwaysapprove", "yolo", "bypass":
			return m.setApprovalMode("always-approve")
		case "dont-ask", "dont_ask", "dontask":
			return m.setApprovalMode("dont-ask")
		case "accept-edits", "accept_edits", "acceptedits":
			return m.setApprovalMode("accept-edits")
		default:
			m.push(m.text(MsgUpdateUsageApprovalMode, nil))
			return nil
		}
	case "dont-ask", "dont_ask", "dontask":
		if len(parts) == 1 {
			if m.approvalModeLabel() == "dont-ask" {
				return m.setApprovalMode("ask")
			}
			return m.setApprovalMode("dont-ask")
		}
		switch parts[1] {
		case "on", "true", "1", "enable":
			return m.setApprovalMode("dont-ask")
		case "off", "false", "0", "disable":
			return m.setApprovalMode("ask")
		case "toggle":
			if m.approvalModeLabel() == "dont-ask" {
				return m.setApprovalMode("ask")
			}
			return m.setApprovalMode("dont-ask")
		default:
			m.push(m.text(MsgUpdateUsageDontAsk, nil))
			return nil
		}
	case "accept-edits", "accept_edits", "acceptedits":
		if len(parts) == 1 {
			if m.approvalModeLabel() == "accept-edits" {
				return m.setApprovalMode("ask")
			}
			return m.setApprovalMode("accept-edits")
		}
		switch parts[1] {
		case "on", "true", "1", "enable":
			return m.setApprovalMode("accept-edits")
		case "off", "false", "0", "disable":
			return m.setApprovalMode("ask")
		case "toggle":
			if m.approvalModeLabel() == "accept-edits" {
				return m.setApprovalMode("ask")
			}
			return m.setApprovalMode("accept-edits")
		default:
			m.push(m.text(MsgUpdateUsageAcceptEdits, nil))
			return nil
		}
	case "inspect", "welcome":
		m.openOperationalPager("inspect", m.text(MsgInspectHeader, nil))
		m.transcriptPager.operationalMethod = "__inspect__"
		return m.inspectSurface()
	case "tasks", "ps":
		return m.showTasksSurfaceAsync()
	case "extension":
		if len(parts) == 3 && parts[1] == "enable" {
			return m.extensionToggle(parts[2], true)
		}
		if len(parts) == 3 && parts[1] == "disable" {
			return m.extensionToggle(parts[2], false)
		}
		m.push(m.text(MsgUpdateUsageExtension, nil))
		return nil
	case "sessions":
		return m.openSessionPicker()
	case "export":
		path := ""
		if len(parts) >= 2 {
			path = strings.Join(parts[1:], " ")
		}
		return m.exportTranscript(path)
	case "remember":
		return m.rememberNote(strings.Join(parts[1:], " "))
	case "init":
		return m.initProjectRules()
	case "compact-mode":
		m.compactMode = !m.compactMode
		m.push(m.text(MsgUpdateCompactMode, MessageArgs{"state": map[bool]string{true: "on", false: "off"}[m.compactMode]}))
		m.layout()
		return nil
	case "session-review":
		return m.openOperationalSurface("review", "session.review", map[string]any{"session_id": m.sessionID})
	case "memory":
		if len(parts) == 1 || parts[1] == "status" {
			return m.openOperationalSurface("memory", "memory.status", map[string]any{"session_id": m.sessionID})
		}
		if parts[1] == "list" && len(parts) == 2 {
			return m.openOperationalSurface("memory", "memory.list", map[string]any{"session_id": m.sessionID, "target": "memory"})
		}
		if parts[1] == "search" && len(parts) >= 3 {
			return m.openOperationalSurface("memory", "memory.search", map[string]any{"session_id": m.sessionID, "target": "memory", "query": strings.Join(parts[2:], " "), "mode": "auto"})
		}
		if parts[1] == "read" && len(parts) <= 3 {
			target := "memory"
			if len(parts) == 3 {
				target = parts[2]
			}
			if !validMemoryTarget(target) {
				m.push(m.text(MsgUpdateUsageMemory, nil))
				return nil
			}
			return m.openOperationalSurface("memory", "memory.read", map[string]any{"session_id": m.sessionID, "target": target})
		}
		if parts[1] == "verify" && len(parts) >= 2 && len(parts) <= 4 {
			target := "memory"
			if len(parts) >= 3 {
				target = parts[2]
			}
			if !validMemoryTarget(target) {
				m.push(m.text(MsgUpdateUsageMemory, nil))
				return nil
			}
			params := map[string]any{"session_id": m.sessionID, "target": target}
			if len(parts) == 4 {
				params["revision"] = parts[3]
			}
			return m.openOperationalSurface("memory", "memory.verify", params)
		}
		if parts[1] == "rollback" && len(parts) == 7 && parts[6] == "--yes" && validMemoryTarget(parts[2]) {
			return m.openOperationalSurface("memory", "memory.rollback", map[string]any{
				"session_id": m.sessionID, "target": parts[2], "revision": parts[3],
				"expected_revision": parts[4], "idempotency_key": parts[5],
			})
		}
		if parts[1] == "handoff" && len(parts) == 7 && parts[6] == "--yes" && validMemoryTarget(parts[3]) {
			expected := parts[4]
			if expected == "-" {
				expected = ""
			}
			return m.openOperationalSurface("memory", "memory.handoff", map[string]any{
				"source_session_id": m.sessionID, "target_session_id": parts[2], "target": parts[3],
				"expected_revision": expected, "idempotency_key": parts[5],
			})
		}
		m.push(m.text(MsgUpdateUsageMemory, nil))
		return nil
	case "skills":
		return m.openOperationalSurface("skills", "skill.inventory", map[string]any{"session_id": m.sessionID})
	case "hooks":
		return m.openOperationalSurface("hooks", "hook.inventory", map[string]any{"session_id": m.sessionID})
	case "extensions":
		return m.openOperationalSurface("extensions", "extension.list", map[string]any{"workspace_root": m.workspaceRoot})
	case "diff":
		if len(parts) != 1 {
			m.push(m.text(MsgUpdateUsageDiff, nil))
			return nil
		}
		return m.openWorkspaceDiff()
	case "mcp":
		if len(parts) > 2 || (len(parts) == 2 && parts[1] != "verbose") {
			m.push(m.text(MsgUpdateUsageMCP, nil))
			return nil
		}
		return m.openOperationalSurface("mcp", "mcp.inventory", map[string]any{"verbose": len(parts) == 2})
	case "mode":
		if len(parts) == 2 && parts[1] == "cycle" {
			return m.cycleInteractionMode()
		}
		if len(parts) != 2 || (parts[1] != "build" && parts[1] != "plan") {
			m.push(m.text(MsgUpdateUsageMode, nil))
			return nil
		}
		return m.changeMode(parts[1])
	case "loop":
		return m.loopCommand(parts[1:])
	case "goal":
		return m.goalCommand(parts[1:])
	case "model":
		if len(parts) == 1 {
			return m.openModelPicker()
		}
		if len(parts) != 2 || strings.ContainsAny(parts[1], "\r\n\t") {
			m.push(m.text(MsgUpdateUsageModel, nil))
			return nil
		}
		previous, previousEffort := m.model, m.reasoningEffort
		if parts[1] == "default" {
			m.model = ""
		} else {
			m.model = parts[1]
		}
		m.reasoningEffort = ""
		m.modelPinned = false
		m.push(m.text(MsgUpdateModelChanged, MessageArgs{"model": parts[1]}))
		m.layout()
		return m.persistSessionModel(previous, previousEffort, m.model, m.reasoningEffort)
	case "effort":
		if len(parts) == 1 {
			return m.openModelPicker()
		}
		if d, ok := builtinCommand("effort"); !ok || !d.Validate(parts[1:]) {
			m.push(m.text(MsgUpdateUsageEffort, nil))
			return nil
		}
		previous := m.reasoningEffort
		effort := parts[1]
		if effort == "default" {
			effort = ""
		}
		m.reasoningEffort = effort
		m.push(m.text(MsgUpdateEffortChanged, MessageArgs{"effort": parts[1]}))
		return m.persistSessionModel(m.model, previous, m.model, m.reasoningEffort)
	case "agents":
		return m.openOperationalSurface("agents", "agent.list", map[string]any{"session_id": m.sessionID})
	case "checkpoints":
		return m.openCheckpointPicker()
	case "new":
		return m.newSession()
	case "clear":
		return m.newSession()
	case "rename":
		if len(parts) < 2 {
			m.push(m.text(MsgSessionRenameUsage, nil))
			return nil
		}
		return m.renameSession(strings.Join(parts[1:], " "))
	case "fork":
		if len(parts) > 2 {
			m.push("usage: /fork [task_id]")
			return nil
		}
		taskID := ""
		if len(parts) == 2 {
			taskID = parts[1]
		}
		return m.forkSession(taskID)
	case "resume":
		if len(parts) > 2 {
			m.push("usage: /resume [session_id]")
			return nil
		}
		if len(parts) == 1 {
			if m.pausedRestore != nil || m.switchSession == nil {
				return m.resumePausedRestore("")
			}
			return m.openSessionPicker()
		}
		if !strings.HasPrefix(parts[1], "sess_") {
			m.push("/resume task_id is deprecated; use /task-resume task_id")
			return m.resumePausedRestore(parts[1])
		}
		return m.resumeSession(parts[1])
	case "task-resume":
		if len(parts) > 2 {
			m.push(m.text(MsgUpdateUsageResume, nil))
			return nil
		}
		taskID := ""
		if len(parts) == 2 {
			taskID = parts[1]
		}
		return m.resumePausedRestore(taskID)
	case "keymap":
		m.openKeymapEditor()
		return nil
	case "copy":
		return m.copyLastAgentProjection()
	case "transcript":
		if m.call == nil {
			m.openTranscriptPager()
			return nil
		}
		return m.openCanonicalTranscriptPager()
	default:
		return m.resolveDynamicSlash(text)
	}
}

func validMemoryTarget(target string) bool { return target == "memory" || target == "user" }

func (m *Model) queryCanonicalSurface(kind canonicalSurfaceKind, query string) tea.Cmd {
	call, sessionID := m.call, m.sessionID
	generation := m.canonicalGen
	return func() tea.Msg {
		if call == nil {
			return canonicalSurfaceMsg{generation: generation, kind: kind, query: query, err: errors.New("daemon not connected")}
		}
		var items []map[string]any
		err := call.Call("session.items", map[string]any{"session_id": sessionID}, &items)
		return canonicalSurfaceMsg{generation: generation, kind: kind, query: query, items: items, err: err}
	}
}

func canonicalItemText(item map[string]any) string {
	parts := nonEmpty(str(item["timestamp"]), str(item["type"]), str(item["task_id"]), str(item["turn_id"]), str(item["item_id"]), str(item["source_event_id"]))
	if projected, ok := item["item"].(map[string]any); ok {
		parts = append(parts, nonEmpty(str(projected["type"]), str(projected["status"]), str(projected["id"]))...)
		if details, ok := projected["details"].(map[string]any); ok {
			parts = append(parts, compactMapLines(details, "  ")...)
		}
	}
	if details, ok := item["details"].(map[string]any); ok {
		parts = append(parts, compactMapLines(details, "  ")...)
	}
	return strings.Join(parts, " ")
}

func (m *Model) handleCanonicalSurface(msg canonicalSurfaceMsg) {
	if msg.kind == canonicalTranscript {
		if m.transcriptPager == nil || m.transcriptPager.generation != msg.generation {
			return
		}
		m.transcriptPager.loading = false
		if msg.err != nil {
			m.transcriptPager.err = msg.err.Error()
		} else {
			parts := make([]string, 0, len(msg.items))
			for _, item := range msg.items {
				parts = append(parts, canonicalItemText(item))
			}
			m.transcriptPager.text = strings.Join(parts, "\n\n")
		}
		m.layout()
		return
	}
	if msg.err != nil {
		m.push(m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.err.Error()}))
		return
	}
	switch msg.kind {
	case canonicalSearch:
		query := strings.ToLower(strings.TrimSpace(msg.query))
		var hits []string
		for _, item := range msg.items {
			text := canonicalItemText(item)
			if strings.Contains(strings.ToLower(text), query) {
				hits = append(hits, text)
			}
		}
		body := strings.Join(hits, "\n\n")
		if body == "" {
			body = m.text(MsgCanonicalSearchEmpty, nil)
		}
		m.push(m.th.Style(theme.RoleTitle).Render(m.text(MsgCanonicalSearchTitle, MessageArgs{"count": len(hits)})) + "\n" + body)
	case canonicalRecap:
		items := msg.items
		if len(items) > 20 {
			items = items[len(items)-20:]
		}
		parts := make([]string, 0, len(items))
		for _, item := range items {
			parts = append(parts, canonicalItemText(item))
		}
		body := strings.Join(parts, "\n\n")
		if body == "" {
			body = m.text(MsgCanonicalRecapEmpty, nil)
		}
		m.push(m.th.Style(theme.RoleTitle).Render(m.text(MsgUpdateRecap, nil)) + "\n" + body)
	}
}

func (m *Model) queryOperationalSurface(kind, method string, params map[string]any) tea.Cmd {
	call := m.call
	sessionID := m.sessionID
	generation := m.operationalGeneration(kind)
	return func() tea.Msg {
		if call == nil {
			return operationalSurfaceMsg{sessionID: sessionID, kind: kind, generation: generation, err: errors.New("daemon not connected")}
		}
		var out map[string]any
		err := call.Call(method, params, &out)
		return operationalSurfaceMsg{sessionID: sessionID, kind: kind, generation: generation, data: out, err: err}
	}
}

func (m *Model) openOperationalSurface(kind, method string, params map[string]any) tea.Cmd {
	m.openOperationalPager(kind, m.operationalSurfaceTitle(kind))
	m.transcriptPager.operationalMethod = method
	m.transcriptPager.operationalParams = cloneOperationalParams(params)
	return m.queryOperationalSurface(kind, method, params)
}

func (m *Model) operationalGeneration(kind string) int {
	if m.transcriptPager != nil && m.transcriptPager.operationalKind == kind {
		return m.transcriptPager.generation
	}
	return 0
}

func (m *Model) operationalSurfaceTitle(kind string) string {
	titleID := map[string]MessageID{
		"status": MsgOperationalStatusTitle, "permissions": MsgOperationalPermissionsTitle,
		"context": MsgOperationalContextTitle, "config": MsgOperationalConfigTitle, "mcp": MsgOperationalMCPTitle, "compact": MsgOperationalCompactTitle,
		"doctor": MsgOperationalDoctorTitle, "skills": MsgOperationalSkillsTitle, "hooks": MsgOperationalHooksTitle, "extensions": MsgOperationalExtensionsTitle,
		"usage": MsgOperationalUsageTitle, "review": MsgOperationalReviewTitle, "memory": MsgOperationalMemoryTitle,
		"inspect": MsgInspectHeader, "agents": MsgAgentsSummaryHeader,
	}[kind]
	if titleID == "" {
		return kind
	}
	return m.text(titleID, nil)
}

func (m *Model) openWorkspaceDiff() tea.Cmd {
	m.closeSuggest()
	m.canonicalGen++
	generation := m.canonicalGen
	m.transcriptPager = &transcriptPagerState{generation: generation, title: m.text(MsgDiffTitle, nil), loadingText: m.text(MsgDiffLoading, nil), loading: true}
	m.layout()
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return workspaceDiffMsg{generation: generation, err: errors.New("daemon not connected")}
		}
		var out map[string]any
		err := call.Call("workspace.diff", map[string]any{"session_id": sessionID}, &out)
		return workspaceDiffMsg{generation: generation, data: out, err: err}
	}
}

func (m *Model) handleWorkspaceDiff(msg workspaceDiffMsg) {
	if m.transcriptPager == nil || m.transcriptPager.generation != msg.generation {
		return
	}
	m.transcriptPager.loading = false
	if msg.err != nil {
		m.transcriptPager.err = msg.err.Error()
		m.layout()
		return
	}
	files, _ := msg.data["files"].([]any)
	var sections []string
	for _, raw := range files {
		row, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		header := m.text(MsgDiffFile, MessageArgs{"status": str(row["status"]), "path": str(row["path"])})
		switch {
		case diffBool(row["binary"]):
			header += " · " + m.text(MsgDiffBinary, nil)
		case diffBool(row["truncated"]):
			header += " · " + m.text(MsgDiffTruncated, nil)
		}
		body := str(row["diff"])
		if body != "" {
			header += "\n" + body
		}
		sections = append(sections, header)
	}
	if len(sections) == 0 {
		sections = []string{m.text(MsgDiffClean, nil)}
	}
	if diffBool(msg.data["truncated"]) {
		sections = append(sections, m.text(MsgDiffTotalTruncated, nil))
	}
	m.transcriptPager.text = strings.Join(sections, "\n\n")
	m.layout()
}

func diffBool(v any) bool { b, _ := v.(bool); return b }

func (m *Model) changeMode(mode string) tea.Cmd {
	if mode == "plan" {
		_ = m.ensurePlanFileScaffold()
	}
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return modeChangedMsg{sessionID: sessionID, mode: mode, err: errors.New("daemon not connected")}
		}
		err := call.Call("session.plan_mode", map[string]any{"session_id": sessionID, "on": mode == "plan"}, nil)
		return modeChangedMsg{sessionID: sessionID, mode: mode, err: err}
	}
}

func (m *Model) loopCommand(args []string) tea.Cmd {
	action := "list"
	params := map[string]any{}
	method := "schedule.list"
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "list"):
		params["session_id"] = m.sessionID
	case len(args) == 2 && (args[0] == "pause" || args[0] == "resume" || args[0] == "delete"):
		action, method = args[0], "schedule."+args[0]
		params["schedule_id"] = args[1]
		params["session_id"] = m.sessionID
	case len(args) >= 2:
		action, method = "create", "schedule.create"
		promptParts := make([]string, 0, len(args)-1)
		concurrency := "forbid"
		for i := 1; i < len(args); i++ {
			if args[i] == "--concurrency" {
				if i+1 >= len(args) || !validScheduleConcurrency(args[i+1]) {
					m.push(m.text(MsgUpdateUsageLoop, nil))
					return nil
				}
				concurrency = args[i+1]
				i++
				continue
			}
			promptParts = append(promptParts, args[i])
		}
		if strings.TrimSpace(strings.Join(promptParts, " ")) == "" {
			m.push(m.text(MsgUpdateUsageLoop, nil))
			return nil
		}
		params = map[string]any{
			"session_id": m.sessionID, "kind": "every", "expression": args[0],
			"prompt": strings.Join(promptParts, " "), "concurrency_policy": concurrency,
		}
		if m.model != "" {
			params["model"] = m.model
		}
		if m.reasoningEffort != "" {
			params["reasoning_effort"] = m.reasoningEffort
		}
	default:
		m.push(m.text(MsgUpdateUsageLoop, nil))
		return nil
	}
	call, sessionID := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return loopResultMsg{action: action, sessionID: sessionID, err: errors.New("daemon not connected")}
		}
		var out map[string]any
		err := call.Call(method, params, &out)
		return loopResultMsg{action: action, sessionID: sessionID, data: out, err: err}
	}
}

func validScheduleConcurrency(value string) bool {
	switch value {
	case "forbid", "queue", "replace", "allow":
		return true
	default:
		return false
	}
}

func (m *Model) handleLoopResult(msg loopResultMsg) {
	if msg.err != nil {
		m.push(m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.err.Error()}))
		return
	}
	if msg.action != "list" {
		id := str(msg.data["schedule_id"])
		m.push(m.text(MsgUpdateLoopChanged, MessageArgs{"action": msg.action, "id": id}))
		return
	}
	raw, _ := msg.data["schedules"].([]any)
	lines := []string{m.text(MsgUpdateLoopHeader, nil)}
	count := 0
	for _, entry := range raw {
		row, ok := entry.(map[string]any)
		if !ok || str(row["session_id"]) != msg.sessionID {
			continue
		}
		count++
		state := "paused"
		if enabled, _ := row["enabled"].(bool); enabled {
			state = "active"
		}
		lines = append(lines, m.text(MsgUpdateLoopItem, MessageArgs{
			"id": str(row["schedule_id"]), "state": state, "interval": str(row["expression"]),
			"next": str(row["next_run_at"]), "prompt": truncate(str(row["prompt"]), 100),
		}))
	}
	if count == 0 {
		lines = append(lines, m.text(MsgUpdateLoopEmpty, nil))
	}
	m.push(strings.Join(lines, "\n"))
}

func (m *Model) handleOperationalSurface(msg operationalSurfaceMsg) {
	if msg.generation != 0 {
		if m.transcriptPager == nil || m.transcriptPager.operationalKind != msg.kind || m.transcriptPager.generation != msg.generation {
			return
		}
		m.transcriptPager.loading = false
		if msg.err != nil {
			m.transcriptPager.err = msg.err.Error()
			m.layout()
			return
		}
		lines := m.humanizeOperationalSurface(msg.kind, msg.data)
		if len(lines) == 0 {
			lines = []string{m.text(MsgOperationalEmpty, nil)}
		}
		m.transcriptPager.text = strings.Join(lines, "\n")
		m.layout()
		return
	}
	if msg.err != nil {
		m.setOperationalNoticeKind(msg.kind, m.text(MsgUpdateRPCFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.err.Error()}), theme.RoleError)
		return
	}
	lines := m.humanizeOperationalSurface(msg.kind, msg.data)
	if len(lines) == 0 {
		lines = []string{m.text(MsgOperationalEmpty, nil)}
	}
	m.setOperationalNoticeKind(msg.kind, strings.Join(lines, " · "), theme.RoleInfo)
}

func compactMapLines(values map[string]any, indent string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var lines []string
	for _, key := range keys {
		value := values[key]
		switch typed := value.(type) {
		case map[string]any:
			lines = append(lines, indent+key+":")
			lines = append(lines, compactMapLines(typed, indent+"  ")...)
		case []any:
			if len(typed) == 0 {
				lines = append(lines, indent+key+": none")
				continue
			}
			lines = append(lines, fmt.Sprintf("%s%s: %d", indent, key, len(typed)))
			for _, entry := range typed {
				if object, ok := entry.(map[string]any); ok {
					lines = append(lines, compactMapLines(object, indent+"  ")...)
				} else {
					lines = append(lines, fmt.Sprintf("%s  - %v", indent, entry))
				}
			}
		default:
			lines = append(lines, fmt.Sprintf("%s%s: %v", indent, key, value))
		}
	}
	return lines
}

func (m *Model) querySurface(method string, params map[string]any, label string) tea.Cmd {
	call := m.call
	sessionID := m.sessionID
	return func() tea.Msg {
		if call == nil {
			return rpcErrMsg{err: errors.New("daemon not connected")}
		}
		var out any
		if err := call.Call(method, params, &out); err != nil {
			return rpcErrMsg{err: err}
		}
		raw, _ := json.MarshalIndent(out, "", "  ")
		return surfaceResultMsg{sessionID: sessionID, label: label, text: string(raw)}
	}
}

// handlePaste normalizes bracketed-paste content (terminals paste \r line
// endings — spike sharp edge). Multi-line payloads stay out of the textarea
// but are rendered beside it as visible, independently undoable draft items.
func (m *Model) handlePaste(msg tea.PasteMsg) tea.Cmd {
	content := strings.ReplaceAll(strings.ReplaceAll(msg.Content, "\r\n", "\n"), "\r", "\n")
	if path, ok := pastedImagePath(content, m.workspaceRoot); ok {
		return m.attachImage(path)
	}
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
