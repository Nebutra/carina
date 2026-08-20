package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	maxTodoItems        = 16
	maxTodoContentRunes = 160
)

// todoItem is one row on the session checklist. Models may author it as
// content/text/task/step; the stored and projected shape is content+status.
type todoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

func (t *todoItem) UnmarshalJSON(raw []byte) error {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	t.Content = firstTodoString(obj, "content", "text", "task", "description", "step", "title")
	t.Status = firstTodoString(obj, "status")
	return nil
}

func firstTodoString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := obj[key].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func (a *action) incomingChecklist() ([]todoItem, bool) {
	if a == nil {
		return nil, false
	}
	switch {
	case a.Todos != nil:
		return a.Todos, true
	case a.Items != nil:
		return a.Items, true
	case a.Plan != nil:
		return a.Plan, true
	default:
		return nil, false
	}
}

func normalizeTodoStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "in_progress", "in-progress", "in progress", "doing", "active":
		return "in_progress"
	case "completed", "complete", "done", "checked":
		return "completed"
	default:
		return "pending"
	}
}

func coerceTodoItems(items []todoItem) ([]todoItem, error) {
	if len(items) > maxTodoItems {
		return nil, fmt.Errorf("todo accepts at most %d items", maxTodoItems)
	}
	out := make([]todoItem, 0, len(items))
	inProgress := false
	for _, item := range items {
		content := strings.Join(strings.Fields(item.Content), " ")
		if content == "" {
			continue
		}
		if utf8.RuneCountInString(content) > maxTodoContentRunes {
			runes := []rune(content)
			content = string(runes[:maxTodoContentRunes-1]) + "…"
		}
		status := normalizeTodoStatus(item.Status)
		if status == "in_progress" {
			if inProgress {
				status = "pending"
			} else {
				inProgress = true
			}
		}
		out = append(out, todoItem{Content: content, Status: status})
	}
	return out, nil
}

func renderTodoChecklist(items []todoItem) string {
	if len(items) == 0 {
		return "checklist empty"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "checklist (%d):\n", len(items))
	for _, item := range items {
		mark := "[ ]"
		suffix := ""
		switch item.Status {
		case "completed":
			mark = "[x]"
		case "in_progress":
			suffix = " (in_progress)"
		}
		fmt.Fprintf(&b, "- %s %s%s\n", mark, item.Content, suffix)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (d *Daemon) sessionTodos(sessionID string) []todoItem {
	if d == nil || sessionID == "" {
		return nil
	}
	raw, ok := d.todos.Load(sessionID)
	if !ok {
		return nil
	}
	items, _ := raw.([]todoItem)
	return items
}

func (d *Daemon) setSessionTodos(sessionID string, items []todoItem) {
	if d == nil || sessionID == "" {
		return
	}
	if len(items) == 0 {
		d.todos.Delete(sessionID)
		return
	}
	stored := make([]todoItem, len(items))
	copy(stored, items)
	d.todos.Store(sessionID, stored)
}

func (d *Daemon) executeTodoOutcome(sess *sessionstore.Session, _ *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	incoming, replace := act.incomingChecklist()
	if !replace {
		return toolCompleted(renderTodoChecklist(d.sessionTodos(sess.SessionID)))
	}
	items, err := coerceTodoItems(incoming)
	if err != nil {
		return toolFailed("todo error: "+err.Error(), "invalid_input")
	}
	d.setSessionTodos(sess.SessionID, items)
	return toolCompleted(renderTodoChecklist(items))
}
