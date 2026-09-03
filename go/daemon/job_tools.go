package daemon

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	defaultJobListLimit = 20
	maxJobListLimit     = 50
	maxJobWaitIDs       = 32
	maxJobWait          = 30 * time.Second
	maxJobSummaryBytes  = 2048
	maxJobArtifactRefs  = 16
	maxJobIDBytes       = 128
	maxJobCursorBytes   = 512
)

var validJobStatuses = []string{
	"queued", "running", "paused", "waiting_input", "waiting_approval",
	"interrupted", "completed", "degraded", "failed", "cancelled",
}

type jobUsageProjection struct {
	Tokens   int  `json:"tokens"`
	Observed bool `json:"observed"`
	Budget   int  `json:"budget,omitempty"`
}

type jobArtifactRef struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

type jobProjection struct {
	JobID            string             `json:"job_id"`
	Status           string             `json:"status"`
	CreatedAt        time.Time          `json:"created_at"`
	UpdatedAt        time.Time          `json:"updated_at"`
	Summary          string             `json:"summary,omitempty"`
	SummaryTruncated bool               `json:"summary_truncated,omitempty"`
	Usage            jobUsageProjection `json:"usage"`
	ArtifactRefs     []jobArtifactRef   `json:"artifact_refs"`
	ArtifactsOmitted int                `json:"artifacts_omitted,omitempty"`
}

type jobListCursor struct {
	CreatedAt int64  `json:"created_at_unix_nano"`
	JobID     string `json:"job_id"`
}

func builtinJobListHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if err := ctx.Err(); err != nil {
		return toolCancelled("job list cancelled", "operator_cancelled")
	}
	limit := act.Limit
	if limit == 0 {
		limit = defaultJobListLimit
	}
	if limit < 1 || limit > maxJobListLimit {
		return toolFailed(fmt.Sprintf("limit must be between 1 and %d", maxJobListLimit), "invalid_job_request")
	}
	statuses, err := normalizeJobStatuses(act.Statuses)
	if err != nil {
		return toolFailed(err.Error(), "invalid_job_request")
	}
	cursor, err := decodeJobListCursor(act.Cursor)
	if err != nil {
		return toolFailed("invalid job list cursor", "invalid_job_request")
	}

	jobs := d.ownedBackgroundJobs(sess, task)
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].CreatedAt.Before(jobs[j].CreatedAt)
		}
		return jobs[i].RunID < jobs[j].RunID
	})
	filtered := make([]*scheduler.ExecutionRun, 0, len(jobs))
	for _, job := range jobs {
		if len(statuses) > 0 {
			if _, ok := statuses[job.Status]; !ok {
				continue
			}
		}
		if cursor != nil && compareJobCursor(job, *cursor) <= 0 {
			continue
		}
		filtered = append(filtered, job)
	}

	pageSize := min(limit, len(filtered))
	page := make([]jobProjection, 0, pageSize)
	for _, job := range filtered[:pageSize] {
		page = append(page, projectJob(job))
	}
	nextCursor := ""
	if pageSize < len(filtered) && pageSize > 0 {
		nextCursor = encodeJobListCursor(filtered[pageSize-1])
	}
	return marshalJobToolResult(struct {
		Jobs       []jobProjection `json:"jobs"`
		NextCursor string          `json:"next_cursor,omitempty"`
	}{Jobs: page, NextCursor: nextCursor})
}

func builtinJobWaitHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	jobIDs, err := normalizeJobIDs(act.JobIDs)
	if err != nil {
		return toolFailed(err.Error(), "invalid_job_request")
	}
	mode := strings.ToLower(strings.TrimSpace(act.WaitMode))
	if mode != "any" && mode != "all" {
		return toolFailed("mode must be any or all", "invalid_job_request")
	}
	timeout := maxJobWait
	if act.TimeoutMS != 0 {
		if act.TimeoutMS < 1 || time.Duration(act.TimeoutMS)*time.Millisecond > maxJobWait {
			return toolFailed("timeout_ms must be between 1 and 30000", "invalid_job_request")
		}
		timeout = time.Duration(act.TimeoutMS) * time.Millisecond
	}

	updates, unsubscribe := d.sched.SubscribeRunUpdates(jobIDs...)
	defer unsubscribe()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		jobs, ok := d.resolveOwnedBackgroundJobs(sess, task, jobIDs)
		if !ok {
			return jobNotFoundOutcome()
		}
		ready := readyJobIDs(jobs)
		conditionMet := len(ready) > 0
		if mode == "all" {
			conditionMet = len(ready) == len(jobs)
		}
		if conditionMet {
			return marshalJobWaitResult(jobs, ready, mode, true, false)
		}
		select {
		case <-ctx.Done():
			return toolCancelled("job wait cancelled", "operator_cancelled")
		case <-timer.C:
			jobs, ok = d.resolveOwnedBackgroundJobs(sess, task, jobIDs)
			if !ok {
				return jobNotFoundOutcome()
			}
			return marshalJobWaitResult(jobs, readyJobIDs(jobs), mode, false, true)
		case _, ok := <-updates:
			if !ok {
				return toolCancelled("job wait cancelled", "operator_cancelled")
			}
		}
	}
}

func builtinJobCancelHandler(ctx context.Context, d *Daemon, sess *sessionstore.Session, task *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	if err := ctx.Err(); err != nil {
		return toolCancelled("job cancellation cancelled", "operator_cancelled")
	}
	jobID := strings.TrimSpace(act.JobID)
	if jobID == "" || len(jobID) > maxJobIDBytes {
		return toolFailed("job_id must contain between 1 and 128 bytes", "invalid_job_request")
	}
	job, ok := d.ownedBackgroundJob(sess, task, jobID)
	if !ok {
		return jobNotFoundOutcome()
	}
	if job.Status == "cancelled" {
		return marshalJobToolResult(struct {
			Job        jobProjection `json:"job"`
			Cancelled  bool          `json:"cancelled"`
			Idempotent bool          `json:"idempotent"`
		}{Job: projectJob(job), Cancelled: true, Idempotent: true})
	}
	if terminalExecutionStatus(job.Status) {
		return toolFailed("job is already terminal and cannot be cancelled", "job_terminal_conflict")
	}
	d.checkpointMu.Lock()
	_, cancelErr := d.cancelExecution(job.RunID, "model")
	d.checkpointMu.Unlock()
	if cancelErr != nil {
		current, stillOwned := d.ownedBackgroundJob(sess, task, jobID)
		if stillOwned && terminalExecutionStatus(current.Status) && current.Status != "cancelled" {
			return toolFailed("job is already terminal and cannot be cancelled", "job_terminal_conflict")
		}
		return toolFailed("job cancellation did not complete", "job_cancel_error")
	}
	cancelled, ok := d.ownedBackgroundJob(sess, task, jobID)
	if !ok {
		return jobNotFoundOutcome()
	}
	return marshalJobToolResult(struct {
		Job        jobProjection `json:"job"`
		Cancelled  bool          `json:"cancelled"`
		Idempotent bool          `json:"idempotent"`
	}{Job: projectJob(cancelled), Cancelled: true, Idempotent: false})
}

func (d *Daemon) ownedBackgroundJobs(ownerSession *sessionstore.Session, ownerRun *scheduler.ExecutionRun) []*scheduler.ExecutionRun {
	all := d.sched.List()
	jobs := make([]*scheduler.ExecutionRun, 0)
	for _, candidate := range all {
		if candidate.Mode != "background" {
			continue
		}
		if owned, ok := d.ownedBackgroundJob(ownerSession, ownerRun, candidate.RunID); ok {
			jobs = append(jobs, owned)
		}
	}
	return jobs
}

func (d *Daemon) resolveOwnedBackgroundJobs(ownerSession *sessionstore.Session, ownerRun *scheduler.ExecutionRun, jobIDs []string) ([]*scheduler.ExecutionRun, bool) {
	jobs := make([]*scheduler.ExecutionRun, 0, len(jobIDs))
	for _, jobID := range jobIDs {
		job, ok := d.ownedBackgroundJob(ownerSession, ownerRun, jobID)
		if !ok {
			return nil, false
		}
		jobs = append(jobs, job)
	}
	return jobs, true
}

func (d *Daemon) ownedBackgroundJob(ownerSession *sessionstore.Session, ownerRun *scheduler.ExecutionRun, jobID string) (*scheduler.ExecutionRun, bool) {
	if d == nil || ownerSession == nil || ownerRun == nil || ownerRun.SessionID != ownerSession.SessionID ||
		ownerRun.WorkspaceID != ownerSession.WorkspaceID {
		return nil, false
	}
	candidate, ok := d.sched.Get(strings.TrimSpace(jobID))
	if !ok || candidate.Mode != "background" || candidate.RootRunID == "" || candidate.RootRunID != ownerRun.RootRunID {
		return nil, false
	}
	if !d.trustedJobRunChain(candidate, ownerRun, ownerSession) {
		return nil, false
	}
	return candidate, true
}

func (d *Daemon) trustedJobRunChain(candidate, ownerRun *scheduler.ExecutionRun, ownerSession *sessionstore.Session) bool {
	current := candidate
	for depth := 0; depth <= maxSubagentDepth; depth++ {
		if current.ParentRunID == "" {
			return false
		}
		parent, ok := d.sched.Get(current.ParentRunID)
		if !ok || parent.RootRunID != ownerRun.RootRunID {
			return false
		}
		childSession, childOK := d.store.Get(current.SessionID)
		parentSession, parentOK := d.store.Get(parent.SessionID)
		if !childOK || !parentOK || childSession.WorkspaceID != current.WorkspaceID || parentSession.WorkspaceID != parent.WorkspaceID ||
			childSession.ParentID != parentSession.SessionID || !sessionstore.SameTenant(childSession.TenantID, ownerSession.TenantID) ||
			!sessionstore.SameTenant(parentSession.TenantID, ownerSession.TenantID) {
			return false
		}
		if parent.RunID == ownerRun.RunID {
			return parentSession.SessionID == ownerSession.SessionID
		}
		current = parent
	}
	return false
}

func projectJob(job *scheduler.ExecutionRun) jobProjection {
	summary, truncated := boundedJobSummary(job.Summary)
	artifactCount := min(len(job.AppliedPatches), maxJobArtifactRefs)
	artifacts := make([]jobArtifactRef, 0, artifactCount)
	for _, patchID := range job.AppliedPatches[:artifactCount] {
		artifacts = append(artifacts, jobArtifactRef{Kind: "patch", Ref: patchID})
	}
	return jobProjection{
		JobID: job.RunID, Status: job.Status,
		CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
		Summary: summary, SummaryTruncated: truncated,
		Usage:        jobUsageProjection{Tokens: job.TokensUsed, Observed: job.TokenUsageObserved, Budget: job.TokenBudget},
		ArtifactRefs: artifacts, ArtifactsOmitted: len(job.AppliedPatches) - artifactCount,
	}
}

func boundedJobSummary(summary string) (string, bool) {
	if len([]byte(summary)) <= maxJobSummaryBytes {
		return summary, false
	}
	const marker = "\n[summary truncated]"
	return truncateUTF8Bytes(summary, maxJobSummaryBytes-len(marker)) + marker, true
}

func normalizeJobIDs(values []string) ([]string, error) {
	if len(values) == 0 || len(values) > maxJobWaitIDs {
		return nil, fmt.Errorf("job_ids must contain between 1 and %d entries", maxJobWaitIDs)
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maxJobIDBytes {
			return nil, fmt.Errorf("job_ids must contain 1-128 byte values")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out, nil
}

func normalizeJobStatuses(values []string) (map[string]struct{}, error) {
	valid := make(map[string]struct{}, len(validJobStatuses))
	for _, status := range validJobStatuses {
		valid[status] = struct{}{}
	}
	if len(values) > len(valid) {
		return nil, fmt.Errorf("too many job status filters")
	}
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if _, ok := valid[value]; !ok {
			return nil, fmt.Errorf("invalid job status %q", value)
		}
		out[value] = struct{}{}
	}
	return out, nil
}

func readyJobIDs(jobs []*scheduler.ExecutionRun) []string {
	ready := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if terminalExecutionStatus(job.Status) || job.Status == "interrupted" {
			ready = append(ready, job.RunID)
		}
	}
	return ready
}

func marshalJobWaitResult(jobs []*scheduler.ExecutionRun, ready []string, mode string, conditionMet, timedOut bool) toolExecutionOutcome {
	projections := make([]jobProjection, 0, len(jobs))
	for _, job := range jobs {
		projections = append(projections, projectJob(job))
	}
	return marshalJobToolResult(struct {
		Jobs         []jobProjection `json:"jobs"`
		ReadyJobIDs  []string        `json:"ready_job_ids"`
		Mode         string          `json:"mode"`
		ConditionMet bool            `json:"condition_met"`
		TimedOut     bool            `json:"timed_out"`
	}{Jobs: projections, ReadyJobIDs: ready, Mode: mode, ConditionMet: conditionMet, TimedOut: timedOut})
}

func marshalJobToolResult(value any) toolExecutionOutcome {
	raw, err := json.Marshal(value)
	if err != nil {
		return toolFailed("job result could not be encoded", "internal_error")
	}
	return toolCompleted(string(raw))
}

func jobNotFoundOutcome() toolExecutionOutcome {
	return toolFailed("job not found", "job_not_found")
}

func encodeJobListCursor(job *scheduler.ExecutionRun) string {
	raw, _ := json.Marshal(jobListCursor{CreatedAt: job.CreatedAt.UnixNano(), JobID: job.RunID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeJobListCursor(value string) (*jobListCursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if len(value) > maxJobCursorBytes {
		return nil, fmt.Errorf("invalid cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	var cursor jobListCursor
	if err := json.Unmarshal(raw, &cursor); err != nil || cursor.CreatedAt == 0 || cursor.JobID == "" {
		return nil, fmt.Errorf("invalid cursor")
	}
	return &cursor, nil
}

func compareJobCursor(job *scheduler.ExecutionRun, cursor jobListCursor) int {
	createdAt := job.CreatedAt.UnixNano()
	if createdAt < cursor.CreatedAt {
		return -1
	}
	if createdAt > cursor.CreatedAt {
		return 1
	}
	return strings.Compare(job.RunID, cursor.JobID)
}
