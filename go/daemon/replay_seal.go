package daemon

import (
	"encoding/json"
	"strings"
)

type sealedRunPhase struct {
	RunID string
	Phase string
}

func sealedRunPhasesFromPrefix(events []json.RawMessage) ([]sealedRunPhase, error) {
	seen := make(map[sealedRunPhase]struct{}, len(events))
	out := make([]sealedRunPhase, 0)
	for _, raw := range events {
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			return nil, err
		}
		pair, ok := eventSealsAssistantTail(event)
		if !ok {
			continue
		}
		if _, exists := seen[pair]; exists {
			continue
		}
		seen[pair] = struct{}{}
		out = append(out, pair)
	}
	return out, nil
}

func eventSealsAssistantTail(event map[string]any) (sealedRunPhase, bool) {
	runID := strings.TrimSpace(eventRunID(event))
	if runID == "" {
		return sealedRunPhase{}, false
	}
	payload, _ := event["payload"].(map[string]any)
	switch event["type"] {
	case "ExecutionCompleted", "ExecutionFailed", "ExecutionInterrupted", "ExecutionCancelled":
		return sealedRunPhase{RunID: runID, Phase: assistantPhaseFinalAnswer}, true
	case "ModelResponded":
		if modelRespondedSealsVisibleFinal(payload) {
			return sealedRunPhase{RunID: runID, Phase: assistantPhaseFinalAnswer}, true
		}
	}
	return sealedRunPhase{}, false
}

func modelRespondedSealsVisibleFinal(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	text := stringField(payload, "text")
	if strings.TrimSpace(stringField(payload, "error")) != "" && strings.TrimSpace(text) == "" {
		return true
	}
	if strings.TrimSpace(text) == "" {
		return false
	}
	if strings.TrimSpace(stringField(payload, "presentation_text")) != "" {
		return true
	}
	if act, err := parseAction(text); err == nil {
		return terminalDoneSummary(act, boolField(payload, "structured_output")) != ""
	}
	return !looksLikeActionEnvelope(text)
}

func eventRunID(event map[string]any) string {
	if event == nil {
		return ""
	}
	if runID := strings.TrimSpace(stringField(event, "run_id")); runID != "" {
		return runID
	}
	return strings.TrimSpace(stringField(event, "task_id"))
}

func eventRunPhase(event map[string]any) (sealedRunPhase, bool) {
	runID := eventRunID(event)
	if runID == "" {
		return sealedRunPhase{}, false
	}
	phase := assistantPhaseFinalAnswer
	if payload, ok := event["payload"].(map[string]any); ok {
		if value := strings.TrimSpace(stringField(payload, "phase")); value != "" {
			phase = value
		}
	}
	return sealedRunPhase{RunID: runID, Phase: phase}, true
}

func isTransientAssistantEvent(event map[string]any) bool {
	if event == nil {
		return false
	}
	switch event["type"] {
	case "assistant.message.reset", "assistant.message.delta", "assistant.message.completed", assistantMessageSnapshotType:
		return true
	default:
		return false
	}
}

func sealedSet(pairs []sealedRunPhase) map[sealedRunPhase]struct{} {
	out := make(map[sealedRunPhase]struct{}, len(pairs))
	for _, pair := range pairs {
		if pair.RunID == "" || pair.Phase == "" {
			continue
		}
		out[pair] = struct{}{}
	}
	return out
}

func sealedTransient(event map[string]any, sealed map[sealedRunPhase]struct{}) bool {
	if len(sealed) == 0 || rawAuditCursor(event) > 0 || !isTransientAssistantEvent(event) {
		return false
	}
	pair, ok := eventRunPhase(event)
	if !ok {
		return false
	}
	_, found := sealed[pair]
	return found
}
