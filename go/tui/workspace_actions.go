package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/tui/theme"
	ui "github.com/Nebutra/carina/go/tui/ui"
)

type externalEditorSession struct {
	generation int
	draft      externalEditorDraft
	original   composerSnapshot
	holdsQueue bool
}

type transcriptPagerState struct {
	generation        int
	scroll            int
	text              string
	title             string
	loadingText       string
	loading           bool
	err               string
	operationalKind   string
	operationalMethod string
	operationalParams map[string]any
	hoveredAction     string
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
		m.setOperationalNotice(m.text(MsgWorkspaceExternalEditor, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}), theme.RoleError)
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
		m.setOperationalNotice(m.text(MsgWorkspaceDraftRestored, MessageArgs{"glyph": glyphFailed(m.th), "error": err.Error()}), theme.RoleError)
	} else {
		m.restoreDraft(draft)
		m.recordComposerEdit(session.original, composerEditOther)
		m.setOperationalNotice(m.text(MsgWorkspaceEditorApplied, nil), theme.RoleMuted)
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
		m.setOperationalNotice(m.text(MsgWorkspaceNothingToCopy, MessageArgs{"glyph": glyphFailed(m.th)}), theme.RoleError)
		return nil
	}
	write := m.clipboardWrite
	return func() tea.Msg {
		return clipboardDoneMsg{err: write(text)}
	}
}

func (m *Model) copyTranscriptEntry(key string) tea.Cmd {
	text := m.tr.entryPlainText(key)
	if text == "" {
		m.setOperationalNotice(m.text(MsgWorkspaceNothingToCopy, MessageArgs{"glyph": glyphFailed(m.th)}), theme.RoleError)
		return nil
	}
	write := m.clipboardWrite
	return func() tea.Msg {
		return clipboardDoneMsg{err: write(text)}
	}
}

func (m *Model) handleClipboardDone(msg clipboardDoneMsg) {
	if msg.err != nil {
		m.setOperationalNotice(m.text(MsgWorkspaceCopyFailed, MessageArgs{"glyph": glyphFailed(m.th), "error": msg.err.Error()}), theme.RoleError)
		return
	}
	m.setOperationalNotice(m.text(MsgWorkspaceCopied, nil), theme.RoleMuted)
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

func (m *Model) openTranscriptEntryPager(key string) {
	text := m.tr.entryPlainText(key)
	if text == "" {
		return
	}
	m.breakComposerUndoGroup()
	m.closeSuggest()
	m.transcriptPager = &transcriptPagerState{title: m.text(MsgTranscriptInspectTitle, nil), text: text}
	m.layout()
}

func (m *Model) openTranscriptArtifactPager(key string, artifactIDs []string) {
	if len(artifactIDs) == 0 {
		m.openTranscriptEntryPager(key)
		return
	}
	lines := make([]string, 0, len(artifactIDs)*2)
	for _, artifactID := range artifactIDs {
		artifactID = strings.TrimSpace(artifactID)
		if artifactID == "" {
			continue
		}
		lines = append(lines, "artifact: "+artifactID)
		if m.sessionID != "" {
			lines = append(lines, "carina artifact read "+m.sessionID+" "+artifactID)
		}
	}
	if len(lines) == 0 {
		m.openTranscriptEntryPager(key)
		return
	}
	m.breakComposerUndoGroup()
	m.closeSuggest()
	m.transcriptPager = &transcriptPagerState{title: m.text(MsgTranscriptArtifact, MessageArgs{"ids": strings.Join(artifactIDs, ", ")}), text: strings.Join(lines, "\n")}
	m.layout()
}

func (m *Model) openCanonicalTranscriptPager() tea.Cmd {
	m.breakComposerUndoGroup()
	m.closeSuggest()
	m.canonicalGen++
	m.transcriptPager = &transcriptPagerState{generation: m.canonicalGen, title: m.text(MsgCanonicalTranscriptTitle, nil), loading: true}
	m.layout()
	return m.queryCanonicalSurface(canonicalTranscript, "")
}

func (m *Model) closeTranscriptPager() tea.Cmd {
	if m.transcriptPager != nil && m.transcriptPager.operationalKind != "" {
		m.componentRuntime.Screens.Transition(ui.ScreenConversation, "conversation", m.componentRuntime.Focus.Snapshot(), nil)
	}
	m.transcriptPager = nil
	m.layout()
	return m.resumeQueuedAfterTransient()
}

func (m *Model) openOperationalPager(kind, title string) {
	m.breakComposerUndoGroup()
	m.closeSuggest()
	m.canonicalGen++
	m.transcriptPager = &transcriptPagerState{
		generation: m.canonicalGen, title: title, loading: true,
		loadingText: m.text(MsgCanonicalLoading, nil), operationalKind: kind,
	}
	screen := ui.ScreenOperational
	if kind == "doctor" || kind == "inspect" {
		screen = ui.ScreenDoctor
	}
	m.componentRuntime.Screens.Transition(screen, "operational-pager", m.componentRuntime.Focus.Snapshot(), map[string]any{"kind": kind})
	m.layout()
}

func (m *Model) transcriptPagerLines() []string {
	if m.transcriptPager == nil {
		return nil
	}
	text := m.transcriptPager.text
	if m.transcriptPager.loading {
		text = m.transcriptPager.loadingText
		if text == "" {
			text = m.text(MsgCanonicalLoading, nil)
		}
	} else if m.transcriptPager.err != "" {
		text = m.text(MsgCanonicalUnavailable, MessageArgs{"error": m.transcriptPager.err})
	} else if text == "" && m.transcriptPager.generation > 0 {
		text = m.text(MsgCanonicalEmpty, nil)
	} else if text == "" {
		text = m.tr.plainText()
	}
	if text == "" {
		text = m.text(MsgWorkspaceTranscriptEmpty, nil)
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
	case m.transcriptPager.operationalKind != "" && key == "r":
		return m.refreshOperationalPager(), true
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

func (m *Model) refreshOperationalPager() tea.Cmd {
	state := m.transcriptPager
	if state == nil || state.operationalKind == "" {
		return nil
	}
	m.canonicalGen++
	state.generation = m.canonicalGen
	state.loading = true
	state.err = ""
	state.text = ""
	state.scroll = 0
	m.layout()
	if state.operationalMethod == "__inspect__" {
		return m.inspectSurface()
	}
	if state.operationalMethod == "" {
		return nil
	}
	return m.queryOperationalSurface(state.operationalKind, state.operationalMethod, cloneOperationalParams(state.operationalParams))
}

func cloneOperationalParams(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func (m *Model) transcriptPagerView(width, height int) string {
	if m.transcriptPager == nil || width <= 0 || height <= 0 {
		return ""
	}
	if height == 1 {
		return fitRenderedLine(m.text(MsgWorkspaceTranscriptTiny, MessageArgs{
			"close": m.keys.label(KeyContextPager, ActionPagerClose),
		}), width)
	}
	lines := m.transcriptPagerLines()
	m.clampTranscriptPagerScroll(lines)
	page := maxInt(height-2, 0)
	start := m.transcriptPager.scroll
	end := minInt(start+page, len(lines))
	visible := append([]string(nil), lines[start:end]...)
	headerText := m.countText(MsgWorkspaceTranscriptHeader, len(lines), nil)
	if m.transcriptPager.title != "" {
		headerText = m.transcriptPager.title + "  " + headerText
	}
	header := fitRenderedLine(headerText, width)
	footer := fitRenderedLine(m.text(MsgWorkspaceTranscriptFooter, MessageArgs{
		"up":        m.keys.label(KeyContextPager, ActionPagerUp),
		"down":      m.keys.label(KeyContextPager, ActionPagerDown),
		"page_up":   m.keys.label(KeyContextPager, ActionPagerPageUp),
		"page_down": m.keys.label(KeyContextPager, ActionPagerPageDown),
		"close":     m.keys.label(KeyContextPager, ActionPagerClose),
	}), width)
	if m.transcriptPager.operationalKind != "" {
		refresh, close := m.operationalActionLabels()
		if m.transcriptPager.hoveredAction == "refresh" {
			refresh = m.th.Style(theme.RoleTitle).Render(refresh)
		}
		if m.transcriptPager.hoveredAction == "close" {
			close = m.th.Style(theme.RoleTitle).Render(close)
		}
		footer = joinOperationalFooter(refresh, close, width)
	}
	out := []string{header}
	out = append(out, visible...)
	for len(out) < height-1 {
		out = append(out, "")
	}
	out = append(out, footer)
	return strings.Join(out, "\n")
}
