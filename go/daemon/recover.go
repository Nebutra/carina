package daemon

import (
	"strings"
	"sync"
)

// Named recover transitions. These are loop-level reason codes for doctor and
// audit, not a new TUI surface. Recoveries keep the run alive; terminals land
// on ExecutionFailed.reason_code so a requery is distinguishable from a fail.
const (
	recoverNativeToolRejected = "native_tool_rejected"
	recoverEmptyAfterTools    = "empty_after_tools"
	recoverPromptTooLong      = "prompt_too_long"

	recoverPhaseRecover  = "recover"
	recoverPhaseTerminal = "terminal"

	maxRecoverJournal = 8
)

func namedRecoverReasons() []string {
	return []string{recoverNativeToolRejected, recoverEmptyAfterTools, recoverPromptTooLong}
}

func isNamedRecoverReason(code string) bool {
	switch code {
	case recoverNativeToolRejected, recoverEmptyAfterTools, recoverPromptTooLong:
		return true
	default:
		return false
	}
}

// namedRecoverFromProvider maps a provider/classifier code onto the recover
// vocabulary when the two names differ.
func namedRecoverFromProvider(code string) string {
	switch code {
	case recoverNativeToolRejected, "provider_native_tools_rejected":
		return recoverNativeToolRejected
	case recoverPromptTooLong:
		return recoverPromptTooLong
	case recoverEmptyAfterTools:
		return recoverEmptyAfterTools
	default:
		return ""
	}
}

func looksLikePromptTooLongMessage(msg string) bool {
	lower := strings.ToLower(msg)
	for _, marker := range []string{
		"prompt_too_long",
		"prompt is too long",
		"prompt too long",
		"context_length_exceeded",
		"context length exceeded",
		"maximum context length",
		"max context length",
		"requested tokens exceed",
		"input is too long",
		"too many tokens",
		"context window exceeded",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func transcriptHasToolObservation(tr *Transcript) bool {
	if tr == nil {
		return false
	}
	for _, turn := range tr.Turns {
		switch turn.Tool {
		case "", "system", "done":
			continue
		default:
			return true
		}
	}
	return false
}

// recoverTransition is one named recover or terminal, newest last.
type recoverTransition struct {
	ReasonCode string `json:"reason_code"`
	Phase      string `json:"phase"`
	RunID      string `json:"run_id,omitempty"`
	Turn       int    `json:"turn,omitempty"`
}

type recoverJournal struct {
	mu     sync.Mutex
	recent []recoverTransition
}

func (j *recoverJournal) note(next recoverTransition) {
	if j == nil || !isNamedRecoverReason(next.ReasonCode) {
		return
	}
	if next.Phase != recoverPhaseRecover && next.Phase != recoverPhaseTerminal {
		next.Phase = recoverPhaseRecover
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.recent = append(j.recent, next)
	if extra := len(j.recent) - maxRecoverJournal; extra > 0 {
		j.recent = append([]recoverTransition(nil), j.recent[extra:]...)
	}
}

func (j *recoverJournal) snapshot() []recoverTransition {
	if j == nil {
		return []recoverTransition{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]recoverTransition, len(j.recent))
	copy(out, j.recent)
	return out
}

func (d *Daemon) noteRecover(reasonCode, phase, runID string, turn int) {
	if d == nil {
		return
	}
	d.recovers.note(recoverTransition{ReasonCode: reasonCode, Phase: phase, RunID: runID, Turn: turn})
}

func (d *Daemon) recoverDoctor() map[string]any {
	recent := []recoverTransition{}
	if d != nil {
		recent = d.recovers.snapshot()
	}
	return map[string]any{
		"ok":     true,
		"codes":  namedRecoverReasons(),
		"recent": recent,
	}
}
