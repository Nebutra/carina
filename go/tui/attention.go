package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const (
	attentionTitle         = "Carina"
	attentionBell          = "\a"
	attentionDedupCapacity = 256
)

// attentionEventText deliberately returns fixed copy. Runtime-controlled text
// must never be interpolated into OSC payloads, where terminal controls would
// otherwise become a command-injection boundary.
func (m *Model) attentionEventText(ev map[string]any) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(str(ev["type"]))) {
	case "permission.request":
		return m.text(MsgAttentionApproval, nil), true
	case "user.question":
		return m.text(MsgAttentionInput, nil), true
	case "task.completed", "taskcomplete", "taskcompleted", "task.failed", "task.cancelled", "task.canceled":
		return m.text(MsgAttentionTaskFinished, nil), true
	default:
		return "", false
	}
}

func attentionEventKey(ev map[string]any) string {
	typ := strings.ToLower(strings.TrimSpace(str(ev["type"])))
	family := typ
	var id string
	switch typ {
	case "permission.request":
		family = "permission"
		id = attentionValue(ev, "decision_id", "permission_decision_id")
	case "user.question":
		family = "question"
		id = attentionValue(ev, "question_id")
	case "task.completed", "taskcomplete", "taskcompleted", "task.failed", "task.cancelled", "task.canceled":
		family = "task-terminal"
		id = attentionValue(ev, "task_id")
	}
	if id != "" {
		return family + ":" + id
	}
	canonical := strings.Join([]string{
		family,
		attentionValue(ev, "task_id"),
		attentionValue(ev, "status"),
		attentionValue(ev, "capability"),
		attentionValue(ev, "resource"),
		attentionValue(ev, "prompt"),
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return family + ":sha256:" + hex.EncodeToString(sum[:])
}

func attentionValue(ev map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(str(ev[key])); value != "" {
			return value
		}
	}
	payload, _ := ev["payload"].(map[string]any)
	for _, key := range keys {
		if value := strings.TrimSpace(str(payload[key])); value != "" {
			return value
		}
	}
	return ""
}

func (m *Model) rememberAttentionEvent(key string) bool {
	if m.attentionSeen == nil {
		m.attentionSeen = make(map[string]struct{}, attentionDedupCapacity)
	}
	if _, exists := m.attentionSeen[key]; exists {
		return false
	}
	if len(m.attentionOrder) >= attentionDedupCapacity {
		oldest := m.attentionOrder[0]
		copy(m.attentionOrder, m.attentionOrder[1:])
		m.attentionOrder = m.attentionOrder[:len(m.attentionOrder)-1]
		delete(m.attentionSeen, oldest)
	}
	m.attentionSeen[key] = struct{}{}
	m.attentionOrder = append(m.attentionOrder, key)
	return true
}

// noteAttention mirrors Kaku's lost-focus notification latch: all important
// events remain countable, but the terminal is alerted at most once until the
// operator focuses the TUI again. OSC 9 and OSC 777 cover common host-terminal
// notification protocols; the leading BEL is the portable fallback.
func (m *Model) noteAttention(ev map[string]any) tea.Cmd {
	message, important := m.attentionEventText(ev)
	if !important || !m.rememberAttentionEvent(attentionEventKey(ev)) || !m.terminalBlurred {
		return nil
	}
	m.unreadAttention++
	if m.attentionAlerted {
		return nil
	}
	m.attentionAlerted = true
	return tea.Raw(attentionBell +
		"\x1b]9;" + message + "\x07" +
		"\x1b]777;notify;" + attentionTitle + ";" + message + "\x07")
}

func (m *Model) terminalBlurredNow() {
	m.terminalBlurred = true
	m.unreadAttention = 0
	m.attentionAlerted = false
}

func (m *Model) terminalFocusedNow() {
	m.terminalBlurred = false
	m.unreadAttention = 0
	m.attentionAlerted = false
}
