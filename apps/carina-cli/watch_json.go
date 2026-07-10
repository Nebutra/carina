package main

// controlFrameForEvent converts a raw session.events.stream event payload
// into a typed NDJSON control frame for `carina watch --json`. Permission
// requests are resolved by a second `carina approve`/`carina deny` command;
// structured user questions are resolved by `carina answer`. The stream stays
// output-only instead of inventing a second bidirectional stdin protocol.
//
// ok is false for events that are neither an actionable permission request
// nor a structured user question (or are malformed). Ordinary audit events
// remain out of the pipe-mode control stream.
//
// Builds a {"frame":"control_request","decision_id":...,"capability":...,
// "resource":...,"reason":...,"diff":...} frame from a permission.request
// event's fields (go/daemon/approval.go's awaitInteractiveApproval already
// publishes decision_id, capability, resource, reason, label, and diff for
// patches on that event type). A resolving script greps stdout for
// frame=control_request and shells out to the already-working `carina
// approve`/`carina deny <session_id> <decision_id>` — no new bidirectional
// stdin-frame protocol.
func controlFrameForEvent(event map[string]any) (frame map[string]any, ok bool) {
	switch event["type"] {
	case "permission.request":
		decisionID, _ := event["decision_id"].(string)
		if decisionID == "" {
			return nil, false
		}
		out := map[string]any{
			"frame":       "control_request",
			"decision_id": decisionID,
		}
		for _, key := range []string{"capability", "resource", "reason", "label", "diff", "session_id", "task_id"} {
			if v, present := event[key]; present {
				out[key] = v
			}
		}
		return out, true
	case "user.question":
		questionID, _ := event["question_id"].(string)
		prompt, _ := event["prompt"].(string)
		options, hasOptions := event["options"]
		if questionID == "" || prompt == "" || !hasOptions {
			return nil, false
		}
		out := map[string]any{
			"frame":       "user_question",
			"question_id": questionID,
			"prompt":      prompt,
			"options":     options,
		}
		for _, key := range []string{"session_id", "task_id"} {
			if v, present := event[key]; present {
				out[key] = v
			}
		}
		return out, true
	default:
		return nil, false
	}
}
