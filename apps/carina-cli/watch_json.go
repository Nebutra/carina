package main

// controlFrameForEvent converts a raw session.events.stream event payload
// into a typed NDJSON control frame for `carina watch --json` (P1.5(c) v1,
// right-sized per the plan's explicit scope reduction: this emits
// control_request frames on permission.request events; the resolving script
// runs a SECOND, already-working command concurrently — `carina approve` /
// `carina deny` — rather than a new bidirectional stdin-frame protocol).
//
// ok is false for events that are not permission.request (or malformed) —
// watch --json only emits a frame for governance-relevant events in this
// v1, not a structured frame per raw event.
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
	if event["type"] != "permission.request" {
		return nil, false
	}
	decisionID, _ := event["decision_id"].(string)
	if decisionID == "" {
		return nil, false
	}
	out := map[string]any{
		"frame":       "control_request",
		"decision_id": event["decision_id"],
	}
	for _, key := range []string{"capability", "resource", "reason", "label", "diff", "session_id", "task_id"} {
		if v, present := event[key]; present {
			out[key] = v
		}
	}
	return out, true
}
