// Package continuity defines the durable, client-independent session recovery
// contract. It is intentionally a leaf package so scheduler, daemon, SDK
// projections, and product surfaces share one vocabulary.
package continuity

import (
	"fmt"
	"time"
)

type Activity string

const (
	ActivityRunning         Activity = "running"
	ActivityWaitingInput    Activity = "waiting_input"
	ActivityWaitingApproval Activity = "waiting_approval"
	ActivityIdle            Activity = "idle"
)

type Outcome string

const (
	OutcomeNone        Outcome = "none"
	OutcomeCompleted   Outcome = "completed"
	OutcomePartial     Outcome = "partial"
	OutcomeFailed      Outcome = "failed"
	OutcomeCancelled   Outcome = "cancelled"
	OutcomeInterrupted Outcome = "interrupted"
)

type Progress string

const (
	ProgressEmpty             Progress = "empty"
	ProgressStarted           Progress = "started"
	ProgressInProgress        Progress = "in_progress"
	ProgressVerifying         Progress = "verifying"
	ProgressPartiallyComplete Progress = "partially_complete"
	ProgressComplete          Progress = "complete"
)

type RecoveryDisposition string

const (
	RecoveryNone             RecoveryDisposition = "none"
	RecoveryContinue         RecoveryDisposition = "continue"
	RecoveryRetry            RecoveryDisposition = "retry"
	RecoveryResumeCheckpoint RecoveryDisposition = "resume_checkpoint"
	RecoveryReviewRequired   RecoveryDisposition = "review_required"
	RecoveryBlocked          RecoveryDisposition = "blocked"
)

type InterruptionKind string

const (
	InterruptionOperatorCancelled InterruptionKind = "operator_cancelled"
	InterruptionProviderQuota     InterruptionKind = "provider_quota"
	InterruptionProviderAuth      InterruptionKind = "provider_auth"
	InterruptionProviderRateLimit InterruptionKind = "provider_rate_limit"
	InterruptionNetworkLost       InterruptionKind = "network_lost"
	InterruptionGracefulShutdown  InterruptionKind = "graceful_shutdown"
	InterruptionRuntimeLost       InterruptionKind = "runtime_lost"
	InterruptionUnknown           InterruptionKind = "unknown"
)

type Certainty string

const (
	CertaintyObserved Certainty = "observed"
	CertaintyInferred Certainty = "inferred"
)

type EffectClass string

const (
	EffectPure                   EffectClass = "pure"
	EffectWorkspaceTransactional EffectClass = "workspace_transactional"
	EffectIdempotentExternal     EffectClass = "idempotent_external"
	EffectNonIdempotent          EffectClass = "non_idempotent"
	EffectUnknown                EffectClass = "unknown"
)

type ExecutionLease struct {
	OwnerKind       string    `json:"owner_kind,omitempty"`
	OwnerID         string    `json:"owner_id,omitempty"`
	RuntimeEpoch    int64     `json:"runtime_epoch,omitempty"`
	LeaseGeneration int64     `json:"lease_generation,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
}

func (l ExecutionLease) Empty() bool {
	return l.OwnerKind == "" && l.OwnerID == "" && l.RuntimeEpoch == 0 && l.LeaseGeneration == 0 && l.ExpiresAt.IsZero()
}

func (l ExecutionLease) Validate() error {
	if l.Empty() {
		return nil
	}
	if l.OwnerKind == "" || l.OwnerID == "" {
		return fmt.Errorf("continuity: execution lease owner is incomplete")
	}
	if l.LeaseGeneration < 1 {
		return fmt.Errorf("continuity: execution lease generation must be positive")
	}
	if l.OwnerKind == "local" && l.RuntimeEpoch < 1 {
		return fmt.Errorf("continuity: local execution lease requires a runtime epoch")
	}
	return nil
}

type InterruptionRecord struct {
	Kind             InterruptionKind `json:"kind"`
	Actor            string           `json:"actor"`
	ObservedAt       time.Time        `json:"observed_at"`
	RuntimeEpoch     int64            `json:"runtime_epoch,omitempty"`
	TaskID           string           `json:"task_id"`
	CheckpointID     string           `json:"checkpoint_id,omitempty"`
	EvidenceEventID  string           `json:"evidence_event_id,omitempty"`
	Certainty        Certainty        `json:"certainty"`
	Retryable        bool             `json:"retryable"`
	UserAction       string           `json:"user_action,omitempty"`
	BillingUncertain bool             `json:"billing_uncertain,omitempty"`
}

func (r InterruptionRecord) Validate() error {
	if !validInterruption(r.Kind) {
		return fmt.Errorf("continuity: invalid interruption kind %q", r.Kind)
	}
	if r.Actor == "" || r.TaskID == "" || r.ObservedAt.IsZero() {
		return fmt.Errorf("continuity: interruption requires actor, task_id, and observed_at")
	}
	if r.Certainty != CertaintyObserved && r.Certainty != CertaintyInferred {
		return fmt.Errorf("continuity: invalid interruption certainty %q", r.Certainty)
	}
	return nil
}

type FileDigest struct {
	Path   string `json:"path"`
	Mode   uint32 `json:"mode"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type WorkspaceAnchor struct {
	ID                string       `json:"id"`
	WorkspaceRealpath string       `json:"workspace_realpath"`
	GitHead           string       `json:"git_head,omitempty"`
	GitIndexDigest    string       `json:"git_index_digest,omitempty"`
	DependencyFiles   []FileDigest `json:"dependency_files,omitempty"`
	MutationFiles     []FileDigest `json:"mutation_files,omitempty"`
	PatchLineage      []string     `json:"patch_lineage,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
}

func (a WorkspaceAnchor) Validate() error {
	if a.ID == "" || a.WorkspaceRealpath == "" || a.CreatedAt.IsZero() {
		return fmt.Errorf("continuity: workspace anchor requires id, realpath, and created_at")
	}
	for _, file := range append(append([]FileDigest(nil), a.DependencyFiles...), a.MutationFiles...) {
		if file.Path == "" || file.SHA256 == "" || file.Bytes < 0 {
			return fmt.Errorf("continuity: invalid workspace file digest")
		}
	}
	return nil
}

type RecoveryDecision struct {
	Disposition          RecoveryDisposition `json:"disposition"`
	Reason               string              `json:"reason,omitempty"`
	SourceCursor         string              `json:"source_cursor,omitempty"`
	ExpectedTaskRevision int64               `json:"expected_task_revision,omitempty"`
	CheckpointID         string              `json:"checkpoint_id,omitempty"`
	WorkspaceAnchorID    string              `json:"workspace_anchor_id,omitempty"`
	RecoveryGeneration   int64               `json:"recovery_generation,omitempty"`
	Proofs               map[string]bool     `json:"proofs,omitempty"`
}

func (d RecoveryDecision) Validate() error {
	if !validRecovery(d.Disposition) {
		return fmt.Errorf("continuity: invalid recovery disposition %q", d.Disposition)
	}
	if d.Disposition == RecoveryResumeCheckpoint && d.CheckpointID == "" {
		return fmt.Errorf("continuity: checkpoint recovery requires checkpoint_id")
	}
	return nil
}

type State struct {
	Activity             Activity            `json:"activity"`
	Outcome              Outcome             `json:"outcome"`
	Progress             Progress            `json:"progress"`
	Recovery             RecoveryDecision    `json:"recovery"`
	Interruption         *InterruptionRecord `json:"interruption,omitempty"`
	Execution            ExecutionLease      `json:"execution,omitempty"`
	WorkspaceAnchor      *WorkspaceAnchor    `json:"workspace_anchor,omitempty"`
	RecoveryGeneration   int64               `json:"recovery_generation,omitempty"`
	AutoRecoveryAttempts int                 `json:"auto_recovery_attempts,omitempty"`
}

func (s State) Validate() error {
	if !validActivity(s.Activity) || !validOutcome(s.Outcome) || !validProgress(s.Progress) {
		return fmt.Errorf("continuity: invalid state activity=%q outcome=%q progress=%q", s.Activity, s.Outcome, s.Progress)
	}
	if s.AutoRecoveryAttempts < 0 || s.RecoveryGeneration < 0 {
		return fmt.Errorf("continuity: recovery counters must be non-negative")
	}
	if err := s.Recovery.Validate(); err != nil {
		return err
	}
	if err := s.Execution.Validate(); err != nil {
		return err
	}
	if s.Interruption != nil {
		if err := s.Interruption.Validate(); err != nil {
			return err
		}
	}
	if s.WorkspaceAnchor != nil {
		if err := s.WorkspaceAnchor.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func EmptyState() State {
	return State{Activity: ActivityIdle, Outcome: OutcomeNone, Progress: ProgressEmpty, Recovery: RecoveryDecision{Disposition: RecoveryNone}}
}

func validActivity(v Activity) bool {
	switch v {
	case ActivityRunning, ActivityWaitingInput, ActivityWaitingApproval, ActivityIdle:
		return true
	}
	return false
}
func validOutcome(v Outcome) bool {
	switch v {
	case OutcomeNone, OutcomeCompleted, OutcomePartial, OutcomeFailed, OutcomeCancelled, OutcomeInterrupted:
		return true
	}
	return false
}
func validProgress(v Progress) bool {
	switch v {
	case ProgressEmpty, ProgressStarted, ProgressInProgress, ProgressVerifying, ProgressPartiallyComplete, ProgressComplete:
		return true
	}
	return false
}
func validRecovery(v RecoveryDisposition) bool {
	switch v {
	case RecoveryNone, RecoveryContinue, RecoveryRetry, RecoveryResumeCheckpoint, RecoveryReviewRequired, RecoveryBlocked:
		return true
	}
	return false
}
func validInterruption(v InterruptionKind) bool {
	switch v {
	case InterruptionOperatorCancelled, InterruptionProviderQuota, InterruptionProviderAuth, InterruptionProviderRateLimit, InterruptionNetworkLost, InterruptionGracefulShutdown, InterruptionRuntimeLost, InterruptionUnknown:
		return true
	}
	return false
}
