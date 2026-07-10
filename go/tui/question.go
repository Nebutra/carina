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
	if q.QuestionID == "" || q.Prompt == "" || len(q.Options) == 0 {
		return nil
	}
	return q
}

func (m *Model) answerQuestion(index int) tea.Cmd {
	q, call := m.question, m.call
	if q == nil || index < 0 || index >= len(q.Options) {
		return nil
	}
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

func (m *Model) handleQuestionDone(msg questionDoneMsg) {
	if msg.err != nil {
		m.push(fmt.Sprintf("%s answer failed for question %s: %s", glyphFailed(m.th), msg.questionID, msg.err.Error()))
		return
	}
	m.questionResolved[msg.questionID] = true
	m.push(fmt.Sprintf("%s answered %s: %s", glyphOK(m.th), msg.questionID, msg.label))
	if msg.taskID != "" {
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
		ev := m.approvalQueue[0]
		m.approvalQueue = m.approvalQueue[1:]
		m.approval = m.buildApprovalState(ev)
	}
}

func (m *Model) questionKey(key string) (tea.Cmd, bool) {
	if m.question == nil {
		return nil, false
	}
	n, err := strconv.Atoi(key)
	if err == nil && n >= 1 && n <= len(m.question.Options) && n <= 9 {
		return m.answerQuestion(n - 1), true
	}
	return nil, true
}

func (m *Model) questionOverlayView() string {
	q := m.question
	if q == nil {
		return ""
	}
	outerWidth := maxInt(m.width, 1)
	contentWidth := outerWidth - 6
	if contentWidth < 1 {
		contentWidth = 1
	}
	var lines []string
	lines = append(lines, fitLine(m.th.Style(theme.RoleWarning).Render("Agent needs input"), contentWidth))
	lines = append(lines, "")
	for _, line := range strings.Split(ansi.Wordwrap(q.Prompt, contentWidth, ""), "\n") {
		lines = append(lines, fitLine(line, contentWidth))
	}
	lines = append(lines, "")
	for i, option := range q.Options {
		if i >= 9 {
			break
		}
		line := fmt.Sprintf("[%d] %s", i+1, option.Label)
		if option.Description != "" {
			line += " - " + option.Description
		}
		lines = append(lines, fitLine(line, contentWidth))
	}
	lines = append(lines, "", fitLine(m.th.Style(theme.RoleMuted).Render("Press a number to answer"), contentWidth))
	if m.ctrlCHint != "" {
		lines = append(lines, fitLine(m.th.Style(theme.RoleMuted).Render(m.ctrlCHint), contentWidth))
	}

	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1).Width(contentWidth)
	if c := m.th.Color(theme.RoleWarning); c != nil {
		style = style.BorderForeground(c)
	}
	return style.Render(strings.Join(lines, "\n"))
}
