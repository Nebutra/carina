package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui/theme"
)

// approvalState is the open approval overlay: the reviewable artifact plus
// the decision_id it resolves. The body is a colored unified diff when the
// gated action carries one, otherwise the canonicalized command/resource.
type approvalState struct {
	DecisionID   string
	Action       string // capability, e.g. command.exec
	Resource     string
	Reason       string // the policy rule that fired
	Label        string
	Body         []string // pre-rendered diff lines, when present
	Resolving    bool
	PendingAllow bool
	PendingScope string
	Error        string
	Scroll       int
}

type approvalResolutionSnapshot struct {
	granted   bool
	scope     string
	action    string
	initiator string
}

// openApproval builds the overlay from a permission.request envelope. A
// second request arriving while one overlay is already open is queued
// rather than replacing it: the first decision is still genuinely pending
// server-side (a real task is blocked on it), so silently swapping it out
// would let a keypress resolve the wrong decision_id and orphan the first
// approval until it times out.
func (m *Model) openApproval(ev map[string]any) {
	decisionID := str(ev["decision_id"])
	if decisionID == "" || m.approvalResolved[decisionID] || m.approvalSeen[decisionID] {
		return
	}
	m.approvalSeen[decisionID] = true
	m.approvalNextSeq++
	m.approvalOrder[decisionID] = m.approvalNextSeq
	if m.approval != nil || m.question != nil {
		m.approvalQueue = append(m.approvalQueue, ev)
		return
	}
	m.approval = m.buildApprovalState(ev)
}

// nextQueuedApproval advances to the next queued permission.request, if any,
// after the current overlay is resolved by the daemon.
func (m *Model) nextQueuedApproval() {
	m.approval = nil
	for len(m.approvalQueue) > 0 {
		ev := m.approvalQueue[0]
		m.approvalQueue = m.approvalQueue[1:]
		if m.approvalResolved[str(ev["decision_id"])] {
			continue
		}
		m.approval = m.buildApprovalState(ev)
		if m.approval != nil {
			return
		}
	}
	if len(m.questionQueue) > 0 {
		ev := m.questionQueue[0]
		m.questionQueue = m.questionQueue[1:]
		m.question = buildQuestionState(ev)
	}
}

func (m *Model) observeApprovalResolution(ev map[string]any) {
	payload, _ := ev["payload"].(map[string]any)
	if str(payload["status"]) != "approval_resolved" {
		return
	}
	decisionID := str(payload["decision_id"])
	if decisionID == "" {
		decisionID = str(ev["permission_decision_id"])
	}
	if decisionID == "" {
		return
	}
	m.approvalResolved[decisionID] = true
	filtered := m.approvalQueue[:0]
	for _, queued := range m.approvalQueue {
		if str(queued["decision_id"]) != decisionID {
			filtered = append(filtered, queued)
		}
	}
	m.approvalQueue = filtered
	if m.approval != nil && m.approval.DecisionID == decisionID {
		if m.approval.Resolving {
			initiator := "policy"
			if !m.approval.PendingAllow {
				initiator = "user"
			}
			scope := str(payload["scope"])
			if scope == "" {
				scope = m.approval.PendingScope
			}
			m.approvalPending[decisionID] = approvalResolutionSnapshot{
				granted:   boolValue(payload["granted"]),
				scope:     scope,
				action:    m.approval.Action,
				initiator: initiator,
			}
		}
		m.nextQueuedApproval()
	}
}

func boolValue(value any) bool {
	b, _ := value.(bool)
	return b
}

func (m *Model) buildApprovalState(ev map[string]any) *approvalState {
	ap := &approvalState{
		DecisionID: str(ev["decision_id"]),
		Action:     sanitize(str(ev["capability"])),
		Resource:   sanitize(str(ev["resource"])),
		Reason:     sanitize(str(ev["reason"])),
		Label:      sanitize(str(ev["label"])),
	}
	if diff := str(ev["diff"]); diff != "" {
		ap.Body = ColorDiff(sanitize(diff), m.th)
	}
	if ap.DecisionID == "" {
		return nil
	}
	return ap
}

// resolveApproval resolves the open overlay over RPC. The scope is explicit in
// the request; the daemon returns the scope it actually installed so a failed
// durable grant can never be presented as broader than a one-time approval.
func (m *Model) resolveApproval(scope string, allow bool) tea.Cmd {
	ap, call, sid := m.approval, m.call, m.sessionID
	if ap == nil || ap.Resolving {
		return nil
	}
	// Enter the busy state before returning the command. Bubble Tea commands
	// run asynchronously, so this synchronous transition is the lock that
	// prevents rapid repeated keypresses from issuing duplicate decisions.
	ap.Resolving = true
	ap.PendingAllow = allow
	ap.PendingScope = scope
	ap.Error = ""
	return func() tea.Msg {
		if call == nil {
			return approvalDoneMsg{decisionID: ap.DecisionID, err: errors.New("daemon not connected")}
		}
		if !allow {
			var dec kernel.Decision
			err := call.Call("task.action.deny", map[string]any{
				"session_id":  sid,
				"decision_id": ap.DecisionID,
				"reason":      "denied by operator in carina-tui",
			}, &dec)
			if err != nil {
				return approvalDoneMsg{decisionID: ap.DecisionID, err: err}
			}
			return approvalDoneMsg{
				verdict: "denied", initiator: "user", scope: scope,
				action: ap.Action, decisionID: ap.DecisionID,
			}
		}
		var out struct {
			Decision *kernel.Decision `json:"decision"`
			Result   json.RawMessage  `json:"result"`
			Scope    string           `json:"scope"`
			GrantErr string           `json:"grant_error"`
		}
		err := call.Call("task.action.approve", map[string]any{
			"session_id":  sid,
			"decision_id": ap.DecisionID,
			"approver":    "operator",
			"scope":       scope,
		}, &out)
		if err != nil {
			return approvalDoneMsg{decisionID: ap.DecisionID, err: err}
		}
		verdict, detail := "allowed", ""
		if out.Decision != nil {
			verdict = out.Decision.Decision
			detail = out.Decision.Reason
		}
		actualScope := scope
		if out.Scope != "" {
			actualScope = out.Scope
		}
		msg := approvalDoneMsg{
			verdict: verdict, scope: actualScope,
			action: ap.Action, decisionID: ap.DecisionID, detail: detail,
		}
		if out.GrantErr != "" {
			msg.detail = "requested " + scope + " scope was not persisted: " + out.GrantErr
			msg.initiator = "grant-error"
		}
		if verdict != "allowed" {
			// The operator said yes but the kernel still refused: a policy
			// denial, distinct from a user denial in outcome and exit code.
			msg.initiator = "policy"
		}
		return msg
	}
}

// handleApprovalDone renders the verdict in the Governed register and tracks
// the governance outcome.
func (m *Model) handleApprovalDone(msg approvalDoneMsg) {
	// A reconnect or an externally observed resolution can replace the active
	// overlay while an RPC is in flight. A late reply must never close the next
	// decision in the queue.
	if m.approval == nil || m.approval.DecisionID != msg.decisionID {
		if snapshot, ok := m.approvalPending[msg.decisionID]; ok {
			delete(m.approvalPending, msg.decisionID)
			if msg.err != nil {
				msg = snapshot.approvalDone(msg.decisionID)
			}
			m.recordApprovalOutcome(msg)
		}
		return
	}
	if msg.err != nil {
		m.approval.Resolving = false
		m.approval.Error = "Approval failed: " + msg.err.Error() + ". Press the decision key to retry."
		m.push(fmt.Sprintf("%s approval RPC failed: %s", glyphFailed(m.th), msg.err.Error()))
		return
	}
	m.nextQueuedApproval()
	m.recordApprovalOutcome(msg)
}

func (snapshot approvalResolutionSnapshot) approvalDone(decisionID string) approvalDoneMsg {
	verdict := "denied"
	if snapshot.granted {
		verdict = "allowed"
	}
	return approvalDoneMsg{
		verdict: verdict, initiator: snapshot.initiator, scope: snapshot.scope,
		action: snapshot.action, decisionID: decisionID,
		detail: "decision was confirmed by the durable event stream before the RPC acknowledgement",
	}
}

func (m *Model) recordApprovalOutcome(msg approvalDoneMsg) {
	sequence := m.approvalOrder[msg.decisionID]
	updatesOutcome := sequence > 0 && sequence >= m.approvalOutcomeSeq
	if sequence == 0 {
		updatesOutcome = m.approvalOutcomeSeq == 0
	}
	if updatesOutcome && sequence > 0 {
		m.approvalOutcomeSeq = sequence
	}
	opts := []microcopy.Option{microcopy.WithLocale(m.locale)}
	switch msg.verdict {
	case "allowed":
		m.push(fmt.Sprintf("%s %s", glyphOK(m.th), microcopy.Governed(microcopy.GovernedApprovalGranted, microcopy.Args{
			"action":      msg.action,
			"scope":       msg.scope,
			"decision_id": msg.decisionID,
		}, opts...)))
		if updatesOutcome {
			m.outcome = OutcomeOK
		}
		if msg.initiator == "grant-error" && msg.detail != "" {
			m.push(fmt.Sprintf("%s %s", glyphNeutral(m.th), msg.detail))
		}
	default:
		m.push(fmt.Sprintf("%s %s", glyphFailed(m.th), microcopy.Governed(microcopy.GovernedApprovalDenied, microcopy.Args{
			"action":      msg.action,
			"decision_id": msg.decisionID,
		}, opts...)))
		if msg.initiator == "policy" {
			if msg.detail != "" {
				m.push(fmt.Sprintf("%s policy: %s", glyphNeutral(m.th), msg.detail))
			}
			if updatesOutcome {
				m.outcome = OutcomePolicyDenied
			}
		} else {
			if updatesOutcome {
				m.outcome = OutcomeUserDenied
			}
		}
	}
}

// approvalKey owns every key while an approval is visible. Keeping the
// entire keymap here makes the state transition and its visual affordance one
// unit: the caller only needs to delegate before ordinary prompt handling.
func (m *Model) approvalKey(key string) (tea.Cmd, bool) {
	if m.approval == nil {
		return nil, false
	}
	ap := m.approval
	if ap.Resolving {
		return nil, true
	}
	switch key {
	case "y", "1", "enter":
		return m.resolveApproval("once", true), true
	case "2":
		return m.resolveApproval("session", true), true
	case "3":
		return m.resolveApproval("project", true), true
	case "n", "4":
		return m.resolveApproval("deny", false), true
	case "esc":
		// Escape is a governance action, not a local dismissal: map it to the
		// daemon's explicit deny RPC and keep the overlay resolving until ACK.
		return m.resolveApproval("deny", false), true
	case "up", "k":
		ap.Scroll--
	case "down", "j":
		ap.Scroll++
	case "pgup":
		ap.Scroll -= m.approvalViewportHeight()
	case "pgdown", " ":
		ap.Scroll += m.approvalViewportHeight()
	case "home":
		ap.Scroll = 0
	case "end":
		ap.Scroll = len(m.approvalBodyLines())
	default:
		return nil, true
	}
	m.clampApprovalScroll()
	return nil, true
}

func (m *Model) approvalBodyLines() []string {
	ap := m.approval
	if ap == nil {
		return nil
	}
	width := m.approvalContentWidth()
	body := make([]string, 0, len(ap.Body)+2)
	appendWrapped := func(line string) {
		body = append(body, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	if ap.Label != "" {
		appendWrapped("$ " + ap.Label)
	}
	if ap.Resource != "" && ap.Resource != ap.Label {
		appendWrapped("resource: " + ap.Resource)
	}
	if ap.Reason != "" {
		appendWrapped("policy: " + ap.Reason)
	}
	for _, line := range ap.Body {
		appendWrapped(line)
	}
	if len(body) == 0 {
		appendWrapped(ap.Resource)
	}
	return body
}

func (m *Model) approvalContentWidth() int {
	return maxInt(m.width-8, 1)
}

func (m *Model) approvalViewportHeight() int {
	// Border (2), title/spacing (2), and footer/spacing (2) stay fixed.
	reserved := 6
	if m.approval != nil && m.approval.Error != "" {
		reserved++
	}
	if m.ctrlCHint != "" {
		reserved++
	}
	return maxInt(m.height-reserved, 1)
}

func (m *Model) clampApprovalScroll() {
	if m.approval == nil {
		return
	}
	maxScroll := maxInt(len(m.approvalBodyLines())-m.approvalViewportHeight(), 0)
	if m.approval.Scroll < 0 {
		m.approval.Scroll = 0
	}
	if m.approval.Scroll > maxScroll {
		m.approval.Scroll = maxScroll
	}
}

// overlayView renders the approval overlay: Governed-register title, the
// reviewable artifact as the body, and the structured scope options.
func (m *Model) overlayView() string {
	ap := m.approval
	if ap == nil {
		return ""
	}
	title := microcopy.Governed(microcopy.GovernedApprovalRequired, microcopy.Args{
		"action":      ap.Action,
		"path":        ap.Resource,
		"decision_id": ap.DecisionID,
	}, microcopy.WithLocale(m.locale))

	// Reserve horizontal padding and border cells so fitted rows stay inside
	// the terminal even when the modal is centered by the parent view.
	contentWidth := m.approvalContentWidth()
	body := m.approvalBodyLines()
	m.clampApprovalScroll()
	start := ap.Scroll
	end := minInt(start+m.approvalViewportHeight(), len(body))
	visibleBody := body[start:end]

	footer := "[y/1/enter] approve once  [2] session  [3] project  [esc/n/4] deny"
	if contentWidth < 56 {
		footer = "[1] allow once  [2/3] broader  [esc/4] deny"
	}
	if contentWidth < 34 {
		footer = "[1] allow  [esc/4] deny"
	}
	if ap.Resolving {
		footer = "Resolving decision..."
	} else if len(body) > m.approvalViewportHeight() {
		if contentWidth >= 56 {
			footer += fmt.Sprintf("  [up/down/pgup/pgdown] scroll %d-%d/%d", start+1, end, len(body))
		} else {
			footer += fmt.Sprintf("  %d-%d/%d", start+1, end, len(body))
		}
	}
	footer = m.th.Style(theme.RoleMuted).Render(footer)

	lines := []string{fitRenderedLine(m.th.Style(theme.RoleWarning).Render(title), contentWidth), ""}
	for _, line := range visibleBody {
		lines = append(lines, fitRenderedLine(line, contentWidth))
	}
	if ap.Error != "" {
		lines = append(lines, fitRenderedLine(m.th.Style(theme.RoleError).Render(sanitize(ap.Error)), contentWidth))
	}
	if m.ctrlCHint != "" {
		lines = append(lines, fitRenderedLine(m.th.Style(theme.RoleMuted).Render(m.ctrlCHint), contentWidth))
	}
	// Keep the action footer immediately above the bottom border. fitViewBlock
	// preserves that row even when a terminal is only one or two lines tall.
	lines = append(lines, "", fitRenderedLine(footer, contentWidth))
	box := strings.Join(lines, "\n")

	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1)
	if c := m.th.Color(theme.RoleWarning); c != nil {
		style = style.BorderForeground(c)
	}
	return style.Render(box)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
