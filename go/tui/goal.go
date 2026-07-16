package tui

import (
	"errors"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type goalView struct {
	Objective         string `json:"objective"`
	Status            string `json:"status"`
	AutoContinue      bool   `json:"auto_continue"`
	TokenBudget       int    `json:"token_budget"`
	TokensUsed        int    `json:"tokens_used"`
	TimeUsedSeconds   int64  `json:"time_used_seconds"`
	ContinuationsUsed int    `json:"continuations_used"`
	MaxContinuations  int    `json:"max_continuations"`
}
type goalRPCMsg struct {
	sessionID string
	action    string
	goal      *goalView
	cleared   bool
	err       error
}

func (m *Model) goalCall(action, method string, params map[string]any) tea.Cmd {
	call, sid := m.call, m.sessionID
	return func() tea.Msg {
		if call == nil {
			return goalRPCMsg{sessionID: sid, action: action, err: errors.New("daemon not connected")}
		}
		params["session_id"] = sid
		if method == "goal.get" {
			var out struct {
				Goal *goalView `json:"goal"`
			}
			err := call.Call(method, params, &out)
			return goalRPCMsg{sessionID: sid, action: action, goal: out.Goal, err: err}
		}
		if method == "goal.clear" {
			var out struct {
				Cleared bool `json:"cleared"`
			}
			err := call.Call(method, params, &out)
			return goalRPCMsg{sessionID: sid, action: action, cleared: out.Cleared, err: err}
		}
		if method == "goal.continue" {
			var out map[string]any
			err := call.Call(method, params, &out)
			if err != nil {
				return goalRPCMsg{sessionID: sid, action: action, err: err}
			}
			var refreshed struct {
				Goal *goalView `json:"goal"`
			}
			err = call.Call("goal.get", map[string]any{"session_id": sid}, &refreshed)
			return goalRPCMsg{sessionID: sid, action: action, goal: refreshed.Goal, err: err}
		}
		var out goalView
		err := call.Call(method, params, &out)
		return goalRPCMsg{sessionID: sid, action: action, goal: &out, err: err}
	}
}
func (m *Model) goalCommand(args []string) tea.Cmd {
	if len(args) == 0 {
		return m.goalCall("status", "goal.get", map[string]any{})
	}
	switch args[0] {
	case "clear":
		return m.goalCall("clear", "goal.clear", map[string]any{})
	case "pause":
		return m.goalCall("pause", "goal.pause", map[string]any{})
	case "resume":
		return m.goalCall("resume", "goal.resume", map[string]any{})
	case "complete":
		return m.goalCall("complete", "goal.complete", map[string]any{})
	case "continue":
		return m.goalCall("continue", "goal.continue", map[string]any{})
	default:
		params := map[string]any{}
		var objectiveParts []string
		for len(args) > 0 {
			switch args[0] {
			case "--auto":
				params["auto_continue"] = true
				args = args[1:]
			case "--tokens", "--max-continuations":
				if len(args) < 2 {
					m.push(m.text(MsgUpdateGoalUsage, nil))
					return nil
				}
				value, err := strconv.Atoi(args[1])
				if err != nil || value < 0 || (args[0] == "--max-continuations" && (value < 1 || value > 32)) {
					m.push(m.text(MsgUpdateGoalUsage, nil))
					return nil
				}
				if args[0] == "--tokens" {
					params["token_budget"] = value
				} else {
					params["max_continuations"] = value
				}
				args = args[2:]
			default:
				objectiveParts = append(objectiveParts, args[0])
				args = args[1:]
			}
		}
		objective := strings.TrimSpace(strings.Join(objectiveParts, " "))
		if objective == "" {
			m.push(m.text(MsgUpdateGoalUsage, nil))
			return nil
		}
		params["objective"] = objective
		return m.goalCall("set", "goal.set", params)
	}
}
func (m *Model) handleGoalRPC(msg goalRPCMsg) {
	if msg.err != nil {
		m.push(m.text(MsgUpdateGoalFailed, MessageArgs{"action": msg.action, "error": msg.err.Error()}))
		return
	}
	if msg.action == "clear" {
		m.goal = nil
		m.push(m.text(MsgUpdateGoalCleared, nil))
		m.layout()
		return
	}
	if msg.goal != nil {
		m.goal = msg.goal
	}
	if m.goal == nil {
		m.push(m.text(MsgUpdateGoalNone, nil))
		return
	}
	budget := m.text(MsgUpdateGoalBudgetUnlimited, nil)
	if m.goal.TokenBudget > 0 {
		budget = m.text(MsgUpdateGoalBudgetTokens, MessageArgs{"used": m.goal.TokensUsed, "max": m.goal.TokenBudget})
	}
	continuationMode := "manual"
	if m.goal.AutoContinue {
		continuationMode = "auto"
	}
	m.push(m.text(MsgUpdateGoalState, MessageArgs{"status": m.goal.Status, "objective": m.goal.Objective, "budget": budget, "seconds": m.goal.TimeUsedSeconds, "mode": continuationMode, "used": m.goal.ContinuationsUsed, "max": m.goal.MaxContinuations}))
	m.layout()
}
