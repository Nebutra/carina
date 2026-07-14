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
	original   composerSnapshot
	holdsQueue bool
}

type transcriptPagerState struct {
	scroll int
}

// handleWorkspaceKey owns focused composer actions. Global actions stay in
// handleKey's final switch so focused bindings always win overlapping keys.
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
	}
	return nil, false
}

func (m *Model) beginExternalEditor(draft promptDraft) tea.Cmd {
	return m.beginExternalEditorWithSnapshot(draft, m.composerSnapshot())
}

func (m *Model) beginExternalEditorWithSnapshot(draft promptDraft, original composerSnapshot) tea.Cmd {
	if m.editor != nil || m.submitting != nil || m.approval != nil || m.question != nil {
		return nil
	}
	m.breakComposerUndoGroup()
	state, process, err := prepareExternalEditor(draft, m.getenv)
	if err != nil {
		m.push(fmt.Sprintf("%s external editor: %s", glyphFailed(m.th), err.Error()))
		return nil
	}
	m.editorGen++
	generation := m.editorGen
	m.editor = &externalEditorSession{
		generation: generation,
		draft:      state,
		original:   original,
		holdsQueue: m.queueRecallPending,
	}
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
	if err != nil {
		m.restoreComposerSnapshot(session.original)
		m.push(fmt.Sprintf("%s %s; draft restored", glyphFailed(m.th), err.Error()))
	} else {
		m.restoreDraft(draft)
		m.recordComposerEdit(session.original, composerEditOther)
		m.push(m.th.Style(theme.RoleMuted).Render("- external editor draft applied"))
	}
	m.layout()
	if m.queueRestoreReason != "" {
		return m.resumeQueuedAfterTransient()
	}
	if session.holdsQueue {
		m.queueRecallPending = true
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
	var failures []string
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, candidate.args...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", candidate.name, err.Error()))
			continue
		}
		return nil
	}
	if len(failures) > 0 {
		return fmt.Errorf("clipboard helpers failed: %s", strings.Join(failures, "; "))
	}
	return fmt.Errorf("no clipboard helper found")
}

func (m *Model) openTranscriptPager() {
	m.breakComposerUndoGroup()
	m.closeSuggest()
	m.transcriptPager = &transcriptPagerState{}
	m.layout()
}

func (m *Model) closeTranscriptPager() tea.Cmd {
	m.transcriptPager = nil
	m.layout()
	return m.resumeQueuedAfterTransient()
}

func (m *Model) transcriptPagerLines() []string {
	if m.transcriptPager == nil {
		return nil
	}
	text := m.tr.plainText()
	if text == "" {
		text = "(transcript is empty)"
	}
	width := maxInt(m.width, 1)
	var lines []string
	for _, line := range strings.Split(text, "\n") {
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
		return m.closeTranscriptPager(), true
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
	case m.keys.matches(KeyContextGlobal, ActionGlobalRedraw, key):
		return tea.ClearScreen, true
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
		return fitRenderedLine(fmt.Sprintf("transcript - %s closes",
			m.keys.label(KeyContextPager, ActionPagerClose)), width)
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
