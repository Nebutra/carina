# Codebuff Absorption Decision

Source reviewed: `CodebuffAI/codebuff`.

Carina absorbs mechanisms only when they preserve the daemon, capability
kernel, and append-only audit log as the single authority.

## Absorbed

- **Token-triggered, tool-aware context pruning** (`context_pruner_agent`,
  `go/daemon/transcript.go`, commit `5898e17`). Landed on the second attempt
  after the first was reverted when `daemon.go` turned dirty mid-attempt (see
  prior revision of this doc for that account). `CompactionPolicy.MaxTokens`
  (default 0, byte-identical to prior char-only behavior) plus a
  `shouldCompact()` combiner that ORs the existing char-based trigger
  (`t.size() > triggerChars()`) with a token-estimate trigger
  (`estimateTokens(t.render()) > MaxTokens`, reusing `agent.go`'s existing
  `estimateTokens` helper). Composes with, rather than duplicates,
  `dual_threshold_compaction`'s `triggerChars()`/`ReserveChars`/
  `ThresholdRatio` machinery — `shouldCompact()` calls `triggerChars()`
  internally, so both mechanisms stack. Wired into both of `compact()`'s
  existing gates, replacing the two `t.size() <= trigger` checks. 4 new tests
  in `transcript_test.go`: `MaxTokens=0` parity with the char-only trigger,
  token trigger firing below the char budget, token trigger correctly not
  firing when set high, and `compact()` proceeding through both gates on the
  token trigger alone. This resolves the `context_pruner_agent` slice of
  `absorption-plan.md`'s Wave 2 "multi-tier compaction" TODO that remained
  open after the circuit-breaker and pinned-preservation pieces landed
  earlier; rebuild-with-key-files remains a separate, still-open item outside
  this review's four original candidates.

Of the four candidate mechanisms this review evaluated, one duplicated
capability carina already has under a different (and arguably more
principled) design (see Already Present), and two require control-flow and
RPC-boundary work judged out of scope for this campaign on genuine
sequencing grounds (see Deferred).

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

Two candidates remain deferred, both on genuine multi-file scope/sequencing
grounds rather than on any dirty-file blocker. `context_pruner_agent`, the
third deferred candidate as of the prior pass, has since landed — see
Absorbed above.

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

This is the closing pass for the codebuff absorption review's actionable
scope. Of the four candidates originally identified, one turned out to
already be implemented (repo map symbol scoring, via PageRank rather than
codebuff's formula, now recorded so a future pass doesn't re-review it), and
one — token-triggered context pruning (`context_pruner_agent`) — has now
landed after two attempts: the first was reverted, unlanded but fully
validated, when `go/daemon/daemon.go` turned dirty with unrelated concurrent
work between the start of the attempt and the commit step; the second, once
`transcript.go` and its test file presented a clean window again, landed the
same design that had already been build/vet/test-validated the first time.
The tool-aware-truncation slice of the original candidate had separately
already been subsumed by `cline-absorption.md`'s `mid_truncation` landing
genuine head+tail preservation at the artifact-preview layer.

The remaining two candidates — subagent-level rewind and Best-of-N generation
— are genuinely deferred on scope and sequencing, not on any dirty-file
blocker, and were never expected to land in this campaign. Subagent-level
rewind needs a new resume/RPC control-flow primitive plus deliberate
session-boundary relaxation in the checkpoint RPCs — real multi-file design
work, not a patch, and lower payoff-per-engineering-dollar than the
already-landed session-level rewind it would echo, since subagents are
intentionally cheap and disposable. Best-of-N depends on parallel
fan-out/join and evaluator-optimizer primitives that don't exist yet as
audited infrastructure; forcing it in early would mean either unbounded
replica cost or a half-built selector with no bounded seam to gate it, both
of which conflict with carina's "no unconditional concurrency without a
kernel-gated seam" precedent. Both remain correctly sequenced behind
prerequisite infrastructure that this review does not have standing to build
as a side effect.

With `context_pruner_agent` landed, the repo-map item confirmed already
present, and the two remaining items deferred on grounds unrelated to this
codebase's file-dirtiness churn, this review's actionable scope is fully
closed out.
