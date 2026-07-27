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
	case "ToolRequested":
		if request, ok := projectGovernanceRequest(value); ok {
			if cursor > 0 {
				request["raw_cursor"] = cursor
			}
			return request, true
		}
		return nil, false
	case "ToolApproved", "ToolDenied":
		return nil, false
	default:
		if cursor > 0 {
			value["raw_cursor"] = cursor
		}
		return value, true
	}
}

func projectGovernanceRequest(event map[string]any) (map[string]any, bool) {
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		return nil, false
	}
	status, _ := payload["status"].(string)
	var eventType, idKey string
	switch status {
	case "permission_requested":
		eventType, idKey = "permission.request", "decision_id"
	case "user_question_requested":
		eventType, idKey = "user.question", "question_id"
	default:
		return nil, false
	}
	request, ok := payload["request"].(map[string]any)
	if !ok {
		return nil, false
	}
	projected := make(map[string]any, len(request)+6)
	for key, value := range request {
		projected[key] = value
	}
	projected["type"] = eventType
	if projected[idKey] == nil || projected[idKey] == "" {
		projected[idKey] = payload[idKey]
	}
	for _, key := range []string{"event_id", "session_id", "task_id", "actor", "timestamp"} {
		if projected[key] == nil || projected[key] == "" {
			projected[key] = event[key]
		}
	}
	return projected, true
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
