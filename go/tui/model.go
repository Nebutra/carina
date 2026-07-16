// Package tui is the production engine behind apps/carina-tui: an Elm-model
// (Bubble Tea v2) client of the carina daemon. The binary in apps/ is a thin
// shell; the model, update logic, views, theme, and diff renderer live here
// so the CLI renderer can reuse them (the plan's one-engine/two-renderers
// direction, P1.5/P3.1).
//
// Promoted from spikes/tui-bubbletea (see docs/plans/tui-stack-decision.md
// "Spike verdict"): the two-connection go/rpc pattern, the unified-diff
// colorizer, the per-entry transcript render cache, paste normalization, and
// the approval-overlay keymap. Rewritten rather than copied: all color moved
// behind go/tui/theme (brand tokens), all governance copy behind
// go/microcopy (Governed/Degrade registers), Ctrl-C became the P1.4
// cascading cancel, and connection loss became an explicit degrade state
// with visible reconnection instead of a silent freeze.
package tui

import (
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/tui/theme"
)

// Caller is the request/response RPC surface the model needs. *rpc.Client
// satisfies it; tests substitute a fake.
type Caller interface {
	Call(method string, params any, result any) error
}

// ConnState tracks the daemon link for the degrade banner.
type ConnState int

const (
	ConnConnecting ConnState = iota
	ConnConnected
	ConnLost
	ConnReconnecting
)

// Messages sent by the connection goroutine (conn.go) and internal commands.
type (
	// SessionReadyMsg announces a (re)established call connection bound to a
	// session.
	SessionReadyMsg struct {
		SessionID  string
		Generation uint64
		Call       Caller
	}
	// TaskActiveMsg restores the task the prompt should steer after attach.
	TaskActiveMsg struct {
		SessionID  string
		Generation uint64
		TaskID     string
	}
	// EventMsg is one session.events.stream envelope.
	EventMsg struct {
		SessionID  string
		Generation uint64
		Raw        map[string]any
	}
	// ConnLostMsg reports a failed dial or a dropped event stream.
	ConnLostMsg struct {
		SessionID  string
		Generation uint64
		Err        error
	}
	// ReconnectingMsg reports a reconnect attempt in progress.
	ReconnectingMsg struct {
		SessionID  string
		Generation uint64
		Attempt    int
	}
	// ConnRestoredMsg reports a successful reconnect.
	ConnRestoredMsg struct {
		SessionID  string
		Generation uint64
	}
)

type promptDraft struct {
	Prefix          []string `json:"prefix,omitempty"`
	Text            string   `json:"text,omitempty"`
	Paste           []string `json:"paste,omitempty"`
	Model           string   `json:"model,omitempty"`
	Agent           string   `json:"agent,omitempty"`
	Mode            string   `json:"mode,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
}

type submissionEnvelope struct {
	prompt          string
	model           string
	agent           string
	mode            string
	reasoningEffort string
}

type submissionKind string

const (
	submissionTask  submissionKind = "task"
	submissionSteer submissionKind = "steer"
	submissionShell submissionKind = "shell"
)

type submissionState struct {
	generation   int
	kind         submissionKind
	target       string
	draft        promptDraft
	consumePaste bool
	fromQueue    bool
	background   bool
	// composerDetached means the interactive composer has moved on to a
	// distinct next draft while this immutable submission snapshot waits for
	// its RPC acknowledgement. Queue and background submissions already have
	// independent composer ownership and do not need this transition.
	composerDetached bool
	clientID         string
	envelope         submissionEnvelope
}

type submissionRetry struct {
	clientID        string
	prompt          string
	draft           promptDraft
	model           string
	agent           string
	mode            string
	reasoningEffort string
}

type submissionDoneMsg struct {
	generation int
	taskID     string
	result     string
	status     string
	err        error
}

type earlyTaskTerminal struct {
	generation int
	successful bool
}

type cancelDoneMsg struct {
	taskID string
	err    error
}

type approvalDoneMsg struct {
	verdict    string // allowed | denied
	initiator  string // user | policy (who said no, when verdict is denied)
	scope      string // once | session | project
	action     string
	decisionID string
	detail     string
	err        error
}

type rpcErrMsg struct {
	err error
}

type externalEditorDoneMsg struct {
	generation int
	err        error
}

type clipboardDoneMsg struct {
	err error
}

// Options configures a Model.
type Options struct {
	Theme         theme.Theme
	Locale        string // BCP-47/POSIX UI locale; normalized by NewChecked
	Socket        string
	SessionID     string // reuse an existing session; empty creates one
	WorkspaceRoot string
	Model         string             // default model for new tasks; empty means agent/runtime default
	SwitchSession func(string) error // connection-controller hook for session lifecycle commands
	StateDir      string             // durable local TUI state; empty disables submission recovery
	Now           func() time.Time
	// Keybindings replaces selected action bindings after defaults are loaded.
	// Embedders accepting user-controlled overrides should call NewChecked.
	Keybindings       []KeyBindingOverride
	NoAlternateScreen bool
	KeymapUpdater     KeymapUpdateFunc
}

// Model is the root Bubble Tea model.
type Model struct {
	th            theme.Theme
	locale        string
	socket        string
	workspaceRoot string
	stateDir      string
	submissions   submissionJournal
	now           func() time.Time

	width, height    int
	root             rootLayout
	vp               viewport.Model
	input            textarea.Model
	tr               transcript
	streams          map[string]*messageStream
	followTail       bool
	unseenLines      int
	terminalBlurred  bool
	unreadAttention  int
	attentionAlerted bool
	attentionSeen    map[string]struct{}
	attentionOrder   []string
	keys             runtimeKeymap
	chord            chordState
	keymapErr        error
	keymapUpdater    KeymapUpdateFunc
	keymapEditor     *keymapEditorState
	helpOpen         bool
	helpScroll       int
	settings         *settingsShellState
	compactMode      bool
	composerMode     composerMode // normal chat vs sticky shell (!)
	runtime          runtimeStatus
	// pendingSideQuestion is submitted once after a successful session.fork
	// switch (Codex/CC side conversation pattern).
	pendingSideQuestion string
	// contextNudgeLevel tracks the last pressure notice level to avoid spam.
	contextNudgeLevel int // 0 none, 1 warning(80), 2 critical(90), 3 auto-compacted

	sessionID string
	call      Caller
	conn      ConnState
	attempt   int

	approval              *approvalState
	approvalQueue         []map[string]any // permission.request envelopes queued while an overlay is open
	approvalSeen          map[string]bool
	approvalResolved      map[string]bool
	approvalPending       map[string]approvalResolutionSnapshot
	approvalOrder         map[string]uint64
	approvalNextSeq       uint64
	approvalOutcomeSeq    uint64
	planReview            *planReviewState
	question              *questionState
	questionQueue         []map[string]any
	questionSeen          map[string]bool
	questionResolved      map[string]bool
	tasks                 taskGraph
	inFlightTaskID        string
	pendingPaste          []string
	pendingPrefix         []string
	pasteBurst            pasteBurstState
	followUps             inputQueue
	submitting            *submissionState
	submissionGen         int
	earlyTerminals        map[string]earlyTaskTerminal
	retrySubmission       *submissionRetry
	submissionLeaseErr    error
	queueRecallPending    bool
	editor                *externalEditorSession
	editorGen             int
	queueRestoreReason    string
	transcriptPager       *transcriptPagerState
	checkpointPicker      *checkpointPickerState
	modelPicker           *modelPickerState
	sessionPicker         *sessionPickerState
	modelPickerGen        int
	canonicalGen          int
	pausedRestore         *checkpointRestoreResult
	getenv                func(string) string
	clipboardWrite        func(string) error
	history               []promptDraft
	historyPos            int
	historyScratch        promptDraft
	historyLoadGen        int
	historySearch         *historySearchState
	composerUndo          composerUndoState
	lastCtrlC             time.Time
	ctrlCHint             string // non-empty while the double-press exit hint is live; surfaced in the overlay too (view.go), since it covers the transcript
	rewindPrimed          bool
	noAlternateScreen     bool
	mode                  string
	model                 string
	reasoningEffort       string
	modelPinned           bool
	switchSession         func(string) error
	sessionGeneration     uint64
	sessionOpGen          uint64
	pendingSessionID      string
	pendingWorkspaceRoot  string
	sessionActionPending  string
	previousSessionID     string
	previousWorkspaceRoot string
	goal                  *goalView
	outcome               Outcome

	// Mention/slash suggestion panel (@-file, @-agent, /-command). See
	// suggest.go for the debounce/fetch flow and mention.go for trigger
	// detection.
	suggest       *suggestState
	suggestGen    int // monotonic; discards stale debounce/fetch results
	treeCache     []treeEntry
	treeCacheAt   time.Time
	treeCacheRoot string
}

type surfaceResultMsg struct{ sessionID, label, text string }

type canonicalSurfaceKind string

const (
	canonicalTranscript canonicalSurfaceKind = "transcript"
	canonicalSearch     canonicalSurfaceKind = "search"
	canonicalRecap      canonicalSurfaceKind = "recap"
)

type canonicalSurfaceMsg struct {
	generation int
	kind       canonicalSurfaceKind
	query      string
	items      []map[string]any
	err        error
}

type modeChangedMsg struct {
	sessionID      string
	mode           string
	err            error
	followUpPrompt string // optional: submit after mode switch (e.g. /plan <desc>)
}

type loopResultMsg struct {
	action    string
	sessionID string
	data      map[string]any
	err       error
}

type operationalSurfaceMsg struct {
	sessionID string
	kind      string
	data      map[string]any
	err       error
}

type workspaceDiffMsg struct {
	generation int
	data       map[string]any
	err        error
}

// New builds the root model. Invalid optional overrides visibly degrade to the
// defaults; callers that require a hard startup error use NewChecked.
func New(o Options) *Model {
	m, err := NewChecked(o)
	if err == nil {
		return m
	}
	fallback := o
	fallback.Keybindings = nil
	m, _ = NewChecked(fallback)
	m.keymapErr = err
	m.push(m.th.Style(theme.RoleError).Render(m.text(MsgKeybindingsError, MessageArgs{"error": err.Error()})))
	return m
}

// NewChecked rejects malformed, unknown, or conflicting keybinding overrides.
func NewChecked(o Options) (*Model, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	o.Locale = string(normalizeUILocale(o.Locale))
	keys, err := newRuntimeKeymap(o.Keybindings)
	if err != nil {
		return nil, err
	}
	ti := textarea.New()
	ti.Prompt = "> "
	ti.ShowLineNumbers = false
	ti.DynamicHeight = true
	ti.MinHeight = 1
	ti.MaxHeight = 6
	ti.MaxContentHeight = 1000
	ti.SetStyles(inputStyles(o.Theme))
	// Bubble Tea's declared cursor is the terminal cursor and therefore the
	// anchor used by IMEs for their candidate window. The textarea's default
	// virtual cursor only paints a cell in the returned string; it cannot move
	// the terminal cursor to the logical caret (R13/R21).
	ti.SetVirtualCursor(false)
	installEditorKeymap(&ti, keys)
	ti.Placeholder = newLocalizer(o.Locale).Text(MsgPlaceholderInstruction, MessageArgs{
		"submit":  primaryKeyLabel(keys.keys(KeyContextComposer, ActionComposerSubmit)),
		"newline": primaryKeyLabel(keys.keys(KeyContextComposer, ActionComposerNewline)),
		"help":    primaryKeyLabel(keys.keys(KeyContextGlobal, ActionGlobalHelp)),
	})
	_ = ti.Focus()
	m := &Model{
		th:                o.Theme,
		locale:            o.Locale,
		socket:            o.Socket,
		workspaceRoot:     o.WorkspaceRoot,
		stateDir:          o.StateDir,
		submissions:       newSubmissionJournal(o.StateDir, o.WorkspaceRoot),
		now:               o.Now,
		getenv:            os.Getenv,
		clipboardWrite:    systemClipboardWrite,
		vp:                viewport.New(),
		input:             ti,
		keys:              keys,
		keymapUpdater:     o.KeymapUpdater,
		noAlternateScreen: o.NoAlternateScreen,
		conn:              ConnConnecting,
		followTail:        true,
		questionSeen:      make(map[string]bool),
		questionResolved:  make(map[string]bool),
		approvalSeen:      make(map[string]bool),
		approvalResolved:  make(map[string]bool),
		approvalPending:   make(map[string]approvalResolutionSnapshot),
		approvalOrder:     make(map[string]uint64),
		width:             80,
		height:            24,
		mode:              "build",
		model:             strings.TrimSpace(o.Model),
		modelPinned:       strings.TrimSpace(o.Model) != "",
		switchSession:     o.SwitchSession,
	}
	m.layout()
	return m, nil
}

// Close releases process-scoped TUI resources such as the single-writer
// submission lease. Frontends should defer it after constructing a model.
func (m *Model) Close() {
	m.submissions.close()
}

// inputStyles keeps the third-party textarea inside Carina's terminal
// capability contract. Bubbles defaults include explicit black/white ANSI
// colors, which would otherwise leak through NO_COLOR/Mono rendering.
func inputStyles(th theme.Theme) textarea.Styles {
	plain := lipgloss.NewStyle()
	text := th.Style(theme.RoleText)
	muted := th.Style(theme.RoleMuted)
	prompt := th.Style(theme.RoleInfo)
	s := textarea.Styles{
		Focused: textarea.StyleState{
			Base:             plain,
			CursorLine:       plain,
			CursorLineNumber: muted,
			EndOfBuffer:      muted,
			LineNumber:       muted,
			Placeholder:      muted,
			Prompt:           prompt,
			Text:             text,
		},
		Blurred: textarea.StyleState{
			Base:             plain,
			CursorLine:       plain,
			CursorLineNumber: muted,
			EndOfBuffer:      muted,
			LineNumber:       muted,
			Placeholder:      muted,
			Prompt:           prompt,
			Text:             text,
		},
		Cursor: textarea.CursorStyle{
			Color: th.Color(theme.RoleText),
			Shape: tea.CursorBar,
			Blink: true,
		},
	}
	return s
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return m.input.Focus()
}

// push appends a rendered line to the transcript. New output follows the tail
// only while the operator is already following it; reading older output is
// never interrupted by an asynchronous event.
func (m *Model) push(rendered string) {
	added := len(strings.Split(rendered, "\n"))
	m.tr.push(rendered)
	m.vp.SetContentLines(m.tr.lines)
	if m.followTail {
		m.vp.GotoBottom()
		m.unseenLines = 0
	} else {
		m.unseenLines += added
	}
}

func (m *Model) pushEvent(ev map[string]any) {
	if !showInPrimaryTranscript(ev) {
		return
	}
	before := len(m.tr.lines)
	m.tr.pushPresentation(presentEvent(ev, m.th, m.locale), m.th, m.transcriptWidth())
	m.vp.SetContentLines(m.tr.lines)
	added := len(m.tr.lines) - before
	if added < 1 {
		// An in-place lifecycle update is still unseen activity, but should not
		// pretend that a whole replacement block was appended.
		added = 1
	}
	if m.followTail {
		m.vp.GotoBottom()
		m.unseenLines = 0
	} else {
		m.unseenLines += added
	}
}

// The main surface is a conversation, not an audit tail. Detailed routing and
// lifecycle telemetry remains available through audit/session replay.
func showInPrimaryTranscript(ev map[string]any) bool {
	eventType := str(ev["type"])
	payload, _ := ev["payload"].(map[string]any)
	switch eventType {
	case "ModelRequested", "RoutingDecision", "RoutingOutcome", "RuntimeStageChanged",
		"ToolRequested", "ToolApproved", "ToolDenied", "TaskCreated",
		"MemoryRecallRequested", "MemoryWriteRequested", "GoalChangeRequested", "ScheduleChanged":
		return false
	case "MemoryProjectionChanged":
		status := strings.ToLower(str(payload["status"]))
		return status == "failed" || status == "reconcile"
	case "ModelResponded":
		tool, _, _ := safeModelAction(str(payload["text"]))
		return tool == "done" || str(payload["spawn_agent"]) != "" || strings.HasPrefix(str(payload["status"]), "workflow_")
	case "task.completed":
		return str(ev["status"]) != "completed"
	default:
		return true
	}
}

// plain reports whether glyph/personality suppression applies (NO_COLOR,
// dumb terminal — the Mono profile).
func (m *Model) plain() bool {
	return m.th.Profile() == theme.Mono
}

// Outcome returns the governance outcome the process exits with.
func (m *Model) Outcome() Outcome {
	// No session was ever established: quitting mid-dial (ConnConnecting,
	// before the first SessionReadyMsg or ConnLostMsg arrives) is the same
	// user-facing failure as a confirmed drop (ConnLost/ConnReconnecting) —
	// in both cases nothing ran, and exit 0 would misreport success to an
	// orchestrating script.
	if m.sessionID == "" && m.conn != ConnConnected {
		return OutcomeDaemonUnreachable
	}
	return m.outcome
}
