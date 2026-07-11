# Codebuff Absorption Decision

Source reviewed: `CodebuffAI/codebuff`.

Carina absorbs mechanisms only when they preserve the daemon, capability
kernel, and append-only audit log as the single authority.

## Absorbed

None this round. All four candidate mechanisms either duplicate capability
carina already has under a different (and in one case more principled)
design, or require control-flow and RPC-boundary work that is not safe to
land against currently in-flight call sites.

## Already Present

- **Symbol-importance scoring for repo maps.** `crates/carina-index/src/repomap.rs`
  already implements a confidence-weighted PageRank over the symbol
  reference/call graph (`fn pagerank`, `fn build`), cut to a token budget via
  the daemon's `len/4+1` estimator (`fn token_estimate`), with a `focus_paths`
  multiplicative boost (`FOCUS_BOOST=2.0`) analogous to Aider's "chat files"
  bias. It is exposed end-to-end as the `index_map` RPC in
  `crates/carina-kernel/src/bin/carina-kernel-service.rs` (`index.repo_map(&opts)`),
  accepting `token_budget` and `focus_paths`. This is not codebuff's exact
  formula (`0.8^depth × sqrt(loc/ids) × log(indegree)`), but PageRank is a
  materially equivalent — arguably more principled — importance signal for the
  same goal. No Go-side caller exists yet (the capability lives entirely in
  the Rust kernel/index crates), and no prior wave has claimed credit for it
  in `absorption-status.md` or `absorption-plan.md`.

## Deferred

- **Token-triggered, tool-aware context pruning with 80/20 head+tail
  truncation.** Carina already has a real, audited, tested compaction
  pipeline — `transcript.go`'s `compact()` plus `context_compression.go`'s
  `compressObservation()`, both wired from `agent.go` and `subagent.go`, with
  a circuit breaker, pinned-observation protection, and hash-chained
  compaction receipts. But none of codebuff's three specific mechanisms are
  present: the trigger is a flat char budget (`MaxChars=24000`), not a
  token count, and there is no 5-minute prompt-cache-expiry co-trigger
  (`promptcache.go` is an unrelated stable-prefix/volatile-suffix split for
  provider-side cost caching, not a pruning signal); truncation is head-only
  (`Content[:ToolOutputMax]+"...[truncated]"`), not an 80/20 head+tail split;
  and `contextengine.CompressRequest` carries `Tool`/`Kind` fields that are
  never branched on anywhere (zero call sites) — one uniform elision policy
  handles every tool type. Subagent output is filtered, but via full
  return-value isolation (only `act.Summary` crosses back,
  `subagent.go:88-94`) rather than a configurable denylist — architecturally
  stronger than codebuff's blacklist, not a gap to close. `absorption-plan.md`
  already lists "Multi-tier compaction (token trigger + circuit breaker +
  verbatim-user + rebuild-with-key-files)" as an open Wave 2 item, so this is
  roadmapped, not newly discovered; the circuit-breaker and pinned-preservation
  pieces of that TODO are in fact already done, leaving only token-trigger and
  rebuild-with-key-files genuinely open. The real trigger/dispatch call site
  (`go/daemon/agent.go`, where `tr.compact` and `compressObservation` are
  actually invoked per turn) is dirty right now, so any change that makes a
  *behavioral* difference in the loop would land against a moving target.
  Revisit once `agent.go`'s current changes land.

- **Agent-level (subagent) rewind.** `go/daemon/subagent.go`'s
  `runSubagentLoop` never calls `d.runs.saveCheckpoint`/`saveCheckpointChecked`,
  so there is no snapshot history for a subagent to rewind from — this is a
  genuine absence, not just an unexposed capability. The storage layer
  (`go/daemon/runstore.go`) is already keyed purely by `TaskID`, and subagents
  already get distinct `TaskID`s via `SubmitWithGoalModelAgent`, so the
  write-path addition is small and clean. The read/rewind path is not: both
  `handleCheckpointList` and the shared `checkpoint()` helper in
  `go/daemon/checkpoint.go` hard-gate on `task.SessionID == params.SessionID`,
  and subagents run in a child session (`CreateSubSession`) distinct from the
  parent's, so today's checkpoint RPCs structurally cannot see subagent
  checkpoints even if they existed. Restore-and-resume is wired only through
  `runTask`/`resumeTask` via the session-fork mechanism
  (`ForkedFromTaskID`/`ForkedThroughTurn`) — there is no equivalent primitive
  for resuming a live, already-executing `runSubagentLoop` mid-flight, since
  `spawnSubagent` today is a single synchronous call. Real rewind requires a
  new resume entry point parallel to `resumeTask` plus deliberate
  session-boundary scoping in the checkpoint RPCs — genuine multi-file design
  work, not a patch. This is best understood as a narrower echo of the
  already-adopted session-level fork+rewind wave
  (`absorption-plan.md`, session-level rewind entry) rather than a new
  pattern; subagents are intentionally cheap, disposable, and bounded, so the
  payoff-per-engineering-dollar is lower than it was for session-level
  rewind. No candidate file is dirty — this is deferred on scope, not on
  merge risk.

- **Best-of-N generation with a selector.** No replica-count, voting, or
  selector primitive exists in `go/daemon/workflow.go` or `workflowspec.go` —
  `WorkflowStep` fans out distinct DAG-ready steps concurrently, not N copies
  of the same step. `verifier.go` is a single-candidate pass/reject judge, not
  a chooser among many. `docs/research/workflow-orchestration.md` already
  lists "Voting (同任务跑 N 次)" as one of five aspirational parallelization
  patterns for a future workflow engine phase, explicitly sequenced behind
  linear chaining, conditional edges, and static parallel fan-out/join — this
  is a documented, already-deferred future phase of carina's own roadmap, not
  a discovery from codebuff. The kilocode absorption record's rejection of
  "unbounded MCP startup concurrency" and "in-process high-privilege plugins"
  reflects the same instinct that applies here: don't add unconditional N-way
  cost/concurrency without a bounded, audited, kernel-gated seam. Given it's
  already roadmapped, the safe version needs the parallel fan-out/join and
  evaluator-optimizer machinery to exist as audited infra first, and
  `verifier.go` plus retry-on-reject already captures most of the practical
  payoff at far lower unconditional cost — this is a clean defer, to be built
  as a "voting" step-kind on top of already-planned primitives, not
  prioritized now.

## Deliberately Rejected

None this round. Every candidate mechanism was either already covered by an
architecturally comparable (or stronger) carina design, or is a legitimate
future capability blocked on sequencing and call-site stability rather than
on any conflict with carina's governance model.

## Trade-offs

This review round shipped no code. Its value is in closing the loop on four
candidates: one turned out to already be implemented (repo map symbol
scoring, via PageRank rather than codebuff's formula, and now recorded so a
future pass doesn't re-review it) and three are real, precedented, roadmapped
items that are deliberately not being rushed. Token-triggered compaction and
tool-aware truncation are deferred purely on timing — the call sites that
would make the change behavioral (`go/daemon/agent.go`) are mid-flight-dirty,
and landing there now risks reviewing against a moving target. Subagent-level
rewind is deferred on scope — the storage half is a one-file addition, but the
resume/RPC half requires a new control-flow primitive and deliberate
session-boundary relaxation, which is multi-file design work that shouldn't
be forced into this pass. Best-of-N is deferred on sequencing — it depends on
parallel fan-out/join and evaluator-optimizer primitives that don't exist yet
as audited infrastructure, and forcing it in early would mean either
unbounded replica cost or a half-built selector with no bounded seam to gate
it, both of which conflict with carina's "no unconditional concurrency
without a kernel-gated seam" precedent.
