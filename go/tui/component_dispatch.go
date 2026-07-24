package tui

import (
	tea "charm.land/bubbletea/v2"

	ui "github.com/Nebutra/carina/go/tui/ui"
)

type componentKeyInput struct {
	Key  string
	Text string
}

func (m *Model) dispatchComponentEvent(event ui.Event) (tea.Cmd, bool) {
	if m.componentRuntime == nil {
		return nil, false
	}
	m.refreshComponentFrame()
	result := m.componentRuntime.Dispatch(event)
	cmd := m.applyUIResult(result)
	handled := result.Handled || len(result.Actions) > 0 || len(result.Effects) > 0
	if handled {
		m.refreshComponentFrame()
	}
	return cmd, handled
}

// dispatchComponentPointer is the single Tea-to-component pointer boundary.
// The active surface publishes geometry first, then the runtime resolves the
// target and bubbles the event through the retained component tree.
func (m *Model) dispatchComponentPointer(msg tea.MouseMsg) (tea.Cmd, bool) {
	if m.componentRuntime == nil {
		return nil, false
	}
	m.refreshComponentFrame()
	event, ok := translateTeaPointer(msg, m.componentFrame.Generation)
	if !ok {
		return nil, false
	}
	result := m.componentRuntime.Dispatch(event)
	handled := result.Handled || len(result.Actions) > 0 || len(result.Effects) > 0
	cmd := m.applyUIResult(result)
	if handled {
		m.refreshComponentFrame()
	}
	return cmd, handled
}

func (m *Model) dispatchComponentKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if m.componentRuntime == nil {
		return nil, false
	}
	resolved, chordCmd, chordConsumed := m.resolveChordKey(msg.String())
	if chordConsumed {
		m.pasteBurst.reset()
		return chordCmd, true
	}
	m.maintainConfirmationStateForKey(resolved)
	if resolved != msg.String() {
		m.pasteBurst.reset()
		msg = tea.KeyPressMsg{Text: resolved}
	}
	m.refreshComponentFrame()
	input := componentKeyInput{Key: msg.String(), Text: msg.Key().Text}
	result := m.componentRuntime.Dispatch(ui.Event{Kind: ui.EventKey, Key: input.Key, Text: input.Text})
	for _, action := range result.Actions {
		if (action.Source != conversationScreenID || action.Name != "conversation-key") &&
			(action.Source != conversationComposerID || action.Name != "composer-key") {
			continue
		}
		cmd := m.applyComposerKey(msg)
		m.refreshComponentFrame()
		return cmd, true
	}
	cmd := m.applyUIResult(result)
	handled := result.Handled || len(result.Actions) > 0 || len(result.Effects) > 0
	if handled {
		m.refreshComponentFrame()
	}
	return cmd, handled
}

func (m *Model) dispatchComponentPaste(content string) (tea.Cmd, bool) {
	m.pasteBurst.reset()
	if m.componentRuntime == nil {
		return nil, false
	}
	m.refreshComponentFrame()
	result := m.componentRuntime.Dispatch(ui.Event{Kind: ui.EventPaste, Text: content})
	for _, action := range result.Actions {
		if (action.Source == conversationScreenID && action.Name == "conversation-paste") ||
			(action.Source == conversationComposerID && action.Name == "composer-paste") {
			cmd := m.applyConversationPaste(content)
			m.refreshComponentFrame()
			return cmd, true
		}
	}
	cmd := m.applyUIResult(result)
	handled := result.Handled || len(result.Actions) > 0 || len(result.Effects) > 0
	if handled {
		m.refreshComponentFrame()
	}
	return cmd, handled
}

func (m *Model) applyComposerKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.historySearch != nil {
		m.pasteBurst.reset()
		return m.historySearchKeyPress(msg)
	}
	composerAvailable := m.approval == nil && m.question == nil && m.activePrimaryOverlayKind() == primaryOverlayNone &&
		m.transcriptPager == nil && m.sessionPicker == nil
	if composerAvailable && m.submitting != nil && !submissionHasIndependentComposer(m.submitting) && m.keyStartsSubmissionTypeAhead(msg) {
		m.beginSubmissionTypeAhead()
	}
	blocked := m.submitting != nil && !submissionHasIndependentComposer(m.submitting)
	if blocked {
		m.pasteBurst.reset()
		if cmd, handled := m.handleKey(key); handled {
			return cmd
		}
		return nil
	}
	before := m.composerSnapshot()
	if cmd, handled := m.handlePasteBurstKey(msg); handled {
		m.recordComposerEdit(before, composerEditTyping)
		return cmd
	}
	if cmd, handled := m.handleKey(key); handled {
		return cmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.snapVerticalGraphemeEditorKey(key)
	if msg.Text == "!" || key == "!" {
		m.absorbLoneBangIfNeeded()
	}
	m.layout()
	m.recordComposerEdit(before, composerKeyEditKind(msg))
	return tea.Batch(cmd, m.refreshSuggestTrigger())
}

func (m *Model) applyComposerMessage(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.layout()
	return tea.Batch(cmd, m.refreshSuggestTrigger())
}

func translateTeaPointer(msg tea.MouseMsg, generation uint64) (ui.Event, bool) {
	mouse := msg.Mouse()
	pointer := ui.PointerEvent{X: mouse.X, Y: mouse.Y, Button: int(mouse.Button)}
	switch typed := msg.(type) {
	case tea.MouseMotionMsg:
		pointer.Kind = ui.PointerMove
	case tea.MouseClickMsg:
		if typed.Button != tea.MouseLeft {
			return ui.Event{}, false
		}
		pointer.Kind = ui.PointerClick
	case tea.MouseReleaseMsg:
		pointer.Kind = ui.PointerRelease
	case tea.MouseWheelMsg:
		pointer.Kind = ui.PointerWheel
		switch typed.Button {
		case tea.MouseWheelUp:
			pointer.WheelDelta = -1
		case tea.MouseWheelDown:
			pointer.WheelDelta = 1
		default:
			return ui.Event{}, false
		}
	default:
		return ui.Event{}, false
	}
	return ui.Event{Kind: ui.EventPointer, Pointer: pointer, FrameGeneration: generation}, true
}

func (m *Model) applyUIResult(result ui.Result) tea.Cmd {
	commands := make([]tea.Cmd, 0, len(result.Actions))
	for _, action := range result.Actions {
		if action.Name == "transcript-action" {
			if data, ok := action.Data.(transcriptComponentAction); ok {
				commands = append(commands, m.applyTranscriptComponentAction(data))
			}
			continue
		}
		if action.Name == "transcript-toggle" {
			if key, ok := action.Data.(string); ok && m.tr.toggleCollapsible(key, m.th, m.transcriptWidth()) {
				m.layout()
			}
			continue
		}
		switch action.Source {
		case governanceOverlayID:
			switch action.Name {
			case "governance-key":
				if input, ok := action.Data.(componentKeyInput); ok {
					cmd, _ := m.governanceDomainKey(input)
					commands = append(commands, cmd)
				}
			case "question-paste":
				if text, ok := action.Data.(string); ok && m.question != nil && len(m.question.Options) == 0 && !m.question.Resolving {
					m.appendQuestionText(text)
				}
			case "approval-once":
				commands = append(commands, m.resolveApproval("once", true))
			case "approval-session":
				commands = append(commands, m.resolveApproval("session", true))
			case "approval-project":
				commands = append(commands, m.resolveApproval("project", true))
			case "approval-deny":
				commands = append(commands, m.resolveApproval("deny", false))
			case "question-option":
				if index, ok := action.Data.(int); ok {
					commands = append(commands, m.answerQuestion(index))
				}
			case "question-answer":
				commands = append(commands, m.answerQuestionText())
			}
		case primaryOverlayID:
			commands = append(commands, m.applyPrimaryOverlayResult(ui.Result{Actions: []ui.Action{action}}))
		case sessionNavigatorID:
			commands = append(commands, m.applyNavigatorResult(ui.Result{Actions: []ui.Action{action}}))
		case operationalPagerID:
			switch action.Name {
			case "operational-key":
				if input, ok := action.Data.(componentKeyInput); ok {
					cmd, _ := m.transcriptPagerKey(input.Key)
					commands = append(commands, cmd)
				}
			case "refresh":
				commands = append(commands, m.refreshOperationalPager())
			case "close":
				commands = append(commands, m.closeTranscriptPager())
			}
		case composerAttachmentsID:
			m.applyAttachmentAction(action)
		case conversationTranscriptID:
			if action.Name == "scroll-transcript" {
				m.scrollConversationTranscript(action.Data)
			}
		case conversationScreenID:
			// Resize/focus/blur are already reflected in Model state before the
			// presentation event is dispatched. The root action records component
			// ownership without introducing a second terminal lifecycle reducer.
		}
	}
	return tea.Batch(commands...)
}

func (m *Model) applyTranscriptComponentAction(action transcriptComponentAction) tea.Cmd {
	switch action.Name {
	case "toggle":
		if m.tr.toggleCollapsible(action.Key, m.th, m.transcriptWidth()) {
			m.layout()
		}
	case "inspect":
		m.openTranscriptEntryPager(action.Key)
	case "copy":
		return m.copyTranscriptEntry(action.Key)
	case "edit":
		return m.beginBacktrackEntryEdit(action.Key)
	case "open":
		m.openTranscriptArtifactPager(action.Key, action.ArtifactIDs)
	case "cancel":
		if action.TaskID != "" && action.TaskID == m.inFlightTaskID {
			return m.cancelTask(action.TaskID)
		}
	}
	return nil
}

func (m *Model) governanceDomainKey(input componentKeyInput) (tea.Cmd, bool) {
	if m.question != nil {
		if len(m.question.Options) == 0 {
			if cmd, handled := m.questionKeyText(input.Key, input.Text); handled {
				return cmd, true
			}
		} else if cmd, handled := m.questionKey(input.Key); handled {
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, input.Key) {
			return tea.ClearScreen, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, input.Key) {
			return m.ctrlC(), true
		}
		return nil, true
	}
	if m.approval != nil {
		if cmd, handled := m.approvalKey(input.Key); handled {
			return cmd, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, input.Key) {
			return tea.ClearScreen, true
		}
		if m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, input.Key) {
			return m.ctrlC(), true
		}
		return nil, true
	}
	return nil, false
}

func (m *Model) applyConversationPaste(content string) tea.Cmd {
	if m.editor != nil {
		return nil
	}
	if m.historySearch != nil {
		m.appendHistorySearchQuery(content)
		return nil
	}
	if m.submitting != nil && content != "" && !submissionHasIndependentComposer(m.submitting) {
		m.beginSubmissionTypeAhead()
	}
	before := m.composerSnapshot()
	pasteCount := len(m.pendingPaste)
	cmd := m.handlePaste(tea.PasteMsg{Content: content})
	if len(m.pendingPaste) > pasteCount {
		m.composerExternalMutation()
	} else {
		m.recordComposerEdit(before, composerEditPaste)
	}
	return cmd
}

func (m *Model) applyAttachmentAction(action ui.Action) {
	switch action.Name {
	case "attachment-leave":
		m.attachmentHoverID = ""
		m.syncAttachmentPreviewOwner()
	case "attachment-preview", "attachment-select":
		hit, ok := action.Data.(attachmentHit)
		if !ok || hit.Index < 0 || hit.Index >= len(m.attachments) || m.attachments[hit.Index].ID != hit.ID {
			return
		}
		m.attachmentHoverID = hit.ID
		if action.Name == "attachment-select" {
			m.attachmentFocus = hit.Index
		}
		m.syncAttachmentPreviewOwner()
		m.prepareAttachmentPreview()
	}
}

func (m *Model) scrollConversationTranscript(data any) {
	delta, ok := data.(int)
	if !ok || delta == 0 {
		return
	}
	if delta < 0 {
		m.vp.ScrollUp(3)
	} else {
		m.vp.ScrollDown(3)
	}
	m.followTail = m.vp.AtBottom()
	if m.followTail {
		m.unseenLines = 0
	}
}
