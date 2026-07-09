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
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

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
		SessionID string
		Call      Caller
	}
	// EventMsg is one session.events.stream envelope.
	EventMsg struct {
		Raw map[string]any
	}
	// ConnLostMsg reports a failed dial or a dropped event stream.
	ConnLostMsg struct {
		Err error
	}
	// ReconnectingMsg reports a reconnect attempt in progress.
	ReconnectingMsg struct {
		Attempt int
	}
	// ConnRestoredMsg reports a successful reconnect.
	ConnRestoredMsg struct {
		SessionID string
	}
)

type taskSubmittedMsg struct {
	taskID string
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

// Options configures a Model.
type Options struct {
	Theme         theme.Theme
	Locale        string // normalized microcopy locale (en, zh)
	Socket        string
	SessionID     string // reuse an existing session; empty creates one
	WorkspaceRoot string
	Now           func() time.Time
}

// Model is the root Bubble Tea model.
type Model struct {
	th     theme.Theme
	locale string
	socket string
	now    func() time.Time

	width, height int
	vp            viewport.Model
	input         textinput.Model
	tr            transcript

	sessionID string
	call      Caller
	conn      ConnState
	attempt   int

	approval       *approvalState
	approvalQueue  []map[string]any // permission.request envelopes queued while an overlay is open
	inFlightTaskID string
	pendingPaste   []string
	lastCtrlC      time.Time
	ctrlCHint      string // non-empty while the double-press exit hint is live; surfaced in the overlay too (view.go), since it covers the transcript
	outcome        Outcome
}

// New builds the root model. It renders nothing until the program runs.
func New(o Options) *Model {
	if o.Now == nil {
		o.Now = time.Now
	}
	ti := textinput.New()
	// ASCII placeholder only: bubbles v2 placeholderView mixes display-width
	// with rune indexing (spike sharp edge) — no CJK here until upstreamed.
	ti.Placeholder = "type an instruction - enter submits, ctrl+c cancels"
	ti.Focus()
	m := &Model{
		th:     o.Theme,
		locale: o.Locale,
		socket: o.Socket,
		now:    o.Now,
		vp:     viewport.New(),
		input:  ti,
		conn:   ConnConnecting,
		width:  80,
		height: 24,
	}
	m.layout()
	return m
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	return textinput.Blink
}

// push appends a rendered line to the transcript and follows the tail.
func (m *Model) push(rendered string) {
	m.tr.push(rendered)
	m.vp.SetContentLines(m.tr.lines)
	m.vp.GotoBottom()
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
