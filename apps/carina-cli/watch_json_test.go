package main

import "testing"

// TestParseWatchArgsRecognizesJSONFlag pins the P1.5(c) wiring gap: `carina
// watch <session_id> --json` must be a recognized invocation that requests
// typed control frames, not a raw event dump. Before this, watch() took no
// jsonOut parameter and no flag parser for it existed anywhere in main.go,
// so controlFrameForEvent (fully unit-tested above) was dead code with zero
// production call sites.
func TestParseWatchArgsRecognizesJSONFlag(t *testing.T) {
	sessionID, jsonOut, err := parseWatchArgs([]string{"sess_1", "--json"})
	if err != nil {
		t.Fatalf("parseWatchArgs: unexpected error: %v", err)
	}
	if sessionID != "sess_1" {
		t.Fatalf("sessionID = %q, want %q", sessionID, "sess_1")
	}
	if !jsonOut {
		t.Fatal("jsonOut = false, want true for `carina watch <session_id> --json`")
	}
}

// TestParseWatchArgsWithoutJSONFlag pins the default (raw event dump,
// backward compatible with today's behavior).
func TestParseWatchArgsWithoutJSONFlag(t *testing.T) {
	sessionID, jsonOut, err := parseWatchArgs([]string{"sess_1"})
	if err != nil {
		t.Fatalf("parseWatchArgs: unexpected error: %v", err)
	}
	if sessionID != "sess_1" {
		t.Fatalf("sessionID = %q, want %q", sessionID, "sess_1")
	}
	if jsonOut {
		t.Fatal("jsonOut = true, want false when --json is not passed")
	}
}

// TestParseWatchArgsRequiresSessionID pins the existing usage-error
// behavior: no positional session_id argument is still a usage error
// regardless of --json.
func TestParseWatchArgsRequiresSessionID(t *testing.T) {
	if _, _, err := parseWatchArgs(nil); err == nil {
		t.Fatal("expected a usage error when no session_id is given")
	}
	if _, _, err := parseWatchArgs([]string{"--json"}); err == nil {
		t.Fatal("expected a usage error when only --json is given (no session_id)")
	}
	if _, _, err := parseWatchArgs([]string{""}); err == nil {
		t.Fatal("expected an empty session_id to be rejected")
	}
	if _, _, err := parseWatchArgs([]string{"sess_1", "sess_2"}); err == nil {
		t.Fatal("expected an extra positional argument to be rejected")
	}
}

// TestControlFrameForEventBuildsControlRequestFromPermissionRequest pins
// P1.5(c)'s right-sized v1: on a permission.request event (exactly the
// shape go/daemon/approval.go's awaitInteractiveApproval publishes —
// decision_id, capability, resource, reason, label, and diff for patches),
// `carina watch --json` must emit a typed control_request frame with those
// fields, so a CI bot can grep stdout for frame=control_request and shell
// out to the already-working `carina approve`/`carina deny` — no new
// bidirectional stdin protocol.
func TestControlFrameForEventBuildsControlRequestFromPermissionRequest(t *testing.T) {
	event := map[string]any{
		"type":        "permission.request",
		"session_id":  "sess_1",
		"task_id":     "task_1",
		"decision_id": "perm_abc123",
		"capability":  "PatchApply",
		"resource":    "patch_1",
		"reason":      "policy requires approval for PatchApply",
		"label":       "apply patch to main.go",
		"diff":        "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n",
	}
	frame, ok := controlFrameForEvent(event)
	if !ok {
		t.Fatal("expected ok=true for a permission.request event")
	}
	if frame["frame"] != "control_request" {
		t.Fatalf("frame[\"frame\"] = %v, want control_request", frame["frame"])
	}
	for _, key := range []string{"decision_id", "capability", "resource", "reason", "diff"} {
		if frame[key] != event[key] {
			t.Fatalf("frame[%q] = %v, want %v (must carry the event field through verbatim)", key, frame[key], event[key])
		}
	}
}

// TestControlFrameForEventIgnoresNonPermissionEvents pins the v1 scope
// boundary: watch --json emits a structured frame only for
// governance-relevant events, not a wrapper around every raw event.
func TestControlFrameForEventIgnoresNonPermissionEvents(t *testing.T) {
	event := map[string]any{"type": "CommandStarted", "session_id": "sess_1"}
	_, ok := controlFrameForEvent(event)
	if ok {
		t.Fatal("expected ok=false for a non-permission.request event")
	}
}

// TestControlFrameForEventRequiresDecisionID pins a safety property: a
// malformed/incomplete permission.request event (missing decision_id, the
// field a resolving script needs to call `carina approve <sid>
// <decision_id>`) must not produce a frame a bot could act on blindly.
func TestControlFrameForEventRequiresDecisionID(t *testing.T) {
	event := map[string]any{"type": "permission.request", "capability": "PatchApply"}
	_, ok := controlFrameForEvent(event)
	if ok {
		t.Fatal("expected ok=false for a permission.request event missing decision_id")
	}
}

func TestControlFrameForEventBuildsStructuredUserQuestion(t *testing.T) {
	options := []any{
		map[string]any{"label": "Proceed", "value": "yes"},
		map[string]any{"label": "Stop", "value": "no"},
	}
	event := map[string]any{
		"type": "user.question", "session_id": "sess_1", "task_id": "task_1",
		"question_id": "question_1", "prompt": "Continue?", "options": options,
	}
	frame, ok := controlFrameForEvent(event)
	if !ok {
		t.Fatal("expected a frame for user.question")
	}
	if frame["frame"] != "user_question" || frame["question_id"] != "question_1" || frame["options"] == nil {
		t.Fatalf("unexpected user question frame: %#v", frame)
	}
}

func TestControlFrameForEventRejectsIncompleteUserQuestion(t *testing.T) {
	if _, ok := controlFrameForEvent(map[string]any{"type": "user.question", "question_id": "question_1"}); ok {
		t.Fatal("incomplete user.question must not produce an actionable frame")
	}
}
