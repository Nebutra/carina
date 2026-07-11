package daemon

import (
	"encoding/json"
	"fmt"
)

const internalRawAuditCursor = "__raw_audit_cursor"

type eventMode string

const (
	eventModeCompat    eventMode = "compat"
	eventModeCanonical eventMode = "canonical"
)

func parseEventMode(value string) (eventMode, error) {
	switch eventMode(value) {
	case "", eventModeCompat:
		return eventModeCompat, nil
	case eventModeCanonical:
		return eventModeCanonical, nil
	default:
		return "", fmt.Errorf("unsupported event_mode %q; want compat or canonical", value)
	}
}

func projectEvent(mode eventMode, event any, replayCursor ...int) (any, bool) {
	raw, err := json.Marshal(event)
	if err != nil {
		return event, true
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return event, true
	}
	cursor := 0
	if len(replayCursor) > 0 {
		cursor = replayCursor[0]
	} else if internal, ok := value[internalRawAuditCursor].(float64); ok {
		cursor = int(internal)
	}
	delete(value, internalRawAuditCursor)
	if mode != eventModeCanonical {
		return value, true
	}
	switch value["type"] {
	case "ToolRequested", "ToolApproved", "ToolDenied":
		return nil, false
	default:
		if cursor > 0 {
			value["raw_cursor"] = cursor
		}
		return value, true
	}
}

type projectingSubscriber struct {
	eventSubscriber
	mode eventMode
}

func (s projectingSubscriber) TryNotify(method string, value any) error {
	projected, ok := projectEvent(s.mode, value)
	if !ok {
		return nil
	}
	return s.eventSubscriber.TryNotify(method, projected)
}
