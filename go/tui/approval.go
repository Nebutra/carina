package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/Nebutra/carina/go/kernel"
	"github.com/Nebutra/carina/go/microcopy"
	"github.com/Nebutra/carina/go/tui/theme"
)

// approvalState is the open approval overlay: the reviewable artifact plus
// the decision_id it resolves. The body is a colored unified diff when the
// gated action carries one, otherwise the canonicalized command/resource.
type approvalState struct {
	DecisionID string
	Action     string // capability, e.g. command.exec
	Resource   string
	Reason     string // the policy rule that fired
	Label      string
	Body       []string // pre-rendered diff lines, when present
}

// openApproval builds the overlay from a permission.request envelope. A
// second request arriving while one overlay is already open is queued
// rather than replacing it: the first decision is still genuinely pending
// server-side (a real task is blocked on it), so silently swapping it out
// would let a keypress resolve the wrong decision_id and orphan the first
// approval until it times out.
func (m *Model) openApproval(ev map[string]any) {
	if m.approval != nil || m.question != nil {
		m.approvalQueue = append(m.approvalQueue, ev)
		return
	}
	m.approval = m.buildApprovalState(ev)
}

// nextQueuedApproval advances to the next queued permission.request, if any,
// after the current overlay is resolved or dismissed.
func (m *Model) nextQueuedApproval() {
	m.approval = nil
	if len(m.approvalQueue) > 0 {
		ev := m.approvalQueue[0]
		m.approvalQueue = m.approvalQueue[1:]
		m.approval = m.buildApprovalState(ev)
		return
	}
	if len(m.questionQueue) > 0 {
		ev := m.questionQueue[0]
		m.questionQueue = m.questionQueue[1:]
		m.question = buildQuestionState(ev)
	}
}

func (m *Model) buildApprovalState(ev map[string]any) *approvalState {
	ap := &approvalState{
		DecisionID: str(ev["decision_id"]),
		Action:     str(ev["capability"]),
		Resource:   str(ev["resource"]),
		Reason:     str(ev["reason"]),
		Label:      str(ev["label"]),
	}
	if diff := str(ev["diff"]); diff != "" {
		ap.Body = ColorDiff(diff, m.th)
	}
	return ap
}

// resolveApproval resolves the open overlay over RPC. The scope is explicit in
// the request; the daemon returns the scope it actually installed so a failed
// durable grant can never be presented as broader than a one-time approval.
func (m *Model) resolveApproval(scope string, allow bool) tea.Cmd {
	ap, call, sid := m.approval, m.call, m.sessionID
	return func() tea.Msg {
		if ap == nil {
			return nil
		}
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
	m.nextQueuedApproval()
	if msg.err != nil {
		m.push(fmt.Sprintf("%s approval RPC failed: %s", glyphFailed(m.th), msg.err.Error()))
		return
	}
	opts := []microcopy.Option{microcopy.WithLocale(m.locale)}
	switch msg.verdict {
	case "allowed":
		m.push(fmt.Sprintf("%s %s", glyphOK(m.th), microcopy.Governed(microcopy.GovernedApprovalGranted, microcopy.Args{
			"action":      msg.action,
			"scope":       msg.scope,
			"decision_id": msg.decisionID,
		}, opts...)))
		m.outcome = OutcomeOK
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
			m.outcome = OutcomePolicyDenied
		} else {
			m.outcome = OutcomeUserDenied
		}
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

	var body []string
	if ap.Label != "" {
		body = append(body, m.th.Style(theme.RoleDiffHunk).Render("$ "+ap.Label))
	}
	if ap.Reason != "" {
		body = append(body, m.th.Style(theme.RoleMuted).Render("policy: "+ap.Reason))
	}
	body = append(body, ap.Body...)

	footer := m.th.Style(theme.RoleSuccess).Render("[y/1] approve once  [2] approve for session  [3] approve for project") +
		"  " + m.th.Style(theme.RoleError).Render("[n/4] deny") +
		m.th.Style(theme.RoleMuted).Render("  [esc] dismiss")

	contentWidth := maxInt(m.width-6, 1)
	lines := []string{fitLine(m.th.Style(theme.RoleWarning).Render(title), contentWidth), ""}
	for _, line := range body {
		lines = append(lines, fitLine(line, contentWidth))
	}
	lines = append(lines, "", fitLine(footer, contentWidth))
	box := strings.Join(lines, "\n")
	if m.ctrlCHint != "" {
		// The overlay owns the whole frame while open (view.go) — the
		// transcript line ctrlC() pushed is not rendered behind it, so the
		// cascading-interrupt hint must be surfaced here too or it is
		// invisible for as long as the approval is pending.
		box += "\n" + m.th.Style(theme.RoleMuted).Render(m.ctrlCHint)
	}

	style := lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).Padding(0, 1).Width(contentWidth)
	if c := m.th.Color(theme.RoleWarning); c != nil {
		style = style.BorderForeground(c)
	}
	return style.Render(box)
}
