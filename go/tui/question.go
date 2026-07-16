package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

type questionOption struct {
	Label       string
	Value       string
	Description string
}

type questionState struct {
	QuestionID string
	TaskID     string
	Prompt     string
	Options    []questionOption
	Selected   int
	Scroll     int
	Resolving  bool
	Error      string
	FreeText   string
}

type questionDoneMsg struct {
	questionID string
	taskID     string
	label      string
	value      string
	err        error
}

func (m *Model) openQuestion(ev map[string]any) {
	q := buildQuestionState(ev)
	if q == nil || m.questionResolved[q.QuestionID] || m.questionSeen[q.QuestionID] {
		return
	}
	m.questionSeen[q.QuestionID] = true
	if q.TaskID != "" {
		m.inFlightTaskID = q.TaskID
	}
	if m.question != nil || m.approval != nil {
		m.questionQueue = append(m.questionQueue, ev)
		return
	}
	m.question = q
}

func (m *Model) observeQuestionResolution(ev map[string]any) {
	payload, _ := ev["payload"].(map[string]any)
	if str(payload["status"]) != "user_question_resolved" {
		return
	}
	questionID := str(payload["question_id"])
	if questionID == "" {
		return
	}
	m.questionResolved[questionID] = true
	filtered := m.questionQueue[:0]
	for _, queued := range m.questionQueue {
		if str(queued["question_id"]) != questionID {
			filtered = append(filtered, queued)
		}
	}
	m.questionQueue = filtered
	if m.question != nil && m.question.QuestionID == questionID {
		m.nextQueuedQuestion()
	}
}

func buildQuestionState(ev map[string]any) *questionState {
	q := &questionState{
		QuestionID: str(ev["question_id"]),
		TaskID:     str(ev["task_id"]),
		Prompt:     sanitize(str(ev["prompt"])),
	}
	var options []map[string]any
	switch rawOptions := ev["options"].(type) {
	case []any:
		for _, raw := range rawOptions {
			if item, ok := raw.(map[string]any); ok {
				options = append(options, item)
			}
		}
	case []map[string]any:
		options = append(options, rawOptions...)
	}
	for _, item := range options {
		option := questionOption{
			Label:       sanitize(str(item["label"])),
			Value:       sanitize(str(item["value"])),
			Description: sanitize(str(item["description"])),
		}
		if option.Label == "" {
			option.Label = option.Value
		}
		if option.Value != "" {
			q.Options = append(q.Options, option)
		}
	}
	if q.QuestionID == "" || q.Prompt == "" {
		return nil
	}
	return q
}

func (m *Model) answerQuestion(index int) tea.Cmd {
	q, call := m.question, m.call
	if q == nil || q.Resolving || index < 0 || index >= len(q.Options) {
		return nil
	}
	q.Selected = index
	q.Resolving = true
	q.Error = ""
	option := q.Options[index]
	return func() tea.Msg {
		if call == nil {
			return questionDoneMsg{questionID: q.QuestionID, taskID: q.TaskID, err: errors.New("daemon not connected")}
		}
		if err := call.Call("task.user.answer", map[string]any{
			"question_id": q.QuestionID,
			"value":       option.Value,
		}, nil); err != nil {
			return questionDoneMsg{questionID: q.QuestionID, taskID: q.TaskID, err: err}
		}
		return questionDoneMsg{
			questionID: q.QuestionID,
			taskID:     q.TaskID,
			label:      option.Label,
			value:      option.Value,
		}
	}
}

func (m *Model) answerQuestionText() tea.Cmd {
	q, call := m.question, m.call
	if q == nil || q.Resolving || len(q.Options) != 0 {
		return nil
	}
	value := strings.TrimSpace(q.FreeText)
	if value == "" {
		q.Error = m.text(MsgQuestionFreeTextRequired, nil)
		return nil
	}
	q.Resolving = true
	q.Error = ""
	questionID, taskID := q.QuestionID, q.TaskID
	return func() tea.Msg {
		if call == nil {
			return questionDoneMsg{questionID: questionID, taskID: taskID, err: errors.New("daemon not connected")}
		}
		if err := call.Call("task.user.answer", map[string]any{"question_id": questionID, "value": value}, nil); err != nil {
			return questionDoneMsg{questionID: questionID, taskID: taskID, err: err}
		}
		return questionDoneMsg{questionID: questionID, taskID: taskID, label: value, value: value}
	}
}

func (m *Model) handleQuestionDone(msg questionDoneMsg) {
	// A server event may resolve this question while the answer RPC is still
	// in flight. Never let that stale completion close a newer overlay.
	if m.question == nil || m.question.QuestionID != msg.questionID {
		return
	}
	if msg.err != nil {
		m.question.Resolving = false
		m.question.Error = m.text(MsgQuestionAnswerFailed, MessageArgs{
			"error": msg.err.Error(),
			"retry": primaryKeyLabel(m.keys.keys(KeyContextQuestion, ActionQuestionAnswer)),
		})
		m.push(m.text(MsgQuestionAnswerLogFail, MessageArgs{
			"glyph": glyphFailed(m.th), "id": msg.questionID, "error": msg.err.Error(),
		}))
		return
	}
	m.questionResolved[msg.questionID] = true
	m.push(m.text(MsgQuestionAnswered, MessageArgs{
		"glyph": glyphOK(m.th), "id": msg.questionID, "label": msg.label,
	}))
	if msg.taskID != "" && msg.taskID == m.inFlightTaskID {
		m.tasks.setTask(msg.taskID, "running")
	}
	m.nextQueuedQuestion()
}

func (m *Model) nextQueuedQuestion() {
	m.question = nil
	if len(m.questionQueue) > 0 {
		ev := m.questionQueue[0]
		m.questionQueue = m.questionQueue[1:]
		m.question = buildQuestionState(ev)
		if m.question != nil {
			return
		}
	}
	if len(m.approvalQueue) > 0 {
		m.nextQueuedApproval()
	}
}

func (m *Model) questionKey(key string) (tea.Cmd, bool) {
	return m.questionKeyText(key, key)
}

func (m *Model) questionKeyText(key, text string) (tea.Cmd, bool) {
	if m.question == nil {
		return nil, false
	}
	q := m.question
	if q.Resolving {
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key) ||
			m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			return nil, false
		}
		return nil, true
	}
	if len(q.Options) == 0 {
		if m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key) ||
			m.keys.matches(KeyContextGlobal, ActionGlobalInterrupt, key) {
			return nil, false
		}
		switch key {
		case "enter":
			return m.answerQuestionText(), true
		case "esc":
			q.Error = m.text(MsgQuestionCannotDismiss, nil)
			return nil, true
		case "backspace", "ctrl+h":
			q.FreeText = dropLastGrapheme(q.FreeText)
			q.Error = ""
			return nil, true
		}
		if value, ok := historySearchInput(text); ok {
			q.FreeText += value
			q.Error = ""
		}
		return nil, true
	}
	switch {
	case m.keys.matches(KeyContextQuestion, ActionQuestionAnswer, key):
		n, err := strconv.Atoi(key)
		if err == nil && n >= 1 && n <= len(q.Options) && n <= 9 {
			return m.answerQuestion(n - 1), true
		}
		return m.answerQuestion(q.Selected), true
	case m.keys.matches(KeyContextQuestion, ActionQuestionPrevious, key):
		q.Selected = (q.Selected - 1 + len(q.Options)) % len(q.Options)
		m.ensureQuestionSelectionVisible()
	case m.keys.matches(KeyContextQuestion, ActionQuestionNext, key):
		q.Selected = (q.Selected + 1) % len(q.Options)
		m.ensureQuestionSelectionVisible()
	case m.keys.matches(KeyContextQuestion, ActionQuestionPageUp, key):
		q.Scroll -= m.questionViewportHeight()
		m.clampQuestionScroll()
	case m.keys.matches(KeyContextQuestion, ActionQuestionPageDown, key):
		q.Scroll += m.questionViewportHeight()
		m.clampQuestionScroll()
	case m.keys.matches(KeyContextQuestion, ActionQuestionTop, key):
		q.Scroll = 0
	case m.keys.matches(KeyContextQuestion, ActionQuestionBottom, key):
		q.Scroll = len(m.questionBodyLines())
		m.clampQuestionScroll()
	case m.keys.matches(KeyContextQuestion, ActionQuestionCancel, key):
		// task.user.answer has no cancellation counterpart. Keep the pending
		// server question visible instead of silently orphaning the task.
		q.Error = m.text(MsgQuestionCannotDismiss, nil)
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) appendQuestionText(value string) {
	if m.question == nil || len(m.question.Options) != 0 || m.question.Resolving {
		return
	}
	value = sanitize(value)
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	if value == "" {
		return
	}
	m.question.FreeText += value
	m.question.Error = ""
}

func (m *Model) questionOverlayView() string {
	q := m.question
	if q == nil {
		return ""
	}
	outerWidth := maxInt(m.width, 1)
	contentWidth := outerWidth - 8
	if contentWidth < 1 {
		contentWidth = 1
	}
	body := m.questionBodyLines()
	m.clampQuestionScroll()
	start := q.Scroll
	if len(q.Options) == 0 {
		// Keep the editable answer anchored in very short terminals. The prompt
		// remains reachable by resizing; submitting must never become invisible.
		start = maxInt(len(body)-m.questionViewportHeight(), 0)
	}
	end := minInt(start+m.questionViewportHeight(), len(body))

	lines := []string{fitRenderedLine(m.th.Style(theme.RoleWarning).Render(m.text(MsgQuestionTitle, nil)), contentWidth), ""}
	for _, line := range body[start:end] {
		lines = append(lines, fitLine(line, contentWidth))
	}
	footer := m.text(MsgQuestionFooterWide, MessageArgs{
		"previous": m.keys.label(KeyContextQuestion, ActionQuestionPrevious),
		"next":     m.keys.label(KeyContextQuestion, ActionQuestionNext),
		"answer":   m.keys.label(KeyContextQuestion, ActionQuestionAnswer),
	})
	if len(q.Options) == 0 {
		footer = m.text(MsgQuestionFreeTextFooter, nil)
	}
	if contentWidth < 44 {
		footer = m.text(MsgQuestionFooterMedium, MessageArgs{
			"previous": primaryKeyLabel(m.keys.keys(KeyContextQuestion, ActionQuestionPrevious)),
			"next":     primaryKeyLabel(m.keys.keys(KeyContextQuestion, ActionQuestionNext)),
			"answer":   primaryKeyLabel(m.keys.keys(KeyContextQuestion, ActionQuestionAnswer)),
		})
	}
	if contentWidth < 28 {
		footer = m.text(MsgQuestionFooterNarrow, MessageArgs{
			"answer": primaryKeyLabel(m.keys.keys(KeyContextQuestion, ActionQuestionAnswer)),
		})
	}
	if q.Resolving {
		footer = m.text(MsgQuestionSending, nil)
	} else if len(body) > m.questionViewportHeight() {
		if contentWidth >= 44 {
			footer += m.text(MsgQuestionScroll, MessageArgs{
				"page_up":   m.keys.label(KeyContextQuestion, ActionQuestionPageUp),
				"page_down": m.keys.label(KeyContextQuestion, ActionQuestionPageDown),
				"start":     start + 1, "end": end, "total": len(body),
			})
		} else {
			footer += fmt.Sprintf("  %d-%d/%d", start+1, end, len(body))
		}
	}
	if q.Error != "" {
		lines = append(lines, fitRenderedLine(m.th.Style(theme.RoleError).Render(sanitize(q.Error)), contentWidth))
	}
	if m.ctrlCHint != "" {
		lines = append(lines, fitRenderedLine(m.th.Style(theme.RoleMuted).Render(m.ctrlCHint), contentWidth))
	}
	// Footer remains the final content row so the root modal clipper can keep
	// the primary action visible in extremely short terminals.
	lines = append(lines, "", fitRenderedLine(m.th.Style(theme.RoleMuted).Render(footer), contentWidth))

	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1)
	if c := m.th.Color(theme.RoleWarning); c != nil {
		style = style.BorderForeground(c)
	}
	return style.Render(strings.Join(lines, "\n"))
}

func (m *Model) questionContentWidth() int {
	return maxInt(m.width-8, 1)
}

func (m *Model) questionBodyLines() []string {
	q := m.question
	if q == nil {
		return nil
	}
	width := m.questionContentWidth()
	lines := wrappedOverlayLines(q.Prompt, width)
	if m.conn != ConnConnected {
		lines = append(lines, m.text(MsgOverlayDisconnected, nil))
	}
	lines = append(lines, "")
	if len(q.Options) == 0 {
		lines = append(lines, wrappedOverlayLines(m.text(MsgQuestionFreeTextHint, nil), width)...)
		answer := q.FreeText
		if answer == "" {
			answer = " "
		}
		lines = append(lines, wrappedOverlayLines("> "+answer+"|", width)...)
		return lines
	}
	for i, option := range q.Options {
		marker := "  "
		if i == q.Selected {
			marker = "> "
		}
		number := "    "
		if i < 9 {
			number = fmt.Sprintf("[%d] ", i+1)
		}
		line := marker + number + option.Label
		if option.Description != "" {
			line += " - " + option.Description
		}
		wrapped := wrappedOverlayLines(line, width)
		lines = append(lines, wrapped...)
	}
	return lines
}

func wrappedOverlayLines(value string, width int) []string {
	wrapped := ansi.Wordwrap(sanitize(value), maxInt(width, 1), "")
	lines := strings.Split(wrapped, "\n")
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func (m *Model) questionViewportHeight() int {
	reserved := 6 // border, title, spacing, and anchored footer
	if m.question != nil && m.question.Error != "" {
		reserved++
	}
	if m.ctrlCHint != "" {
		reserved++
	}
	return maxInt(m.height-reserved, 1)
}

func (m *Model) clampQuestionScroll() {
	if m.question == nil {
		return
	}
	maxScroll := maxInt(len(m.questionBodyLines())-m.questionViewportHeight(), 0)
	if m.question.Scroll < 0 {
		m.question.Scroll = 0
	}
	if m.question.Scroll > maxScroll {
		m.question.Scroll = maxScroll
	}
}

func (m *Model) ensureQuestionSelectionVisible() {
	q := m.question
	if q == nil {
		return
	}
	width := m.questionContentWidth()
	line := len(wrappedOverlayLines(q.Prompt, width)) + 1
	selectedStart, selectedEnd := line, line
	for i, option := range q.Options {
		number := "    "
		if i < 9 {
			number = fmt.Sprintf("[%d] ", i+1)
		}
		value := "  " + number + option.Label
		if option.Description != "" {
			value += " - " + option.Description
		}
		height := len(wrappedOverlayLines(value, width))
		if i == q.Selected {
			selectedStart, selectedEnd = line, line+height-1
			break
		}
		line += height
	}
	viewportHeight := m.questionViewportHeight()
	if selectedStart < q.Scroll {
		q.Scroll = selectedStart
	}
	if selectedEnd >= q.Scroll+viewportHeight {
		q.Scroll = selectedEnd - viewportHeight + 1
	}
	m.clampQuestionScroll()
}
