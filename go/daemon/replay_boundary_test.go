package daemon

import (
	"encoding/json"
	"testing"
)

func TestParseEventStreamRequestRejectsCompatReplayTail(t *testing.T) {
	_, _, _, _, err := parseEventStreamRequest(json.RawMessage(`{"session_id":"s","replay_tail_version":1,"event_mode":"compat"}`))
	if err == nil {
		t.Fatal("compat replay tail accepted")
	}
	_, _, _, _, err = parseEventStreamRequest(json.RawMessage(`{"session_id":"s","replay_tail_version":2,"event_mode":"canonical"}`))
	if err == nil {
		t.Fatal("unknown replay tail version accepted")
	}
}

func TestParseEventStreamRequestLegacyOmitsTailVersion(t *testing.T) {
	sessionID, since, mode, version, err := parseEventStreamRequest(json.RawMessage(`{"session_id":"s","since":4}`))
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "s" || since != 4 || mode != eventModeCompat || version != 0 {
		t.Fatalf("legacy parse = %s %d %s %d", sessionID, since, mode, version)
	}
}

func TestParseEventStreamRequestAcceptsCanonicalV1(t *testing.T) {
	sessionID, since, mode, version, err := parseEventStreamRequest(json.RawMessage(`{"session_id":"s","since":3,"event_mode":"canonical","replay_tail_version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "s" || since != 3 || mode != eventModeCanonical || version != 1 {
		t.Fatalf("v1 parse = %s %d %s %d", sessionID, since, mode, version)
	}
}

func TestAuthorizeReplayTailStaysDisabledByDefault(t *testing.T) {
	d := &Daemon{}
	if d.replayTailAdvertised() {
		t.Fatal("event_replay_tail advertised by default")
	}
	if err := d.authorizeReplayTail(replayTailVersionV1, 1); err == nil || err.Error() == "" {
		t.Fatalf("disabled capability accepted: %v", err)
	}
	d.replayTailV1 = true
	if err := d.authorizeReplayTail(replayTailVersionV1, 2); err == nil {
		t.Fatal("non-first request accepted")
	}
	if err := d.authorizeReplayTail(replayTailVersionV1, 1); err == nil {
		t.Fatal("missing runtime identity accepted")
	}
}

func TestValidateReplayBoundaryAndSnapshots(t *testing.T) {
	boundary := ReplayBoundaryV1{
		Version: 1, SessionID: "sess", RuntimeID: "rt", RuntimeEpoch: "ep",
		RuntimeProcessEpoch: 2, RequestedSince: 4, DurableCursor: 7,
		DurableReplayed: 3, TransientTailRevision: 9, TransientSnapshots: 1, BufferedLive: 2,
	}
	if err := validateReplayBoundary(boundary); err != nil {
		t.Fatal(err)
	}
	snapshot := AssistantMessageSnapshot{
		Type: assistantMessageSnapshotType, SessionID: "sess", RunID: "run", Actor: "model",
		Payload: AssistantMessageSnapshotBody{
			Generation: 2, Sequence: 3, Phase: "final_answer", Content: "hi",
			TailRevision: 9, State: assistantTailStateOpen,
		},
	}
	if err := validateAssistantSnapshot(snapshot, boundary); err != nil {
		t.Fatal(err)
	}
	if err := validateReplayAttachment(boundary, 1, 3, 2); err != nil {
		t.Fatal(err)
	}
	newer := snapshot
	newer.Payload.TailRevision = 10
	if err := validateAssistantSnapshot(newer, boundary); err == nil {
		t.Fatal("newer tail revision accepted")
	}
	if err := validateReplayAttachment(boundary, 0, 3, 2); err == nil {
		t.Fatal("count mismatch accepted")
	}
	zero := boundary
	zero.Version = 0
	if err := validateReplayBoundary(zero); err == nil {
		t.Fatal("zero version accepted")
	}
}
