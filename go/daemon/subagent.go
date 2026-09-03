package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nebutra/carina/go/agentview"
	"github.com/Nebutra/carina/go/artifact"
	"github.com/Nebutra/carina/go/scheduler"
	sessionstore "github.com/Nebutra/carina/go/session-store"
)

const (
	maxSubagentDepth       = 4 // bound nested delegation cost and complexity
	subagentMaxTurns       = 10
	maxBackgroundSpawnJobs = 8
)

type preparedSubagent struct {
	parent     *sessionstore.Session
	parentTask *scheduler.ExecutionRun
	child      *sessionstore.Session
	childTask  *scheduler.ExecutionRun
	spec       *AgentSpec
	agentName  string
	cleanup    func()
}

type backgroundSpawnHandle struct {
	JobID     string    `json:"job_id"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type backgroundSpawnFailure struct {
	Agent   string `json:"agent"`
	Message string `json:"message"`
}

// executeSpawn dispatches the spawn tool: a single delegation
// ({agent, task}) or a parallel fan-out ({tasks: [...]}). Each subagent runs
// in an isolated, capability-attenuated session and only its final summary
// returns to the parent — the core sub-agent contract (isolated context,
// single-channel result).
func (d *Daemon) executeSpawn(parent *sessionstore.Session, parentTask *scheduler.ExecutionRun, act *action) string {
	return d.executeSpawnOutcome(parent, parentTask, act).display
}

func (d *Daemon) executeSpawnOutcome(parent *sessionstore.Session, parentTask *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	ctx := d.contextForTask(parentTask.RunID)
	if parent.Depth >= maxSubagentDepth {
		return toolDenied("DENIED: max subagent depth reached (no deeper nesting)", "depth_limit")
	}
	if err := d.ensureActiveToolStarted(parentTask.RunID); err != nil {
		return toolFailed("governance error: "+err.Error(), "audit_persistence_error")
	}
	if len(act.Tasks) > maxBackgroundSpawnJobs {
		return toolFailed(fmt.Sprintf("error: spawn supports at most %d jobs", maxBackgroundSpawnJobs), "invalid_input")
	}
	// Spawning is a gated, audited effect — the actual Capability::SubagentSpawn
	// request happens per-agent inside spawnSubagentContext (below), which
	// runs for both the single-agent and parallel fan-out cases, and is the
	// single choke point workflow.go's spawn-fanout also goes through. A
	// denial/refused-approval surfaces as an error string in that subagent's
	// own result, not a toolDenied here.
	if act.Background {
		return d.executeBackgroundSpawn(ctx, parent, parentTask, act)
	}

	if len(act.Tasks) > 0 {
		// Parallel fan-out (goroutine per subagent).
		results := make([]string, len(act.Tasks))
		var wg sync.WaitGroup
		for i, st := range act.Tasks {
			wg.Add(1)
			go func(i int, st SpawnTask) {
				defer wg.Done()
				results[i] = fmt.Sprintf("=== %s ===\n%s", st.Agent, d.spawnSubagentContext(ctx, parent, parentTask, st.Agent, st.Task))
			}(i, st)
		}
		wg.Wait()
		if ctx.Err() != nil {
			return toolCancelled("subagent batch cancelled", "operator_cancelled")
		}
		return classifyLegacyToolResult(strings.Join(results, "\n\n"))
	}
	if act.Agent == "" {
		return toolFailed("error: spawn needs an 'agent' (or a 'tasks' list)", "invalid_input")
	}
	result := d.spawnSubagentContext(ctx, parent, parentTask, act.Agent, act.Task)
	if ctx.Err() != nil {
		return toolCancelled(result, "operator_cancelled")
	}
	return classifyLegacyToolResult(result)
}

func (d *Daemon) executeBackgroundSpawn(ctx context.Context, parent *sessionstore.Session, parentTask *scheduler.ExecutionRun, act *action) toolExecutionOutcome {
	tasks := act.Tasks
	if len(tasks) == 0 {
		if act.Agent == "" {
			return toolFailed("error: background spawn needs an 'agent' (or a 'tasks' list)", "invalid_input")
		}
		tasks = []SpawnTask{{Agent: act.Agent, Task: act.Task}}
	}
	handles := make([]backgroundSpawnHandle, 0, len(tasks))
	failures := make([]backgroundSpawnFailure, 0)
	for _, spawnTask := range tasks {
		prepared, failure := d.prepareSubagent(ctx, parent, parentTask, spawnTask.Agent, spawnTask.Task, nil, true)
		if prepared == nil {
			failures = append(failures, backgroundSpawnFailure{Agent: spawnTask.Agent, Message: failure})
			continue
		}
		d.sched.SetMode(prepared.childTask.RunID, "background")
		d.persistRun(prepared.childTask.RunID)
		current, _ := d.sched.Get(prepared.childTask.RunID)
		handles = append(handles, backgroundSpawnHandle{JobID: current.RunID, Status: current.Status, CreatedAt: current.CreatedAt})
		d.launchPreparedSubagent(ctx, prepared)
	}
	result := struct {
		Jobs     []backgroundSpawnHandle  `json:"jobs"`
		Failures []backgroundSpawnFailure `json:"failures,omitempty"`
	}{Jobs: handles, Failures: failures}
	raw, err := json.Marshal(result)
	if err != nil {
		return toolFailed("background spawn failed to encode handles", "internal_error")
	}
	if len(handles) == 0 {
		return toolFailed(string(raw), "spawn_failed")
	}
	return toolCompleted(string(raw))
}

// spawnSubagent creates an isolated, capability-attenuated child session,
// runs a bounded ReAct loop under the agent's system prompt, and returns its
// final summary. The child's profile is clamped so it can never exceed the
// parent (child ⊆ parent) — enforced by the Rust kernel.
func (d *Daemon) spawnSubagent(parent *sessionstore.Session, parentTask *scheduler.ExecutionRun, agentName, taskDesc string) string {
	return d.spawnSubagentContext(context.Background(), parent, parentTask, agentName, taskDesc)
}

func (d *Daemon) spawnSubagentContext(ctx context.Context, parent *sessionstore.Session, parentTask *scheduler.ExecutionRun, agentName, taskDesc string) string {
	summary, _ := d.spawnSubagentContextID(ctx, parent, parentTask, agentName, taskDesc)
	return summary
}

// spawnSubagentContextID is spawnSubagentContext plus the child session ID,
// for callers that need to correlate a subagent's result back to what that
// child session actually read/wrote (see bestofn.go, which uses this to
// establish real write-provenance for a winning candidate instead of
// self-seeding it at submission time).
func (d *Daemon) spawnSubagentContextID(ctx context.Context, parent *sessionstore.Session, parentTask *scheduler.ExecutionRun, agentName, taskDesc string) (string, string) {
	return d.spawnSubagentContextIDBound(ctx, parent, parentTask, agentName, taskDesc, nil)
}

// spawnSubagentContextIDBound is spawnSubagentContextID plus an optional
// swarmChannelBinding (nil for every caller except runStreamingStep),
// registered on the child session for the exact duration of its synchronous
// run so a mid-run "swarm_publish"/"swarm_receive" tool call can find the
// right run-scoped broker (see swarm_channel.go) — same
// Store-before/deferred-Delete-after lifetime pattern already used below for
// restrictedTools/allowedTools/allowedSpawnAgents.
func (d *Daemon) spawnSubagentContextIDBound(ctx context.Context, parent *sessionstore.Session, parentTask *scheduler.ExecutionRun, agentName, taskDesc string, binding *swarmChannelBinding) (string, string) {
	prepared, failure := d.prepareSubagent(ctx, parent, parentTask, agentName, taskDesc, binding, false)
	if prepared == nil {
		return failure, ""
	}
	summary := d.runPreparedSubagent(ctx, prepared, false)
	return summary, prepared.child.SessionID
}

func (d *Daemon) prepareSubagent(ctx context.Context, parent *sessionstore.Session, parentTask *scheduler.ExecutionRun, agentName, taskDesc string, binding *swarmChannelBinding, requireDurableParent bool) (*preparedSubagent, string) {
	if ctx.Err() != nil {
		return nil, "subagent cancelled"
	}
	specs := loadAgentSpecs(parent.WorkspaceRoot)
	spec := specs[agentName]
	if spec == nil {
		return nil, fmt.Sprintf("unknown agent %q (available: %s)", agentName, strings.Join(specNames(specs), ", "))
	}
	if taskDesc == "" {
		return nil, "error: spawn needs a task for the subagent"
	}
	if !d.spawnAllowed(parent.SessionID, agentName) {
		return nil, fmt.Sprintf("DENIED: this session's agent spec does not permit spawning %q", agentName)
	}

	// Capability monotonic decrease: child ⊆ parent.
	childProfile := attenuate(parent.PermissionProfile, spec.Profile)

	// Delegation is itself a gated, audited effect — dedicated capability
	// (not folded into PluginLoad) so the resource carries structured
	// identity a future PolicyBundle can differentiate on.
	spawnResource := fmt.Sprintf("agent:%s:profile:%s", agentName, childProfile)
	dec, err := d.kern.Request(parent.SessionID, "SubagentSpawn", spawnResource, parentTask.RunID)
	if err != nil {
		return nil, "spawn governance error: " + err.Error()
	}
	if dec.Decision == "denied" {
		return nil, "DENIED: this session may not spawn subagents"
	}
	if dec.Decision == "requires_approval" {
		approved, ok := d.resolveApprovalOrEscalate(parent, parentTask, dec, "SubagentSpawn", spawnResource, "spawn "+agentName)
		if !ok {
			return nil, "requires approval (not granted): " + dec.Reason
		}
		dec = approved
	}

	childRoot, worktreeID, releaseWorktree, err := d.prepareSpawnWorkspace(parent.WorkspaceRoot, parent.SessionID, spawnUsesWorktree(childProfile))
	if err != nil {
		return nil, "spawn isolation failed: " + err.Error()
	}
	cleanupFns := []func(){releaseWorktree}
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			for i := len(cleanupFns) - 1; i >= 0; i-- {
				cleanupFns[i]()
			}
		})
	}

	child, err := d.createSubSession(childRoot, childProfile, parent.ApprovalMode, parent.SessionID, parent.Depth+1)
	if err != nil {
		cleanup()
		return nil, "spawn failed: " + err.Error()
	}
	cleanupChild := func() {
		_, _ = d.store.SetStatus(child.SessionID, "closed")
		_ = d.store.Delete(child.SessionID)
	}
	if err := d.kern.InitSessionFull(child.SessionID, child.WorkspaceRoot, childProfile, parent.ApprovalMode, d.org); err != nil {
		cleanupChild()
		cleanup()
		return nil, "spawn init failed: " + err.Error()
	}
	if d.isPlanMode(parent.SessionID) {
		if _, err := d.store.SetPlanMode(child.SessionID, true); err != nil {
			cleanupChild()
			cleanup()
			return nil, "spawn failed to inherit plan mode: " + err.Error()
		}
		d.setPlanMode(child.SessionID, true)
	}
	if len(spec.RestrictedTools) > 0 {
		d.restrictedTools.Store(child.SessionID, spec.RestrictedTools)
		cleanupFns = append(cleanupFns, func() { d.restrictedTools.Delete(child.SessionID) })
	}
	if len(spec.ToolNames) > 0 {
		d.allowedTools.Store(child.SessionID, toSet(spec.ToolNames))
		cleanupFns = append(cleanupFns, func() { d.allowedTools.Delete(child.SessionID) })
	}
	if len(spec.SpawnableAgents) > 0 {
		d.allowedSpawnAgents.Store(child.SessionID, toSet(spec.SpawnableAgents))
		cleanupFns = append(cleanupFns, func() { d.allowedSpawnAgents.Delete(child.SessionID) })
	}
	if binding != nil {
		d.swarmChannels.Store(child.SessionID, binding)
		cleanupFns = append(cleanupFns, func() { d.swarmChannels.Delete(child.SessionID) })
	}

	childModel := d.resolveSubagentModel(spec, parentTask)
	childTask, err := d.sched.SubmitChildWithGoalModelAgent(parentTask.RunID, child.SessionID, child.WorkspaceID, taskDesc, childModel, spec.Name, nil)
	if err != nil && !requireDurableParent {
		childTask = d.sched.SubmitWithGoalModelAgent(child.SessionID, child.WorkspaceID, taskDesc, childModel, spec.Name, nil)
		err = nil
	}
	if err != nil {
		cleanupChild()
		cleanup()
		return nil, "spawn failed to create child job: " + err.Error()
	}
	d.sched.SetLocale(childTask.RunID, parentTask.Locale)
	childTask, _ = d.sched.Get(childTask.RunID)
	d.registerSubagentParent(child.SessionID, parentTask.RunID)

	// Audit the delegation on the parent, linking to the child session and run.
	spawnAudit := map[string]any{
		"spawn_agent": agentName, "child_session": child.SessionID,
		"child_run":     childTask.RunID,
		"child_profile": childProfile, "child_model": childModel,
		"depth": child.Depth, "task": taskDesc,
		"isolation": spawnIsolationMode(worktreeID),
	}
	if worktreeID != "" {
		spawnAudit["worktree_id"] = worktreeID
		if d.agentView != nil {
			_ = d.agentView.Set(child.SessionID, agentview.Metadata{WorktreeID: worktreeID})
		}
	}
	if isExploreSubagent(spec) {
		spawnAudit["prompt_mode"] = "explore_lean"
	}
	d.record(parent.SessionID, "ToolApproved", parentTask.RunID, "go", spawnAudit, dec.DecisionID)
	return &preparedSubagent{
		parent: parent, parentTask: parentTask, child: child, childTask: childTask,
		spec: spec, agentName: agentName, cleanup: cleanup,
	}, ""
}

func (d *Daemon) runPreparedSubagent(ctx context.Context, prepared *preparedSubagent, guarded bool) string {
	var summary string
	d.withTaskParentContext(ctx, prepared.childTask.RunID, func(childCtx context.Context) {
		summary = d.runPreparedSubagentContext(childCtx, prepared, guarded)
	})
	return summary
}

// launchPreparedSubagent installs cancellation ownership before returning the
// handle. This closes the race where an immediate job.cancel could otherwise
// arrive before the background goroutine registered its task context.
func (d *Daemon) launchPreparedSubagent(parent context.Context, prepared *preparedSubagent) {
	ctx, cancel := context.WithCancelCause(parent)
	d.taskContextMu.Lock()
	d.taskContexts[prepared.childTask.RunID] = ctx
	d.taskCancels[prepared.childTask.RunID] = cancel
	d.taskContextMu.Unlock()
	go func() {
		defer cancel(nil)
		defer func() {
			d.taskContextMu.Lock()
			delete(d.taskContexts, prepared.childTask.RunID)
			delete(d.taskCancels, prepared.childTask.RunID)
			d.taskContextMu.Unlock()
		}()
		d.runPreparedSubagentContext(ctx, prepared, true)
	}()
}

func (d *Daemon) runPreparedSubagentContext(ctx context.Context, prepared *preparedSubagent, guarded bool) string {
	defer prepared.cleanup()
	var summary string
	run := func() {
		summary = d.runSubagentLoopContext(ctx, prepared.child, prepared.childTask, prepared.spec)
		d.finalizeSubagentRun(prepared.child, prepared.childTask, summary)
	}
	if guarded {
		d.guardRun(ctx, prepared.child, prepared.childTask, run)
	} else {
		run()
	}
	if summary == "" && ctx.Err() != nil {
		summary = "subagent cancelled"
	}

	d.record(prepared.parent.SessionID, "ModelResponded", prepared.parentTask.RunID, "go", map[string]any{
		"spawn_agent": prepared.agentName, "child_session": prepared.child.SessionID,
		"child_run":      prepared.childTask.RunID,
		"result_summary": truncate(summary, 300),
	}, "")
	return summary
}

func (d *Daemon) finalizeSubagentRun(child *sessionstore.Session, childTask *scheduler.ExecutionRun, summary string) {
	current, ok := d.sched.Get(childTask.RunID)
	if !ok {
		return
	}
	if current.Status == "cancelled" || current.Status == "completed" {
		d.persistRun(current.RunID)
		return
	}
	status := current.Status
	if status != "failed" && status != "degraded" {
		status = "failed"
	}
	if _, err := d.sched.SetTerminalResultFenced(current.RunID, current.Continuity.Execution.LeaseGeneration, status, summary, d.appliedPatchIDsForRun(child, current.RunID)); err != nil {
		return
	}
	d.persistRun(current.RunID)
}

// spawnAllowed reports whether sessionID's own AgentSpec.SpawnableAgents
// allow-list (if any was set when it was spawned) permits delegating to
// agentName. No entry in allowedSpawnAgents means unrestricted.
func (d *Daemon) spawnAllowed(sessionID, agentName string) bool {
	v, ok := d.allowedSpawnAgents.Load(sessionID)
	if !ok {
		return true
	}
	set, _ := v.(map[string]bool)
	return set[agentName]
}

// toolAllowed reports whether sessionID's own AgentSpec.ToolNames allow-list
// (if any was set when it was spawned) permits dispatching toolName. No
// entry in allowedTools means unrestricted.
func (d *Daemon) toolAllowed(sessionID, toolName string) bool {
	v, ok := d.allowedTools.Load(sessionID)
	if !ok {
		return true
	}
	set, _ := v.(map[string]bool)
	return set[toolName]
}

func toSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[n] = true
	}
	return set
}

// runSubagentLoop runs a bounded ReAct loop for a subagent, using its own
// system prompt and isolated session. It returns the subagent's final
// summary (the only thing that crosses back to the parent).
func (d *Daemon) runSubagentLoop(sess *sessionstore.Session, task *scheduler.ExecutionRun, spec *AgentSpec) string {
	return d.runSubagentLoopContext(context.Background(), sess, task, spec)
}

func (d *Daemon) runSubagentLoopContext(ctx context.Context, sess *sessionstore.Session, task *scheduler.ExecutionRun, spec *AgentSpec) string {
	defer func() { _ = d.cleanupTerminalExecutionControl(task.RunID) }()
	if !d.reasonerReady() {
		return "(no reasoner configured)"
	}
	if ctx.Err() != nil {
		_, _ = d.sched.Cancel(task.RunID)
		return "subagent cancelled"
	}
	d.sched.SetStatus(task.RunID, "running")
	d.persistRun(task.RunID)
	ctx = withExecutionKeepalive(ctx, d, sess.SessionID, task.RunID)
	maxTurns := spec.MaxTurns
	if maxTurns <= 0 || maxTurns > subagentMaxTurns {
		maxTurns = subagentMaxTurns
	}
	tr := newTranscript(task.UserPrompt)
	tr.bindArtifacts(d.artifacts, artifact.Scope{SessionID: sess.SessionID, TaskID: task.RunID})
	applyCompactionBudget(tr, d.providerCatalog, taskModel(task))
	guard := newLoopGuard()
	mistakes := newMistakeTracker()
	memorySnapshot := d.memory.snapshot(memoryScopeFromSession(sess))
	layers := d.composeSubagentPromptLayers(sess, task, spec, memorySnapshot)

	d.record(sess.SessionID, "ModelRequested", task.RunID, "model",
		map[string]any{"subagent": spec.Name, "model": taskModel(task), "prompt": task.UserPrompt}, "")

	for turn := 1; turn <= maxTurns; turn++ {
		if ctx.Err() != nil {
			_, _ = d.sched.Cancel(task.RunID)
			return "subagent cancelled"
		}
		if receipt := tr.compact(func(head string) (string, error) {
			return thinkWithRetry(ctx, d.summarizeReasoner(), "Summarize concisely:\n"+head)
		}); receipt != nil {
			d.recordCompactRebuild(sess, task, tr, receipt, nil)
		}
		seg := buildPromptSegmentsFromLayers(layers, task.UserPrompt, tr.render(), "Next action as one JSON object.")

		result, err := thinkWithRetryModelSegments(ctx, d.reasoner, taskModel(task), seg)
		raw := result.Text
		if err != nil {
			if ctx.Err() != nil {
				_, _ = d.sched.Cancel(task.RunID)
				return "subagent cancelled"
			}
			d.sched.SetStatus(task.RunID, "failed")
			return "subagent failed: " + err.Error()
		}
		d.record(sess.SessionID, "ModelResponded", task.RunID, "model",
			map[string]any{"turn": turn, "text": truncate(sanitizeModelResponseForAudit(raw), 300)}, "")

		// Per-subagent token budget (whale-session protection).
		turnTokens := result.Usage.totalTokens()
		if turnTokens == 0 {
			turnTokens = estimateTokens(seg.full()) + estimateTokens(raw)
		}
		d.sched.AddTokens(task.RunID, turnTokens)
		if !result.Usage.Estimated {
			tr.noteObservedInputTokens(result.Usage.InputTokens)
		}
		if mtt := d.maxTaskTokens.Load(); mtt > 0 {
			if t, ok := d.sched.Get(task.RunID); ok && int64(t.TokensUsed) > mtt {
				d.sched.SetStatus(task.RunID, "degraded")
				return "(subagent hit token budget)"
			}
		}

		act, perr := parseAction(raw)
		if perr != nil {
			tr.addTurn(Turn{Tool: "system", ActionBrief: "reparse", Obs: Observation{Content: "reply with one valid JSON action"}})
			continue
		}
		if act.Tool == "done" {
			tr.addTurn(Turn{Tool: "done", ActionBrief: "done", Obs: Observation{Content: act.Summary, Pinned: true}})
			if !d.persistFinalCheckpoint(sess, task, tr, turn, memorySnapshot) {
				// Candidate drafts are untrusted inputs to the parent orchestrator.
				// Preserve the draft so best-of-n can run its source-provenance
				// check and report a stale read precisely, while the child itself
				// remains degraded and exposes no forkable completion boundary.
				if spec.Name == "candidate-drafter" {
					return act.Summary
				}
				return "subagent failed: final checkpoint could not be persisted"
			}
			d.finish(sess, task, act.Summary)
			return act.Summary
		}
		if act.Tool == "spawn" {
			// subagents don't re-spawn in this MVP (depth already bounded);
			// keep them focused.
			tr.addTurn(Turn{Tool: "system", ActionBrief: "no-spawn", Obs: Observation{Content: "subagents cannot spawn; do the work directly or finish"}})
			continue
		}
		// Same canonical, all-fields signature the main loop uses (agent.go's
		// runLoopContext) so subagents get the same tightened loop detection
		// instead of a narrower hand-picked fingerprint. swarm_receive is
		// exempt: polling a channel for a not-yet-arrived message is
		// EXPECTED to look identical call to call — that's legitimate
		// waiting, not the stuck-model pattern this guard exists to catch.
		// It's still bounded by the ordinary max-turns ceiling (subagentMaxTurns),
		// just not by the identical-action hard-stop.
		var softRepeat, hardRepeat bool
		if act.Tool != "swarm_receive" {
			softRepeat, hardRepeat = guard.observe(act.Tool, act.signature())
		}
		if hardRepeat {
			d.sched.SetStatus(task.RunID, "degraded")
			return "(subagent loop guard: repeated actions with no progress)"
		}
		if softRepeat {
			tr.addTurn(Turn{Tool: act.Tool, ActionBrief: briefAction(&act),
				Obs: Observation{Content: "repeated action; change approach or finish with done"}})
			continue
		}
		obs, outcome := d.executeActionOutcome(sess, task, &act)
		if ctx.Err() != nil {
			_, _ = d.sched.Cancel(task.RunID)
			return "subagent cancelled"
		}
		// Same consecutive-failure circuit breaker as the main loop
		// (agent.go's runLoopContext), so a subagent stuck retrying a broken
		// tool degrades instead of burning its (smaller) turn budget.
		if mistakes.observe(outcome) {
			d.sched.SetStatus(task.RunID, "degraded")
			return "(subagent mistake tracker: too many consecutive tool failures)"
		}
		pinned := act.Tool == "run" || act.Tool == "patch" || act.Tool == "edit"
		compressedObs, err := d.compressObservation(ctx, sess, task, tr, turn, act.Tool, obs, pinned)
		if err != nil {
			d.sched.SetStatus(task.RunID, "failed")
			return "subagent failed: context compression failed: " + err.Error()
		}
		compressedObs.Error = outcome.observationError
		newTurn := Turn{Thought: act.Thought, Tool: act.Tool, ActionBrief: briefAction(&act),
			Obs: compressedObs}
		// Same path-keyed stale-read dedup as the main loop (agent.go's
		// runLoopContext) — see Transcript.supersedeStaleReads.
		if act.Tool == "read" {
			newTurn.Path = act.Path
		}
		tr.addTurn(newTurn)
	}
	d.sched.SetStatus(task.RunID, "degraded")
	if tr.Summary != "" {
		return "(subagent hit turn limit) " + tr.Summary
	}
	return "(subagent hit turn limit without finishing)"
}

func specNames(specs map[string]*AgentSpec) []string {
	out := make([]string, 0, len(specs))
	for name := range specs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func spawnUsesWorktree(profile string) bool {
	switch strings.TrimSpace(profile) {
	case "", "read-only", "sandboxed":
		return false
	default:
		return true
	}
}

func spawnIsolationMode(worktreeID string) string {
	if worktreeID != "" {
		return "worktree"
	}
	return "shared"
}

func (d *Daemon) prepareSpawnWorkspace(parentRoot, owner string, isolate bool) (string, string, func(), error) {
	noop := func() {}
	if !isolate || d == nil || d.worktrees == nil {
		return parentRoot, "", noop, nil
	}
	id := sessionstore.NewID("spawn")
	rec, err := d.worktrees.Create(id, parentRoot, "HEAD", "", owner)
	if err != nil {
		if strings.Contains(err.Error(), "not a git repository") {
			return parentRoot, "", noop, nil
		}
		return "", "", noop, err
	}
	if _, err := d.worktrees.Lock(rec.ID, owner); err != nil {
		_ = d.worktrees.Cleanup(rec.ID, owner, true)
		return "", "", noop, err
	}
	release := func() {
		_, _ = d.worktrees.Unlock(rec.ID, owner)
		_ = d.worktrees.Cleanup(rec.ID, owner, true)
	}
	return rec.Path, rec.ID, release, nil
}
