package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
)

type externalEditorSession struct {
	generation int
	draft      externalEditorDraft
}

type transcriptPagerState struct {
	text   string
	scroll int
}

// handleWorkspaceKey is the one mapping boundary for product-level composer
// actions. A semantic keymap can replace these strings later without touching
// queue, editor, clipboard, or pager state transitions.
func (m *Model) handleWorkspaceKey(key string) (tea.Cmd, bool) {
	switch {
	case m.keys.matches(KeyContextComposer, ActionComposerQueue, key):
		if m.inFlightTaskID != "" {
			return nil, m.enqueueFollowUp()
		}
	case m.keys.matches(KeyContextComposer, ActionComposerRecallQueue, key):
		return nil, m.recallLastFollowUp()
	case m.keys.matches(KeyContextComposer, ActionComposerExternalEditor, key):
		return m.beginExternalEditor(m.currentDraft()), true
	case m.keys.matches(KeyContextGlobal, ActionGlobalTranscript, key):
		m.openTranscriptPager()
		return nil, true
	}
	return nil, false
}

func (m *Model) beginExternalEditor(draft promptDraft) tea.Cmd {
	if m.editor != nil || m.submitting != nil || m.approval != nil || m.question != nil {
		return nil
	}
	state, process, err := prepareExternalEditor(draft, m.getenv)
	if err != nil {
		m.restoreDraft(draft)
		m.push(fmt.Sprintf("%s external editor: %s", glyphFailed(m.th), err.Error()))
		return nil
	}
	m.editorGen++
	generation := m.editorGen
	m.editor = &externalEditorSession{generation: generation, draft: state}
	if m.suggest != nil {
		m.closeSuggest()
	}
	m.layout()
	return tea.ExecProcess(process, func(err error) tea.Msg {
		return externalEditorDoneMsg{generation: generation, err: err}
	})
}

func (m *Model) handleExternalEditorDone(msg externalEditorDoneMsg) tea.Cmd {
	session := m.editor
	if session == nil || session.generation != msg.generation {
		return nil
	}
	m.editor = nil
	draft, err := finishExternalEditor(session.draft, msg.err)
	m.restoreDraft(draft)
	if err != nil {
		m.push(fmt.Sprintf("%s %s; draft restored", glyphFailed(m.th), err.Error()))
	} else {
		m.resetComposerUndo()
		m.push(m.th.Style(theme.RoleMuted).Render("- external editor draft applied"))
	}
	m.layout()
	if m.queueRestoreReason != "" {
		reason := m.queueRestoreReason
		m.queueRestoreReason = ""
		m.restoreQueuedDrafts(reason)
		return nil
	}
	return m.maybeSubmitNextQueued()
}

func (m *Model) copyLastAgentProjection() tea.Cmd {
	text := m.tr.lastAgentText()
	if text == "" {
		m.push(fmt.Sprintf("%s nothing to copy: no rendered agent response", glyphFailed(m.th)))
		return nil
	}
	write := m.clipboardWrite
	return func() tea.Msg {
		return clipboardDoneMsg{err: write(text)}
	}
}

func (m *Model) handleClipboardDone(msg clipboardDoneMsg) {
	if msg.err != nil {
		m.push(fmt.Sprintf("%s copy failed: %s", glyphFailed(m.th), msg.err.Error()))
		return
	}
	m.push(m.th.Style(theme.RoleMuted).Render("- copied last agent response"))
}

func systemClipboardWrite(text string) error {
	type candidate struct {
		name string
		args []string
	}
	var candidates []candidate
	switch runtime.GOOS {
	case "darwin":
		candidates = []candidate{{name: "pbcopy"}}
	case "windows":
		candidates = []candidate{{name: "cmd", args: []string{"/c", "clip"}}}
	default:
		candidates = []candidate{
			{name: "wl-copy"},
			{name: "xclip", args: []string{"-selection", "clipboard"}},
			{name: "xsel", args: []string{"--clipboard", "--input"}},
		}
	}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, candidate.args...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", candidate.name, err)
		}
		return nil
	}
	return fmt.Errorf("no clipboard helper found")
}

func (m *Model) openTranscriptPager() {
	m.closeSuggest()
	text := m.tr.plainText()
	if text == "" {
		text = "(transcript is empty)"
	}
	m.transcriptPager = &transcriptPagerState{text: text}
	m.layout()
}

func (m *Model) closeTranscriptPager() {
	m.transcriptPager = nil
	m.layout()
}

func (m *Model) transcriptPagerLines() []string {
	if m.transcriptPager == nil {
		return nil
	}
	width := maxInt(m.width, 1)
	var lines []string
	for _, line := range strings.Split(m.transcriptPager.text, "\n") {
		wrapped := ansi.Hardwrap(line, width, true)
		lines = append(lines, strings.Split(wrapped, "\n")...)
	}
	return lines
}

func (m *Model) transcriptPagerPageHeight() int {
	return maxInt(m.height-2, 1)
}

func (p *transcriptPagerState) scrollBy(delta int) {
	p.scroll += delta
	if p.scroll < 0 {
		p.scroll = 0
	}
}

func (m *Model) clampTranscriptPagerScroll(lines []string) {
	if m.transcriptPager == nil {
		return
	}
	maxScroll := maxInt(len(lines)-m.transcriptPagerPageHeight(), 0)
	m.transcriptPager.scroll = clampInt(m.transcriptPager.scroll, 0, maxScroll)
}

func (m *Model) transcriptPagerKey(key string) (tea.Cmd, bool) {
	lines := m.transcriptPagerLines()
	page := m.transcriptPagerPageHeight()
	switch {
	case m.keys.matches(KeyContextPager, ActionPagerClose, key),
		m.keys.matches(KeyContextGlobal, ActionGlobalTranscript, key):
		m.closeTranscriptPager()
	case m.keys.matches(KeyContextPager, ActionPagerUp, key):
		m.transcriptPager.scroll--
	case m.keys.matches(KeyContextPager, ActionPagerDown, key):
		m.transcriptPager.scroll++
	case m.keys.matches(KeyContextPager, ActionPagerPageUp, key):
		m.transcriptPager.scroll -= page
	case m.keys.matches(KeyContextPager, ActionPagerPageDown, key):
		m.transcriptPager.scroll += page
	case m.keys.matches(KeyContextPager, ActionPagerTop, key):
		m.transcriptPager.scroll = 0
	case m.keys.matches(KeyContextPager, ActionPagerBottom, key):
		m.transcriptPager.scroll = len(lines)
	default:
		return nil, true
	}
	m.clampTranscriptPagerScroll(lines)
	return nil, true
}

func (m *Model) transcriptPagerView(width, height int) string {
	if m.transcriptPager == nil || width <= 0 || height <= 0 {
		return ""
	}
	if height == 1 {
		return fitRenderedLine("transcript - esc closes", width)
	}
	lines := m.transcriptPagerLines()
	m.clampTranscriptPagerScroll(lines)
	page := maxInt(height-2, 0)
	start := m.transcriptPager.scroll
	end := minInt(start+page, len(lines))
	visible := append([]string(nil), lines[start:end]...)
	header := fitRenderedLine(fmt.Sprintf("transcript · %d lines", len(lines)), width)
	footer := fitRenderedLine(fmt.Sprintf("%s/%s scroll · %s/%s page · %s close",
		m.keys.label(KeyContextPager, ActionPagerUp),
		m.keys.label(KeyContextPager, ActionPagerDown),
		m.keys.label(KeyContextPager, ActionPagerPageUp),
		m.keys.label(KeyContextPager, ActionPagerPageDown),
		m.keys.label(KeyContextPager, ActionPagerClose)), width)
	out := []string{header}
	out = append(out, visible...)
	for len(out) < height-1 {
		out = append(out, "")
	}
	out = append(out, footer)
	return strings.Join(out, "\n")
}
