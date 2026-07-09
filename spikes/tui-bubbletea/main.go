// SPIKE — DISPOSABLE CODE. Bubble Tea v2 TUI spike for docs/plans/tui-stack-decision.md §4.
// Not production. No polish. Exists only to measure the gates in spikes/tui-bubbletea/README.md.
//
// Modes:
//
//	default        connect to a live carina daemon, attach/stream session events,
//	               scrolling transcript + input box + approval overlay.
//	-bench         synthetic event burst (no daemon needed): -bench-rate lines/sec for
//	               -bench-secs, in-process frame instrumentation, stats -> -perf-log.
//	-cjk-demo      preload the transcript with mixed zh/en lines (gate G3).
//
// In-TUI commands (typed into the input box):
//
//	/patch             propose a real patch via workspace.patch.propose and open the
//	                   approval overlay with the REAL unified diff as the body (G2).
//	/cmd <argv...>     command.exec; on requires_approval, open the approval overlay
//	                   carrying the real decision_id; allow -> task.action.approve (G2).
//	/burst             same as -bench but from a live session.
//	anything else      appended to the transcript as a user line.
//
// Approval overlay keys: y/1 allow-once, 2 allow-session, 3 allow-project, n/4 deny, esc close.
// (Scope choices 2/3 resolve through the same RPC in this spike; scoped grant persistence is
// P1.1 production work, not spike scope.)
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/rpc"
)

// ---------------------------------------------------------------------------
// styles (hardcoded — brand token table is out of spike scope)
// ---------------------------------------------------------------------------

var (
	stTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("189"))
	stMuted   = lipgloss.NewStyle().Foreground(lipgloss.Color("96"))
	stOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("139"))
	stWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("137"))
	stErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("132"))
	stDiffAdd = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	stDiffDel = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stDiffHdr = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	stBorder  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("96"))
	stOverlay = lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(lipgloss.Color("137")).Padding(0, 1)
)

// ---------------------------------------------------------------------------
// frame instrumentation: event-receipt -> renderer flush, measured at the
// io.Writer the framework writes frames through (R22 starts here).
// ---------------------------------------------------------------------------

type perfStats struct {
	mu           sync.Mutex
	pendingEvent time.Time // set in Update when an event lands
	lastViewAt   time.Time // set at View() entry; frame time = View start -> Write end
	flushLatency []float64 // ms, event -> end of first Write after it (includes FPS-tick wait)
	frameDur     []float64 // ms, View() start -> renderer flush end (true frame render time)
	writeDur     []float64 // ms, duration of each Write call
	viewDur      []float64 // ms, time inside View()
	frames       int
}

func (p *perfStats) markEvent(t time.Time) {
	p.mu.Lock()
	if p.pendingEvent.IsZero() {
		p.pendingEvent = t
	}
	p.mu.Unlock()
}

func (p *perfStats) noteView(start time.Time, d time.Duration) {
	p.mu.Lock()
	p.viewDur = append(p.viewDur, float64(d.Microseconds())/1000.0)
	p.lastViewAt = start
	p.mu.Unlock()
}

// timingWriter wraps the TTY. NOTE (friction): bubbletea v2 only treats the
// output as a real terminal if it implements term.File (Fd/Read/Close on top
// of io.Writer) — a plain io.Writer silently disables the renderer entirely
// (blank screen, no error). So we must forward Fd/Read/Close to the real TTY.
type timingWriter struct {
	w  *os.File
	ps *perfStats
}

func (tw *timingWriter) Read(b []byte) (int, error) { return tw.w.Read(b) }
func (tw *timingWriter) Close() error               { return tw.w.Close() }
func (tw *timingWriter) Fd() uintptr                { return tw.w.Fd() }

func (tw *timingWriter) Write(b []byte) (int, error) {
	start := time.Now()
	n, err := tw.w.Write(b)
	end := time.Now()
	tw.ps.mu.Lock()
	tw.ps.frames++
	tw.ps.writeDur = append(tw.ps.writeDur, float64(end.Sub(start).Microseconds())/1000.0)
	if !tw.ps.lastViewAt.IsZero() {
		tw.ps.frameDur = append(tw.ps.frameDur, float64(end.Sub(tw.ps.lastViewAt).Microseconds())/1000.0)
		tw.ps.lastViewAt = time.Time{}
	}
	if !tw.ps.pendingEvent.IsZero() {
		tw.ps.flushLatency = append(tw.ps.flushLatency, float64(end.Sub(tw.ps.pendingEvent).Microseconds())/1000.0)
		tw.ps.pendingEvent = time.Time{}
	}
	tw.ps.mu.Unlock()
	return n, err
}

func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	idx := int(math.Ceil(p/100*float64(len(s)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(s) {
		idx = len(s) - 1
	}
	return s[idx]
}

// ---------------------------------------------------------------------------
// messages
// ---------------------------------------------------------------------------

type evMsg struct {
	raw      map[string]any
	received time.Time
}
type benchLineMsg struct {
	line string
	t    time.Time
}
type benchDoneMsg struct{ sent int }
type rpcErrMsg struct{ err error }
type patchProposedMsg struct{ patch *kernel.Patch }
type cmdDecisionMsg struct {
	decision *kernel.Decision
	argv     []string
	result   json.RawMessage
}
type approvalDoneMsg struct {
	verdict string // allowed / denied
	scope   string
	detail  string
	err     error
}
type sessionReadyMsg struct{ sessionID string }

// ---------------------------------------------------------------------------
// approval overlay state
// ---------------------------------------------------------------------------

type approval struct {
	kind       string // "patch" | "command"
	title      string
	bodyLines  []string // pre-colored
	decisionID string   // command path
	patchID    string   // patch path
	label      string
}

// ---------------------------------------------------------------------------
// model
// ---------------------------------------------------------------------------

type entry struct {
	rendered string // cached render (the charCache lesson, R18)
}

type model struct {
	width, height int
	vp            viewport.Model
	input         textinput.Model
	entries       []entry
	lines         []string // flattened cached lines fed to viewport
	sessionID     string
	socket        string
	call          *rpc.Client // request/response connection
	approval      *approval
	ps            *perfStats
	benchMode     bool
	benchRate     int
	benchSecs     int
	perfLog       string
	program       *tea.Program // for Send from goroutines
	statusLine    string
	quitAfter     time.Duration
}

func (m *model) push(line string) {
	m.entries = append(m.entries, entry{rendered: line})
	m.lines = append(m.lines, line)
	m.vp.SetContentLines(m.lines)
	m.vp.GotoBottom()
}

func (m *model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink}
	if m.benchMode {
		cmds = append(cmds, func() tea.Msg { return benchStart{} })
	}
	return tea.Batch(cmds...)
}

type benchStart struct{}

func (m *model) layout() {
	inputHeight := 3
	statusHeight := 1
	vh := m.height - inputHeight - statusHeight - 2 // transcript border
	if vh < 3 {
		vh = 3
	}
	vw := m.width - 2
	if vw < 20 {
		vw = 20
	}
	iw := m.width - 8
	if iw < 20 {
		iw = 20
	}
	m.vp.SetWidth(vw)
	m.vp.SetHeight(vh)
	// NOTE (friction): bubbles v2 textinput panics (makeslice: len out of
	// range) if width goes negative with a placeholder set — clamp defensively.
	m.input.SetWidth(iw)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case sessionReadyMsg:
		m.sessionID = msg.sessionID
		m.statusLine = "session " + msg.sessionID
		m.push(stMuted.Render("· attached to " + msg.sessionID))
		return m, nil

	case evMsg:
		m.ps.markEvent(msg.received)
		m.push(renderEvent(msg.raw))
		return m, nil

	case benchLineMsg:
		m.ps.markEvent(msg.t)
		m.push(msg.line)
		return m, nil

	case benchDoneMsg:
		stats := m.snapshotStats(msg.sent)
		m.push(stOK.Render(fmt.Sprintf("burst done: %d events; p50=%.2fms p95=%.2fms max=%.2fms (event→flush, n=%d)",
			msg.sent, stats["p50_ms"], stats["p95_ms"], stats["max_ms"], int(stats["samples"]))))
		if m.perfLog != "" {
			b, _ := json.MarshalIndent(stats, "", "  ")
			_ = os.WriteFile(m.perfLog, b, 0o644)
			m.push(stMuted.Render("· perf stats written to " + m.perfLog))
		}
		return m, nil

	case benchStart:
		return m, m.startBurst()

	case patchProposedMsg:
		p := msg.patch
		m.approval = &approval{
			kind:      "patch",
			title:     fmt.Sprintf("Patch approval — %s (risk %d, status %s)", p.PatchID, p.RiskLevel, p.ApprovalStatus),
			bodyLines: colorDiff(p.Diff),
			patchID:   p.PatchID,
			label:     p.Reason,
		}
		m.push(stWarn.Render(fmt.Sprintf("⚿ patch %s proposed — awaiting approval", p.PatchID)))
		return m, nil

	case cmdDecisionMsg:
		d := msg.decision
		switch d.Decision {
		case "requires_approval":
			m.approval = &approval{
				kind:       "command",
				title:      fmt.Sprintf("Command approval — decision_id %s", d.DecisionID),
				bodyLines:  []string{stDiffHdr.Render("$ " + strings.Join(msg.argv, " ")), stMuted.Render("policy: " + d.Reason)},
				decisionID: d.DecisionID,
				label:      strings.Join(msg.argv, " "),
			}
			m.push(stWarn.Render("⚿ requires_approval decision_id=" + d.DecisionID))
		case "denied":
			m.push(stErr.Render("✗ denied: " + d.Reason))
		default:
			m.push(stOK.Render("✓ allowed; executed"))
			if len(msg.result) > 0 {
				m.push(stMuted.Render(truncate(string(msg.result), 200)))
			}
		}
		return m, nil

	case approvalDoneMsg:
		m.approval = nil
		if msg.err != nil {
			m.push(stErr.Render("✗ approval RPC failed: " + msg.err.Error()))
			return m, nil
		}
		switch msg.verdict {
		case "allowed":
			m.push(stOK.Render("✓ allowed (" + msg.scope + ") — resumed: " + truncate(msg.detail, 160)))
		default:
			m.push(stErr.Render("✗ denied — " + truncate(msg.detail, 160)))
		}
		return m, nil

	case rpcErrMsg:
		m.push(stErr.Render("✗ rpc: " + msg.err.Error()))
		return m, nil

	case tea.PasteMsg:
		// Terminals paste line breaks as \r (tmux does), not \n — normalize first.
		content := strings.ReplaceAll(strings.ReplaceAll(msg.Content, "\r\n", "\n"), "\r", "\n")
		nlines := strings.Count(content, "\n") + 1
		if nlines > 1 {
			m.push(stMuted.Render(fmt.Sprintf("[Pasted %d lines]", nlines)))
			return m, nil
		}
		// single-line paste falls through to the input
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case tea.KeyPressMsg:
		key := msg.String()
		if m.approval != nil {
			switch key {
			case "y", "1":
				return m, m.resolveApproval("once", true)
			case "2":
				return m, m.resolveApproval("session", true)
			case "3":
				return m, m.resolveApproval("project", true)
			case "n", "4":
				return m, m.resolveApproval("deny", false)
			case "esc":
				m.approval = nil
				m.push(stMuted.Render("· approval prompt dismissed (still pending server-side)"))
			case "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		switch key {
		case "ctrl+c":
			return m, tea.Quit
		case "enter":
			text := m.input.Value()
			m.input.Reset()
			return m, m.execute(text)
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) snapshotStats(sent int) map[string]float64 {
	m.ps.mu.Lock()
	defer m.ps.mu.Unlock()
	return map[string]float64{
		"events_sent":   float64(sent),
		"samples":       float64(len(m.ps.flushLatency)),
		"frames":        float64(m.ps.frames),
		"p50_ms":        percentile(m.ps.flushLatency, 50),
		"p95_ms":        percentile(m.ps.flushLatency, 95),
		"max_ms":        percentile(m.ps.flushLatency, 100),
		"frame_p50_ms":  percentile(m.ps.frameDur, 50),
		"frame_p95_ms":  percentile(m.ps.frameDur, 95),
		"frame_max_ms":  percentile(m.ps.frameDur, 100),
		"view_p95_ms":   percentile(m.ps.viewDur, 95),
		"write_p95_ms":  percentile(m.ps.writeDur, 95),
		"write_p50_ms":  percentile(m.ps.writeDur, 50),
		"lines_visible": float64(len(m.lines)),
	}
}

// startBurst injects benchRate synthetic events/sec for benchSecs via program.Send.
func (m *model) startBurst() tea.Cmd {
	rate, secs := m.benchRate, m.benchSecs
	prog := m.program
	return func() tea.Msg {
		go func() {
			total := rate * secs
			interval := time.Second / time.Duration(rate)
			tick := time.NewTicker(interval)
			defer tick.Stop()
			zh := []string{
				"补丁干净落地。无惊无险，本该如此。",
				"审计链校验通过:1,204 条记录",
				"kernel verdict read-only-allow · 内核放行",
				"stream event burst line with plain ascii text",
			}
			for i := 0; i < total; i++ {
				<-tick.C
				line := fmt.Sprintf("%s #%04d %s", stMuted.Render("evt"), i, zh[i%len(zh)])
				prog.Send(benchLineMsg{line: line, t: time.Now()})
			}
			prog.Send(benchDoneMsg{sent: total})
		}()
		return nil
	}
}

// execute handles input-box submissions.
func (m *model) execute(text string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	m.push(stTitle.Render("you ") + text)
	switch {
	case text == "/burst":
		return m.startBurst()
	case text == "/patch":
		return m.proposePatch()
	case strings.HasPrefix(text, "/cmd "):
		argv := strings.Fields(strings.TrimPrefix(text, "/cmd "))
		return m.execCommand(argv)
	case text == "/quit":
		return tea.Quit
	}
	return nil
}

func (m *model) proposePatch() tea.Cmd {
	call, sid := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return rpcErrMsg{fmt.Errorf("no daemon connection (bench mode?)")}
		}
		var patch kernel.Patch
		err := call.Call("workspace.patch.propose", map[string]any{
			"session_id": sid,
			"reason":     "spike: approval overlay demo",
			"files": []map[string]any{{
				"path":        "spike_hello.txt",
				"new_content": "hello from the bubbletea spike\n你好，来自 Bubble Tea 尖峰试验\nsecond line kept\n",
			}},
		}, &patch)
		if err != nil {
			return rpcErrMsg{err}
		}
		return patchProposedMsg{patch: &patch}
	}
}

func (m *model) execCommand(argv []string) tea.Cmd {
	call, sid := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return rpcErrMsg{fmt.Errorf("no daemon connection (bench mode?)")}
		}
		var out struct {
			Decision *kernel.Decision `json:"decision"`
			Result   json.RawMessage  `json:"result"`
		}
		if err := call.Call("command.exec", map[string]any{"session_id": sid, "argv": argv}, &out); err != nil {
			return rpcErrMsg{err}
		}
		return cmdDecisionMsg{decision: out.Decision, argv: argv, result: out.Result}
	}
}

func (m *model) resolveApproval(scope string, allow bool) tea.Cmd {
	ap, call, sid := m.approval, m.call, m.sessionID
	return func() tea.Msg {
		if call == nil || ap == nil {
			return approvalDoneMsg{err: fmt.Errorf("no daemon connection")}
		}
		switch ap.kind {
		case "patch":
			if !allow {
				return approvalDoneMsg{verdict: "denied", scope: scope, detail: "patch " + ap.patchID + " left pending (spike: no patch-deny RPC)"}
			}
			var patch kernel.Patch
			if err := call.Call("workspace.patch.apply", map[string]any{
				"session_id": sid, "patch_id": ap.patchID, "approver": "operator",
			}, &patch); err != nil {
				return approvalDoneMsg{err: err}
			}
			return approvalDoneMsg{verdict: "allowed", scope: scope,
				detail: fmt.Sprintf("patch %s status=%s approval=%s files=%v", patch.PatchID, patch.Status, patch.ApprovalStatus, patch.AffectedFiles)}
		case "command":
			if !allow {
				var dec kernel.Decision
				if err := call.Call("task.action.deny", map[string]any{
					"session_id": sid, "decision_id": ap.decisionID, "reason": "operator denied via spike TUI",
				}, &dec); err != nil {
					return approvalDoneMsg{err: err}
				}
				return approvalDoneMsg{verdict: "denied", scope: scope, detail: "decision " + dec.DecisionID + " -> " + dec.Decision}
			}
			var out struct {
				Decision *kernel.Decision `json:"decision"`
				Result   json.RawMessage  `json:"result"`
			}
			if err := call.Call("task.action.approve", map[string]any{
				"session_id": sid, "decision_id": ap.decisionID, "approver": "operator",
			}, &out); err != nil {
				return approvalDoneMsg{err: err}
			}
			detail := "decision " + out.Decision.DecisionID + " -> " + out.Decision.Decision
			if len(out.Result) > 0 {
				detail += " result=" + truncate(string(out.Result), 120)
			}
			return approvalDoneMsg{verdict: out.Decision.Decision, scope: scope, detail: detail}
		}
		return approvalDoneMsg{err: fmt.Errorf("unknown approval kind %q", ap.kind)}
	}
}

// ---------------------------------------------------------------------------
// view
// ---------------------------------------------------------------------------

func (m *model) View() tea.View {
	start := time.Now()
	var b strings.Builder

	b.WriteString(stBorder.Width(m.width - 2).Render(m.vp.View()))
	b.WriteString("\n")
	b.WriteString(stBorder.Width(m.width - 2).Render(" ❯ " + m.input.View()))
	b.WriteString("\n")
	status := m.statusLine
	if status == "" {
		status = "not attached"
	}
	b.WriteString(stMuted.Render(fmt.Sprintf(" carina spike · %s · %d lines · /patch /cmd /burst /quit", status, len(m.lines))))

	content := b.String()
	if m.approval != nil {
		body := strings.Join(m.approval.bodyLines, "\n")
		box := stOverlay.Render(
			stTitle.Render(m.approval.title) + "\n\n" +
				body + "\n\n" +
				stOK.Render("[y/1] allow once  [2] session  [3] project  ") +
				stErr.Render("[n/4] deny") + stMuted.Render("  [esc] close"),
		)
		// spike shortcut: overlay replaces the transcript region instead of
		// compositing layers (v2 layer API not exercised).
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	m.ps.noteView(start, time.Since(start))
	return v
}

func renderEvent(ev map[string]any) string {
	t, _ := ev["type"].(string)
	ts, _ := ev["timestamp"].(string)
	if len(ts) >= 19 {
		ts = ts[11:19]
	}
	var detail string
	if p, ok := ev["payload"].(map[string]any); ok {
		if c, ok := p["command"].(string); ok {
			detail = c
		} else if ch, ok := p["chunk"].(string); ok {
			detail = ch
		} else if st, ok := p["status"].(string); ok {
			detail = st
		}
	}
	if detail == "" {
		b, _ := json.Marshal(ev["payload"])
		detail = truncate(string(b), 120)
	}
	glyph := stOK.Render("✓")
	if t == "permission.request" {
		glyph = stWarn.Render("⚿")
		detail = fmt.Sprintf("decision_id=%v label=%v", ev["decision_id"], ev["label"])
	}
	return fmt.Sprintf("%s %s %s %s", stMuted.Render(ts), glyph, stTitle.Render(t), detail)
}

func colorDiff(diff string) []string {
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		switch {
		case strings.HasPrefix(ln, "+"):
			out = append(out, stDiffAdd.Render(ln))
		case strings.HasPrefix(ln, "-"):
			out = append(out, stDiffDel.Render(ln))
		case strings.HasPrefix(ln, "@@"), strings.HasPrefix(ln, "diff "), strings.HasPrefix(ln, "index "):
			out = append(out, stDiffHdr.Render(ln))
		default:
			out = append(out, ln)
		}
	}
	return out
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------------------------------------------------------------------
// main
// ---------------------------------------------------------------------------

func main() {
	home, _ := os.UserHomeDir()
	socket := flag.String("socket", filepath.Join(home, ".carina", "daemon.sock"), "daemon unix socket")
	session := flag.String("session", "", "existing session id (default: create one)")
	workspace := flag.String("workspace", "", "workspace root for session.create")
	bench := flag.Bool("bench", false, "synthetic burst mode, no daemon required")
	benchRate := flag.Int("bench-rate", 100, "synthetic events per second")
	benchSecs := flag.Int("bench-secs", 10, "burst duration seconds")
	perfLog := flag.String("perf-log", "", "write perf stats JSON here after a burst")
	cjkDemo := flag.Bool("cjk-demo", false, "preload mixed zh/en transcript lines (gate G3)")
	flag.Parse()

	ps := &perfStats{}
	ti := textinput.New()
	ti.Placeholder = "输入 / type here — /patch /cmd <argv> /burst"
	ti.Focus()
	vp := viewport.New()

	m := &model{
		vp: vp, input: ti, ps: ps,
		socket: *socket, benchMode: *bench, benchRate: *benchRate, benchSecs: *benchSecs,
		perfLog: *perfLog,
		width:   80, height: 24,
	}
	m.layout()
	m.push(stTitle.Render("carina · bubbletea v2 spike") + stMuted.Render("  (disposable)"))
	if *cjkDemo {
		for _, l := range []string{
			"补丁干净落地。无惊无险，本该如此。",
			"审计链校验通过:1,204 条记录",
			"mixed 中英 line: kernel verdict read-only-allow 内核放行 OK",
			"ascii only line for column comparison ......................",
			"全角字符宽度测试:一二三四五六七八九十一二三四五六七八九十",
		} {
			m.push(l)
		}
	}

	tw := &timingWriter{w: os.Stdout, ps: ps}
	prog := tea.NewProgram(m, tea.WithOutput(tw))
	m.program = prog

	// Daemon wiring (skipped in bench mode): one call connection + one
	// dedicated stream connection (the go/rpc demux pattern).
	if !*bench {
		go func() {
			call, err := rpc.Dial(*socket)
			if err != nil {
				prog.Send(rpcErrMsg{err})
				return
			}
			sid := *session
			if sid == "" {
				ws := *workspace
				if ws == "" {
					ws, _ = os.Getwd()
				}
				var out struct {
					SessionID string `json:"session_id"`
				}
				if err := call.Call("session.create", map[string]any{
					"workspace_root": ws, "profile": "safe-edit",
				}, &out); err != nil {
					prog.Send(rpcErrMsg{err})
					return
				}
				sid = out.SessionID
			}
			m.call = call
			prog.Send(sessionReadyMsg{sessionID: sid})

			stream, err := rpc.Dial(*socket)
			if err != nil {
				prog.Send(rpcErrMsg{err})
				return
			}
			if err := stream.Call("session.events.stream", map[string]any{"session_id": sid}, nil); err != nil {
				prog.Send(rpcErrMsg{err})
				return
			}
			for {
				method, params, err := stream.ReadNotification()
				if err != nil {
					prog.Send(rpcErrMsg{fmt.Errorf("event stream closed: %w", err)})
					return
				}
				if method != "event" {
					continue
				}
				var ev map[string]any
				if json.Unmarshal(params, &ev) == nil {
					prog.Send(evMsg{raw: ev, received: time.Now()})
				}
			}
		}()
	}

	if _, err := prog.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "spike:", err)
		os.Exit(1)
	}
	// On exit print a final stats line (useful when quitting a bench run early).
	stats := m.snapshotStats(0)
	b, _ := json.Marshal(stats)
	fmt.Println(string(b))
}
