package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

func createOwnedJobFixture(t *testing.T, d *Daemon, ownerSession *sessionstore.Session, ownerRun *scheduler.ExecutionRun, status, summary string) (*sessionstore.Session, *scheduler.ExecutionRun) {
	t.Helper()
	child, err := d.createSubSession(ownerSession.WorkspaceRoot, "read-only", ownerSession.ApprovalMode, ownerSession.SessionID, ownerSession.Depth+1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(child.SessionID, child.WorkspaceRoot, child.PermissionProfile, child.ApprovalMode, nil); err != nil {
		t.Fatal(err)
	}
	job, err := d.sched.SubmitChildWithGoalModelAgent(ownerRun.RunID, child.SessionID, child.WorkspaceID, "PRIVATE PROMPT MUST NOT LEAK", "private/model", "private-agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.sched.SetMode(job.RunID, "background")
	if status != "" && status != "queued" {
		if terminalExecutionStatus(status) {
			if _, err := d.sched.SetTerminalResultFenced(job.RunID, 0, status, summary, nil); err != nil {
				t.Fatal(err)
			}
		} else {
			d.sched.SetStatus(job.RunID, status)
			if summary != "" {
				d.sched.SetResult(job.RunID, summary, nil)
			}
		}
	}
	job, _ = d.sched.Get(job.RunID)
	return child, job
}

func newJobOwner(t *testing.T, d *Daemon, workspace string) (*sessionstore.Session, *scheduler.ExecutionRun) {
	t.Helper()
	sess, err := d.store.CreateSessionMode(workspace, "full-workspace", "on_request")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.kern.InitSessionFull(sess.SessionID, workspace, sess.PermissionProfile, sess.ApprovalMode, nil); err != nil {
		t.Fatal(err)
	}
	run := d.sched.Submit(sess.SessionID, sess.WorkspaceID, "owner")
	return sess, run
}

func TestJobListPaginationProjectionAndSummaryBounds(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	ownerSession, ownerRun := newJobOwner(t, d, ws)
	_, first := createOwnedJobFixture(t, d, ownerSession, ownerRun, "completed", strings.Repeat("x", maxJobSummaryBytes+500))
	_, second := createOwnedJobFixture(t, d, ownerSession, ownerRun, "running", "")
	_, third := createOwnedJobFixture(t, d, ownerSession, ownerRun, "failed", "bounded failure")
	d.sched.SetInputMediaRefs(first.RunID, []scheduler.InputMediaRef{{ArtifactID: "PRIVATE_INPUT_ARTIFACT"}})
	patches := make([]string, maxJobArtifactRefs+4)
	for i := range patches {
		patches[i] = fmt.Sprintf("patch-%02d", i)
	}
	d.sched.SetAppliedPatches(first.RunID, patches)

	firstPage := builtinJobListHandler(context.Background(), d, ownerSession, ownerRun, &action{Limit: 2})
	if firstPage.status != "completed" {
		t.Fatalf("first page = %+v", firstPage)
	}
	if strings.Contains(firstPage.display, "PRIVATE PROMPT") || strings.Contains(firstPage.display, "private/model") ||
		strings.Contains(firstPage.display, "private-agent") || strings.Contains(firstPage.display, "PRIVATE_INPUT_ARTIFACT") {
		t.Fatalf("job projection leaked private run fields: %s", firstPage.display)
	}
	var page struct {
		Jobs       []jobProjection `json:"jobs"`
		NextCursor string          `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(firstPage.display), &page); err != nil || len(page.Jobs) != 2 || page.NextCursor == "" {
		t.Fatalf("first page decode = %+v err=%v", page, err)
	}
	if page.Jobs[0].JobID != first.RunID || page.Jobs[1].JobID != second.RunID {
		t.Fatalf("deterministic page order = %+v", page.Jobs)
	}
	if len([]byte(page.Jobs[0].Summary)) > maxJobSummaryBytes || !page.Jobs[0].SummaryTruncated ||
		len(page.Jobs[0].ArtifactRefs) != maxJobArtifactRefs || page.Jobs[0].ArtifactsOmitted != 4 {
		t.Fatalf("bounded projection = %+v", page.Jobs[0])
	}

	secondPage := builtinJobListHandler(context.Background(), d, ownerSession, ownerRun, &action{Limit: 2, Cursor: page.NextCursor})
	var tail struct {
		Jobs []jobProjection `json:"jobs"`
	}
	if err := json.Unmarshal([]byte(secondPage.display), &tail); err != nil || len(tail.Jobs) != 1 || tail.Jobs[0].JobID != third.RunID {
		t.Fatalf("second page = %+v err=%v", tail, err)
	}

	filtered := builtinJobListHandler(context.Background(), d, ownerSession, ownerRun, &action{Statuses: []string{"failed"}})
	if err := json.Unmarshal([]byte(filtered.display), &tail); err != nil || len(tail.Jobs) != 1 || tail.Jobs[0].Status != "failed" {
		t.Fatalf("filtered list = %+v err=%v", tail, err)
	}
	if outcome := builtinJobListHandler(context.Background(), d, ownerSession, ownerRun, &action{Cursor: "not-a-cursor"}); outcome.errorCategory != "invalid_job_request" {
		t.Fatalf("invalid cursor outcome = %+v", outcome)
	}
}

func TestJobAccessFailsClosedWithoutExistenceLeak(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	ownerSession, ownerRun := newJobOwner(t, d, ws)
	childSession, childJob := createOwnedJobFixture(t, d, ownerSession, ownerRun, "running", "")
	_, siblingJob := createOwnedJobFixture(t, d, ownerSession, ownerRun, "running", "")

	unknown := builtinJobWaitHandler(context.Background(), d, ownerSession, ownerRun, &action{JobIDs: []string{"job-missing"}, WaitMode: "all", TimeoutMS: 1})
	sibling := builtinJobWaitHandler(context.Background(), d, childSession, childJob, &action{JobIDs: []string{siblingJob.RunID}, WaitMode: "all", TimeoutMS: 1})
	if unknown.status != "failed" || sibling.status != "failed" || unknown.display != sibling.display ||
		unknown.errorCategory != sibling.errorCategory || unknown.observationError == nil || sibling.observationError == nil ||
		*unknown.observationError != *sibling.observationError {
		t.Fatalf("unknown and unauthorized diverged: unknown=%+v sibling=%+v", unknown, sibling)
	}
	unknownCancel := builtinJobCancelHandler(context.Background(), d, ownerSession, ownerRun, &action{JobID: "job-missing"})
	siblingCancel := builtinJobCancelHandler(context.Background(), d, childSession, childJob, &action{JobID: siblingJob.RunID})
	if unknownCancel.status != "failed" || siblingCancel.status != "failed" ||
		unknownCancel.display != siblingCancel.display || unknownCancel.errorCategory != siblingCancel.errorCategory {
		t.Fatalf("unknown and unauthorized cancellation diverged: unknown=%+v sibling=%+v", unknownCancel, siblingCancel)
	}
	childList := builtinJobListHandler(context.Background(), d, childSession, childJob, &action{})
	if strings.Contains(childList.display, siblingJob.RunID) {
		t.Fatalf("child list leaked a sibling job: %s", childList.display)
	}

	grandchildSession, err := d.createSubSession(childSession.WorkspaceRoot, "read-only", childSession.ApprovalMode, childSession.SessionID, childSession.Depth+1)
	if err != nil {
		t.Fatal(err)
	}
	grandchildJob, err := d.sched.SubmitChildWithGoalModelAgent(childJob.RunID, grandchildSession.SessionID, grandchildSession.WorkspaceID, "grandchild", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.sched.SetMode(grandchildJob.RunID, "background")
	if _, ok := d.ownedBackgroundJob(ownerSession, ownerRun, grandchildJob.RunID); !ok {
		t.Fatal("trusted descendant job was rejected by the root owner")
	}

	tamperedSession, err := d.createSubSession(ownerSession.WorkspaceRoot, "read-only", ownerSession.ApprovalMode, ownerSession.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := d.sched.SubmitChildWithGoalModelAgent(ownerRun.RunID, tamperedSession.SessionID, "wrong-workspace", "private", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d.sched.SetMode(tampered.RunID, "background")
	if _, ok := d.ownedBackgroundJob(ownerSession, ownerRun, tampered.RunID); ok {
		t.Fatal("workspace-mismatched job passed ownership checks")
	}

	otherRoot := d.sched.Submit(ownerSession.SessionID, ownerSession.WorkspaceID, "other root in the same workspace")
	_, otherRootJob := createOwnedJobFixture(t, d, ownerSession, otherRoot, "running", "")
	if _, ok := d.ownedBackgroundJob(ownerSession, ownerRun, otherRootJob.RunID); ok {
		t.Fatal("same-workspace foreign root job passed ownership checks")
	}

	tenantSession, tenantJob := createOwnedJobFixture(t, d, ownerSession, ownerRun, "running", "")
	tenantSession.TenantID = "foreign-tenant"
	if _, ok := d.ownedBackgroundJob(ownerSession, ownerRun, tenantJob.RunID); ok {
		t.Fatal("tenant-mismatched job passed ownership checks")
	}

	otherSession, otherRun := newJobOwner(t, d, t.TempDir())
	_, otherJob := createOwnedJobFixture(t, d, otherSession, otherRun, "running", "")
	if _, ok := d.ownedBackgroundJob(ownerSession, ownerRun, otherJob.RunID); ok {
		t.Fatal("foreign root job passed ownership checks")
	}
}

func TestJobWaitAnyAllTimeoutAndParentCancellation(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	ownerSession, ownerRun := newJobOwner(t, d, ws)
	_, first := createOwnedJobFixture(t, d, ownerSession, ownerRun, "running", "")
	_, second := createOwnedJobFixture(t, d, ownerSession, ownerRun, "running", "")

	anyResult := make(chan toolExecutionOutcome, 1)
	go func() {
		anyResult <- builtinJobWaitHandler(context.Background(), d, ownerSession, ownerRun, &action{
			JobIDs: []string{first.RunID, second.RunID}, WaitMode: "any", TimeoutMS: 1000,
		})
	}()
	if _, err := d.sched.SetTerminalResultFenced(first.RunID, 0, "completed", "first done", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-anyResult:
		if outcome.status != "completed" || !strings.Contains(outcome.display, `"condition_met":true`) || !strings.Contains(outcome.display, first.RunID) {
			t.Fatalf("wait any = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("wait any did not receive scheduler event")
	}

	allResult := make(chan toolExecutionOutcome, 1)
	go func() {
		allResult <- builtinJobWaitHandler(context.Background(), d, ownerSession, ownerRun, &action{
			JobIDs: []string{first.RunID, second.RunID}, WaitMode: "all", TimeoutMS: 1000,
		})
	}()
	if _, err := d.sched.SetTerminalResultFenced(second.RunID, 0, "failed", "second failed", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-allResult:
		if outcome.status != "completed" || !strings.Contains(outcome.display, `"condition_met":true`) || !strings.Contains(outcome.display, second.RunID) {
			t.Fatalf("wait all = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("wait all did not receive scheduler event")
	}

	_, live := createOwnedJobFixture(t, d, ownerSession, ownerRun, "running", "")
	timedOut := builtinJobWaitHandler(context.Background(), d, ownerSession, ownerRun, &action{JobIDs: []string{live.RunID}, WaitMode: "all", TimeoutMS: 5})
	if timedOut.status != "completed" || !strings.Contains(timedOut.display, `"timed_out":true`) {
		t.Fatalf("wait timeout = %+v", timedOut)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelledResult := make(chan toolExecutionOutcome, 1)
	go func() {
		cancelledResult <- builtinJobWaitHandler(ctx, d, ownerSession, ownerRun, &action{JobIDs: []string{live.RunID}, WaitMode: "all", TimeoutMS: 1000})
	}()
	cancel()
	select {
	case outcome := <-cancelledResult:
		if outcome.status != "cancelled" {
			t.Fatalf("cancelled wait = %+v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("job wait ignored parent cancellation")
	}
}

func TestJobCancelIsOwnedIdempotentAndTerminalSafe(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	ownerSession, ownerRun := newJobOwner(t, d, ws)
	childSession, job := createOwnedJobFixture(t, d, ownerSession, ownerRun, "running", "")

	jobCtx, cancelJob := context.WithCancelCause(context.Background())
	d.taskContextMu.Lock()
	d.taskContexts[job.RunID] = jobCtx
	d.taskCancels[job.RunID] = cancelJob
	d.taskContextMu.Unlock()
	defer func() {
		d.taskContextMu.Lock()
		delete(d.taskContexts, job.RunID)
		delete(d.taskCancels, job.RunID)
		d.taskContextMu.Unlock()
	}()

	first := builtinJobCancelHandler(context.Background(), d, ownerSession, ownerRun, &action{JobID: job.RunID})
	if first.status != "completed" || !strings.Contains(first.display, `"cancelled":true`) || !strings.Contains(first.display, `"idempotent":false`) {
		t.Fatalf("first cancel = %+v", first)
	}
	select {
	case <-jobCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("job cancel did not invoke the owned task cancellation context")
	}
	current, _ := d.sched.Get(job.RunID)
	if current.Status != "cancelled" {
		t.Fatalf("cancelled job = %+v", current)
	}
	second := builtinJobCancelHandler(context.Background(), d, ownerSession, ownerRun, &action{JobID: job.RunID})
	if second.status != "completed" || !strings.Contains(second.display, `"idempotent":true`) {
		t.Fatalf("idempotent cancel = %+v", second)
	}

	actorFound := false
	for _, event := range readAuditEvents(t, d, childSession.SessionID) {
		if event["type"] == "ExecutionCancelled" && event["actor"] == "model" {
			actorFound = true
		}
	}
	if !actorFound {
		t.Fatal("job cancellation audit did not identify the model actor")
	}

	_, completed := createOwnedJobFixture(t, d, ownerSession, ownerRun, "completed", "done")
	conflict := builtinJobCancelHandler(context.Background(), d, ownerSession, ownerRun, &action{JobID: completed.RunID})
	if conflict.status != "failed" || conflict.errorCategory != "job_terminal_conflict" {
		t.Fatalf("terminal cancel = %+v", conflict)
	}
	after, _ := d.sched.Get(completed.RunID)
	if after.Status != "completed" || after.Summary != "done" {
		t.Fatalf("completed job was rewritten: %+v", after)
	}
}

func TestBackgroundJobRestartPreservesLineageAndNeverClaimsStaleRunning(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	d1 := newDaemonAt(t, stateDir)
	ownerSession, ownerRun := newJobOwner(t, d1, workspace)
	_, job := createOwnedJobFixture(t, d1, ownerSession, ownerRun, "running", "durable partial summary")
	d1.persistRun(ownerRun.RunID)
	d1.persistRun(job.RunID)
	d1.Close()

	d2 := newDaemonAt(t, stateDir)
	defer d2.Close()
	reloadedOwner, ok := d2.store.Get(ownerSession.SessionID)
	if !ok {
		t.Fatal("owner session was not restored")
	}
	reloadedRoot, ok := d2.sched.Get(ownerRun.RunID)
	if !ok {
		t.Fatal("owner run was not restored")
	}
	reloadedJob, ok := d2.sched.Get(job.RunID)
	if !ok {
		t.Fatal("background job was not restored")
	}
	if reloadedJob.Status == "running" {
		t.Fatalf("restart claimed stale job was still running: %+v", reloadedJob)
	}
	if reloadedJob.Status != "interrupted" || reloadedJob.ParentRunID != ownerRun.RunID ||
		reloadedJob.RootRunID != ownerRun.RunID || reloadedJob.Summary != "durable partial summary" {
		t.Fatalf("reconciled background job = %+v", reloadedJob)
	}

	listed := builtinJobListHandler(context.Background(), d2, reloadedOwner, reloadedRoot, &action{})
	if listed.status != "completed" || !strings.Contains(listed.display, job.RunID) ||
		!strings.Contains(listed.display, `"status":"interrupted"`) || !strings.Contains(listed.display, "durable partial summary") {
		t.Fatalf("restarted job list = %+v", listed)
	}
}

func TestJobToolsExecuteInDescriptorShadowAndLegacyModes(t *testing.T) {
	d, ws := newLoopDaemon(t)
	defer d.Close()
	ownerSession, ownerRun := newJobOwner(t, d, ws)
	_, job := createOwnedJobFixture(t, d, ownerSession, ownerRun, "completed", "done")
	originalMode := d.builtinToolsMode
	defer func() { d.builtinToolsMode = originalMode }()
	for _, mode := range []builtinToolRegistryMode{builtinToolRegistryDescriptor, builtinToolRegistryShadow, builtinToolRegistryLegacy} {
		d.builtinToolsMode = mode
		outcome := d.dispatchBuiltinActionOutcome(ownerSession, ownerRun, &action{Tool: "job.list"})
		if outcome.status != "completed" || !strings.Contains(outcome.display, job.RunID) {
			t.Fatalf("%s job.list = %+v", mode, outcome)
		}
	}
}
