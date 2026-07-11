// Package runtimecontract defines transport-neutral runtime operation contracts.
package runtimecontract

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ToolCallStatus string

const (
	ToolCallPending          ToolCallStatus = "pending"
	ToolCallAwaitingApproval ToolCallStatus = "awaiting_approval"
	ToolCallRunning          ToolCallStatus = "running"
	ToolCallCompleted        ToolCallStatus = "completed"
	ToolCallFailed           ToolCallStatus = "failed"
	ToolCallDenied           ToolCallStatus = "denied"
	ToolCallCancelled        ToolCallStatus = "cancelled"
	ToolCallTimedOut         ToolCallStatus = "timed_out"
)

type ToolCallEnvelope struct {
	CallID      string          `json:"call_id"`
	SessionID   string          `json:"session_id"`
	TaskID      string          `json:"task_id,omitempty"`
	Tool        string          `json:"tool"`
	Status      ToolCallStatus  `json:"status"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	ArtifactIDs []string        `json:"artifact_ids,omitempty"`
	Error       *ErrorEnvelope  `json:"error,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Deadline    *time.Time      `json:"deadline,omitempty"`
}

func (e ToolCallEnvelope) Validate() error {
	if strings.TrimSpace(e.CallID) == "" || strings.TrimSpace(e.SessionID) == "" || strings.TrimSpace(e.Tool) == "" {
		return errors.New("runtimecontract: call_id, session_id, and tool are required")
	}
	if !isStatus(e.Status) {
		return fmt.Errorf("runtimecontract: invalid tool call status %q", e.Status)
	}
	if !e.CreatedAt.IsZero() && !e.UpdatedAt.IsZero() && e.UpdatedAt.Before(e.CreatedAt) {
		return errors.New("runtimecontract: updated_at precedes created_at")
	}
	if e.Status == ToolCallFailed && e.Error == nil {
		return errors.New("runtimecontract: failed tool call requires an error")
	}
	if e.Error != nil {
		if err := e.Error.Validate(); err != nil {
			return err
		}
		if e.Status != ToolCallFailed && e.Status != ToolCallDenied && e.Status != ToolCallTimedOut && e.Status != ToolCallCancelled {
			return errors.New("runtimecontract: error is only valid on failed, timed_out, or cancelled calls")
		}
	}
	return nil
}

func ValidateTransition(from, to ToolCallStatus) error {
	if !isStatus(from) || !isStatus(to) {
		return errors.New("runtimecontract: transition contains invalid status")
	}
	allowed := map[ToolCallStatus]map[ToolCallStatus]bool{
		ToolCallPending:          {ToolCallAwaitingApproval: true, ToolCallRunning: true, ToolCallDenied: true, ToolCallCancelled: true, ToolCallTimedOut: true, ToolCallFailed: true},
		ToolCallAwaitingApproval: {ToolCallRunning: true, ToolCallDenied: true, ToolCallCancelled: true, ToolCallTimedOut: true, ToolCallFailed: true},
		ToolCallRunning:          {ToolCallCompleted: true, ToolCallFailed: true, ToolCallDenied: true, ToolCallCancelled: true, ToolCallTimedOut: true},
	}
	if !allowed[from][to] {
		return fmt.Errorf("runtimecontract: invalid tool call transition %s -> %s", from, to)
	}
	return nil
}

func isStatus(s ToolCallStatus) bool {
	switch s {
	case ToolCallPending, ToolCallAwaitingApproval, ToolCallRunning, ToolCallCompleted, ToolCallFailed, ToolCallDenied, ToolCallCancelled, ToolCallTimedOut:
		return true
	}
	return false
}

type ErrorCategory string

const (
	ErrorInvalidInput   ErrorCategory = "invalid_input"
	ErrorPermission     ErrorCategory = "permission"
	ErrorAuthentication ErrorCategory = "authentication"
	ErrorRateLimit      ErrorCategory = "rate_limit"
	ErrorUnavailable    ErrorCategory = "unavailable"
	ErrorTimeout        ErrorCategory = "timeout"
	ErrorConflict       ErrorCategory = "conflict"
	ErrorInternal       ErrorCategory = "internal"
)

type RetryMetadata struct {
	Retryable    bool       `json:"retryable"`
	RetryAt      *time.Time `json:"retry_at,omitempty"`
	RetryAfterMs int64      `json:"retry_after_ms,omitempty"`
	Attempt      int        `json:"attempt,omitempty"`
	MaxAttempts  int        `json:"max_attempts,omitempty"`
}

type ErrorEnvelope struct {
	Code          string         `json:"code"`
	Category      ErrorCategory  `json:"category"`
	Message       string         `json:"message"`
	UserAction    string         `json:"user_action,omitempty"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Retry         *RetryMetadata `json:"retry,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
}

func (e ErrorEnvelope) Validate() error {
	if strings.TrimSpace(e.Code) == "" || strings.TrimSpace(e.Message) == "" {
		return errors.New("runtimecontract: error code and message are required")
	}
	if !isCategory(e.Category) {
		return fmt.Errorf("runtimecontract: invalid error category %q", e.Category)
	}
	if e.Retry != nil {
		if err := e.Retry.Validate(); err != nil {
			return err
		}
		if e.Retry.Retryable && e.Category != ErrorRateLimit && e.Category != ErrorUnavailable && e.Category != ErrorTimeout && e.Category != ErrorConflict && e.Category != ErrorInternal {
			return fmt.Errorf("runtimecontract: category %s is not retryable", e.Category)
		}
	}
	return nil
}

func (r RetryMetadata) Validate() error {
	if r.RetryAfterMs < 0 || r.Attempt < 0 || r.MaxAttempts < 0 {
		return errors.New("runtimecontract: retry values must be non-negative")
	}
	if !r.Retryable && (r.RetryAt != nil || r.RetryAfterMs != 0) {
		return errors.New("runtimecontract: non-retryable error cannot schedule retry")
	}
	if r.MaxAttempts > 0 && r.Attempt > r.MaxAttempts {
		return errors.New("runtimecontract: retry attempt exceeds maximum")
	}
	return nil
}

func RetryAfter(delay time.Duration, attempt, maxAttempts int, now time.Time) *RetryMetadata {
	if delay < 0 {
		delay = 0
	}
	at := now.UTC().Add(delay)
	return &RetryMetadata{Retryable: true, RetryAt: &at, RetryAfterMs: delay.Milliseconds(), Attempt: attempt, MaxAttempts: maxAttempts}
}

func NoRetry() *RetryMetadata { return &RetryMetadata{Retryable: false} }

func isCategory(c ErrorCategory) bool {
	switch c {
	case ErrorInvalidInput, ErrorPermission, ErrorAuthentication, ErrorRateLimit, ErrorUnavailable, ErrorTimeout, ErrorConflict, ErrorInternal:
		return true
	}
	return false
}
