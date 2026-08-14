package daemon

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	replayTailVersionV1             = 1
	assistantMessageSnapshotType    = "assistant.message.snapshot"
	assistantTailStateOpen          = "open"
	assistantTailStateAwaitingFinal = "awaiting_canonical"
)

// ReplayBoundaryV1 is the typed subscribe ACK attachment for replay-tail v1.
// It is a transport projection, not a durable audit event.
type ReplayBoundaryV1 struct {
	Version               int    `json:"version"`
	SessionID             string `json:"session_id"`
	RuntimeID             string `json:"runtime_id"`
	RuntimeEpoch          string `json:"runtime_epoch"`
	RuntimeProcessEpoch   int64  `json:"runtime_process_epoch"`
	RequestedSince        int    `json:"requested_since"`
	DurableCursor         int    `json:"durable_cursor"`
	DurableReplayed       int    `json:"durable_replayed"`
	TransientTailRevision int    `json:"transient_tail_revision"`
	TransientSnapshots    int    `json:"transient_snapshots"`
	BufferedLive          int    `json:"buffered_live"`
}

// AssistantMessageSnapshot is the optional pre-ACK transport frame for a
// public assistant tail. It must never enter the durable event catalog.
type AssistantMessageSnapshot struct {
	Type      string                       `json:"type"`
	SessionID string                       `json:"session_id"`
	RunID     string                       `json:"run_id"`
	Actor     string                       `json:"actor"`
	Payload   AssistantMessageSnapshotBody `json:"payload"`
}

type AssistantMessageSnapshotBody struct {
	Generation       int    `json:"generation"`
	Sequence         int    `json:"sequence"`
	Phase            string `json:"phase"`
	Content          string `json:"content"`
	StructuredOutput bool   `json:"structured_output"`
	TailRevision     int    `json:"tail_revision"`
	State            string `json:"state"`
}

func parseEventStreamRequest(params json.RawMessage) (sessionID string, since int, mode eventMode, tailVersion int, err error) {
	var p struct {
		SessionID         string `json:"session_id"`
		Since             int    `json:"since"`
		EventMode         string `json:"event_mode"`
		ReplayTailVersion *int   `json:"replay_tail_version"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return "", 0, "", 0, fmt.Errorf("invalid params: %w", err)
	}
	if strings.TrimSpace(p.SessionID) == "" {
		return "", 0, "", 0, fmt.Errorf("session_id required")
	}
	mode, err = parseEventMode(p.EventMode)
	if err != nil {
		return "", 0, "", 0, err
	}
	if p.Since < 0 {
		p.Since = 0
	}
	if p.ReplayTailVersion == nil {
		return p.SessionID, p.Since, mode, 0, nil
	}
	if *p.ReplayTailVersion != replayTailVersionV1 {
		return "", 0, "", 0, fmt.Errorf("replay_tail_version must be %d", replayTailVersionV1)
	}
	if mode != eventModeCanonical {
		return "", 0, "", 0, fmt.Errorf("replay_tail_version=%d requires event_mode=canonical", replayTailVersionV1)
	}
	return p.SessionID, p.Since, mode, replayTailVersionV1, nil
}

func validateReplayBoundary(boundary ReplayBoundaryV1) error {
	if boundary.Version != replayTailVersionV1 {
		return fmt.Errorf("replay_boundary.version must be %d", replayTailVersionV1)
	}
	if strings.TrimSpace(boundary.SessionID) == "" {
		return fmt.Errorf("replay_boundary.session_id is required")
	}
	if strings.TrimSpace(boundary.RuntimeID) == "" {
		return fmt.Errorf("replay_boundary.runtime_id is required")
	}
	if strings.TrimSpace(boundary.RuntimeEpoch) == "" {
		return fmt.Errorf("replay_boundary.runtime_epoch is required")
	}
	if boundary.RuntimeProcessEpoch < 0 {
		return fmt.Errorf("replay_boundary.runtime_process_epoch must be >= 0")
	}
	if boundary.RequestedSince < 0 || boundary.DurableCursor < 0 || boundary.DurableReplayed < 0 ||
		boundary.TransientTailRevision < 0 || boundary.TransientSnapshots < 0 || boundary.BufferedLive < 0 {
		return fmt.Errorf("replay_boundary counts must be >= 0")
	}
	if boundary.DurableCursor < boundary.RequestedSince {
		return fmt.Errorf("replay_boundary.durable_cursor is below requested_since")
	}
	return nil
}

func validateAssistantSnapshot(snapshot AssistantMessageSnapshot, boundary ReplayBoundaryV1) error {
	if snapshot.Type != assistantMessageSnapshotType {
		return fmt.Errorf("snapshot type must be %s", assistantMessageSnapshotType)
	}
	if snapshot.SessionID != boundary.SessionID {
		return fmt.Errorf("snapshot session_id does not match replay_boundary")
	}
	if strings.TrimSpace(snapshot.RunID) == "" {
		return fmt.Errorf("snapshot run_id is required")
	}
	if snapshot.Payload.Generation <= 0 || snapshot.Payload.Sequence <= 0 || snapshot.Payload.TailRevision <= 0 {
		return fmt.Errorf("snapshot generation, sequence, and tail_revision must be positive")
	}
	if strings.TrimSpace(snapshot.Payload.Phase) == "" {
		return fmt.Errorf("snapshot phase is required")
	}
	switch snapshot.Payload.State {
	case assistantTailStateOpen, assistantTailStateAwaitingFinal:
	default:
		return fmt.Errorf("snapshot state must be open or awaiting_canonical")
	}
	if snapshot.Payload.TailRevision > boundary.TransientTailRevision {
		return fmt.Errorf("snapshot tail_revision is newer than the advertised cut")
	}
	return nil
}

func validateReplayAttachment(boundary ReplayBoundaryV1, snapshots int, durable int, live int) error {
	if err := validateReplayBoundary(boundary); err != nil {
		return err
	}
	if snapshots != boundary.TransientSnapshots || durable != boundary.DurableReplayed || live != boundary.BufferedLive {
		return fmt.Errorf("replay attachment counts do not match replay_boundary")
	}
	if snapshots > 0 && boundary.TransientTailRevision <= 0 {
		return fmt.Errorf("transient snapshots require a positive transient_tail_revision")
	}
	return nil
}

func (d *Daemon) replayRuntimeIdentity() (runtimeID, runtimeEpoch string, processEpoch int64, ok bool) {
	if d == nil {
		return "", "", 0, false
	}
	desc := d.runtimeDescription()
	runtimeID, _ = desc["runtime_id"].(string)
	runtimeEpoch, _ = desc["epoch"].(string)
	switch value := desc["process_epoch"].(type) {
	case int64:
		processEpoch = value
	case int:
		processEpoch = int64(value)
	}
	return runtimeID, runtimeEpoch, processEpoch, strings.TrimSpace(runtimeID) != "" && strings.TrimSpace(runtimeEpoch) != ""
}

func (d *Daemon) replayTailAdvertised() bool {
	return d != nil && d.replayTailV1
}

func (d *Daemon) authorizeReplayTail(version, requestSeq int) error {
	if version != replayTailVersionV1 {
		return nil
	}
	if !d.replayTailAdvertised() {
		return fmt.Errorf("replay_tail_version requires the event_replay_tail capability")
	}
	if requestSeq != 1 {
		return fmt.Errorf("replay_tail_version=%d must be the first request on a fresh connection", replayTailVersionV1)
	}
	if _, _, _, ok := d.replayRuntimeIdentity(); !ok {
		return fmt.Errorf("replay_tail_version=%d requires a workspace runtime identity", replayTailVersionV1)
	}
	return nil
}
