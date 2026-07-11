# Cline Absorption Decision

Source reviewed: `cline/cline`, Bun monorepo layout —
`sdk/packages/{core,llms,agents,shared}` + `apps/{vscode,cli}`.

Carina absorbs mechanisms only when they preserve the daemon, capability
kernel, and append-only audit log as the single authority.

## Absorbed

- **Consistent dual-threshold compaction trigger** (`dual_threshold_compaction`,
  `go/daemon/transcript.go`, commit `a817f21`). See Deferred below for the
  design flaw the adversarial pass caught and how it was fixed.
- **Line-shift-tolerant pre/post-edit diagnostics delta**
  (`pre_post_diagnostics_diff`, `go/daemon/diagnostics.go`, commit `1c778bf`).
  Standalone algorithm landed and tested; behavioral wiring (calling it before
  *and* after an edit) is a one-line `agent.go` change, still deferred — see
  below.
- **Tightened loop detection** (`loop_detection`, `go/daemon/transcript.go`,
  `go/daemon/agent.go`, `go/daemon/subagent.go`, commit `b6051dd`). Once
  `agent.go`/`daemon.go`/`subagent.go` returned to a clean base, the design
  sketched below landed as written: `action.signature()` replaces the
  hand-picked 5-field fingerprint with a canonical JSON-hash over every
  parameter field except free-form `Thought`, and `LoopGuard` gained a
  cumulative `MaxHardRepeat` mistake counter (`observe`/`hardStop`) so a
  model rotating between several repeated actions still trips a hard stop
  even when no single signature crosses the soft-nudge threshold. Wired into
  both the main loop and the subagent loop, calling `d.degrade(...)` once
  hard-stopped, avoiding the inconsistent half-measure this doc warned
  against. Tests: `TestLoopGuardHardRepeatedTripsOnSingleSignature`,
  `TestLoopGuardHardRepeatedTripsAcrossRotatingSignatures`,
  `TestLoopGuardHardRepeatDisabledWhenZero`,
  `TestActionSignatureCanonicalAcrossAllFields`,
  `TestActionSignatureExcludesThought`, `TestActionSignatureIncludesActions`,
  `TestActionSignatureStableAndDeterministic`,
  `TestActionSignatureIncludesBatchPayloadButNotThought`, and an end-to-end
  `TestLoopHardStopDegradesBeforeMaxTurns`.
- **Consecutive-failure circuit breaker** (`mistake_tracker`,
  `go/daemon/tool_lifecycle.go`, `go/daemon/agent.go`, `go/daemon/subagent.go`,
  commit `694e3cc`). `MistakeTracker` mirrors `LoopGuard`'s shape: it counts
  consecutive non-`"completed"` `toolExecutionOutcome` statuses and degrades
  once the streak crosses `MaxConsecutive` (default 3), catching a model
  rotating across several *different* failing tool calls — orthogonal to
  `LoopGuard`, which only fires on identical repeated actions. The blocker
  this doc identified (`executeAction` narrowing the outcome to a display
  `string` before it reached `runLoop`) was fixed additively: the existing
  body moved into a new sibling `executeActionOutcome`, leaving
  `executeAction`'s signature and ~90 existing call sites untouched. Wired
  into both the main and subagent loops. Tests: 8 new cases in
  `mistake_tracker_test.go`, including two end-to-end integration tests using
  the `newLoopDaemon`/`scriptedReasoner` harness.
- **Read-path dedup for stale re-reads** (`stale_read_dedup`,
  `go/daemon/transcript.go`, `go/daemon/agent.go`, `go/daemon/subagent.go`,
  commit `38ba80e`). `Transcript.supersedeStaleReads`, called from `addTurn`
  whenever the new turn carries a `Path`, elides any earlier non-pinned,
  not-yet-elided turn that read the identical path — the same
  `Observation.Elided`/`OriginalSHA256` mechanism `compact()`'s age-based
  elision already uses, keyed on path identity instead of turn age. Wired at
  the `"read"`-tool `addTurn` call sites in both loops; search/list/batch
  results are untouched since they aren't identified by one stable path.
  Four new tests in `transcript_test.go` cover basic supersede-with-hash,
  path-scoping, pinned/path-less immunity, and no double-processing of an
  already-elided turn.
- **Steer-vs-queue delivery priority** (`steer_vs_queue_priority`,
  `go/daemon/daemon.go`, `go/daemon/ecosystem.go`, commit `1281f76`). A new
  `taskMailbox` (urgent/normal `[]string` tiers, urgent always drained first,
  FIFO within each tier) plus a fail-closed `steerPriority`/
  `parseSteerPriority` and a `priority` param on `task.steer`. The existing
  `steer()` remains a normal-priority convenience wrapper so unrelated call
  sites are unaffected. `ecosystem.go`'s channel-event call site now uses
  `steerUrgent` so external events (e.g. CI failures) preempt queued routine
  steering for a running task. 6 new tests in `steering_test.go`.
- **Mid-command-output truncation, head+tail preserved** (`mid_truncation`,
  `go/artifact/store.go`, `go/daemon/tool_lifecycle.go`, commit `a8be846`).
  `makePreview()` now builds a genuine head+tail preview (head slice + tail
  slice joined by an "N bytes omitted" marker; line-budget split
  ceil-biased to head, byte-budget split evenly) instead of pure head-only
  truncation, completing the artifact-store wiring this doc said was the
  correct integration surface. `finishToolCall` now passes `PreviewBytes`
  (reusing `transcript.go`'s `ToolOutputMax`) into its existing `Store.Put`
  call and surfaces a boolean `artifact_truncated` output flag; the raw
  preview text is deliberately kept out of the audited `ToolCallCompleted`
  payload (`safeOutputMetadata`'s hash-only redaction stays load-bearing) —
  the full preview is reachable via the existing `handleArtifactRead` RPC.
  5 new tests across `store_test.go` and `tool_lifecycle_test.go`.

Everything else from this review either mismatches carina's architecture,
contradicts a prior rejection, or still lacks a clean (non-dirty-blocklisted)
integration seam. See Deferred below for the two items still parked.

## Deliberately Rejected

- **Three-tier diff-error escalation text** (`diff_error_escalation`). Cline's
  three-tier guidance exists to manage a fuzzy SEARCH/REPLACE anchor-matching
  failure mode. Carina's patch model is full-file-content replacement with
  SHA-256 base-hash conflict detection (`crates/carina-patch/src/lib.rs`,
  wired at `go/daemon/agent.go:864-931`), so the underlying failure class this
  text is written for does not structurally exist here. This is a repeat of
  the same call already made in `kilocode-absorption.md`'s "Model-name
  substring routing and permissive fuzzy edits" rejection: carina does not
  want permissive/fuzzy edit-matching UX, and there is no standalone unit to
  land — the entire value of the mechanism is in wiring an attempt/retry
  field into the dirty-blocklisted tool-failure path
  (`go/daemon/tool_lifecycle.go:20-34`), which has no such field today.

- **Cascading fuzzy diff/patch matching with fuzz score**
  (`fuzzy_patch_matching`). Not a hardening of an existing fuzzy matcher —
  carina has zero hunk-locate or Levenshtein code anywhere in the repo.
  Adopting Cline's cascade presupposes first adopting Cline's SEARCH/REPLACE
  hunk format in place of carina's exact full-file-replacement model, which is
  a separate, larger protocol decision (`FileChange` shape, RPC schema,
  model-facing patch contract) that this review does not have standing to
  make. No prior wave has taken a position on hunk-based vs. full-content
  patching, so this stays out of scope rather than landing as an orphaned,
  uncallable locator module.

## Deferred

Two candidates remain open. Both are architecturally accepted — extensions of
already-adopted patterns, no fail-closed or kernel-authority concern — but
still need a clean, reviewable window in `go/daemon/agent.go` /
`go/daemon/daemon.go` / `go/daemon/subagent.go` to wire in. This is now the
third documented attempt for both; the repo's dirty-file churn during this
absorption effort has consistently outpaced single-item wiring passes, so
each attempt below is condensed to current status rather than re-narrated in
full — see git history on this file for the earlier blow-by-blow accounts if
needed.

Five siblings that were parked alongside these two — `stale_read_dedup`,
`mistake_tracker`, `loop_detection`, `steer_vs_queue_priority`, and
`mid_truncation` — have since landed; see Absorbed above.

- **Structured compaction summary template** (`agentic_summary_template`,
  partially present). `CompactionReceipt` (`transcript.go:55-63`) already
  gives carina a stronger integrity guarantee than Cline has (dual SHA-256
  hash chain over the compaction event; Cline has no cryptographic binding on
  its summary at all). What's missing is the summary *content* shape: Cline
  types Goal/State(Done|InProgress|Blocked)/Highlights/Next/Files(read+
  modified); carina's is unstructured prose from one hand-written
  instruction (`agent.go:184-185`, "Summarize... <=200 words"), no parser, no
  schema. Additive, does not touch the kernel. A standalone `SummaryContent`
  struct plus template/parse helper could land in a new file today, but it
  delivers zero behavioral value until wired into `agent.go`'s summarizer
  call site.
  **Status after three attempts**: blocked each time by `agent.go`/
  `daemon.go`(/`subagent.go`) being dirty with unrelated concurrent work at
  the moment of the precondition check — first a `runTaskContext`/
  `lifecycleCallID`/`go/runtimecontract` context-cancellation wave, most
  recently (this pass) a `lifecycleCallID` field plus tool-call lifecycle
  plumbing in `agent.go`, new `taskContexts`/`taskCancels`/`activeToolCalls`
  maps and a `runArtifactGC` loop in `daemon.go`, and an `executeSpawn` →
  `executeSpawnOutcome` refactor in `subagent.go` — all with mtimes seconds
  to minutes old, i.e. actively being written, not stale leftovers. Zero
  edits made each time. Re-run once all three files are confirmed clean (or
  carry only this run's own prior-item edits); the design above is still the
  intended shape.

- **Plan/Act mode-switch notice injection** (`mode_switch_notice`, partially
  present). Architecturally the same shape as the already-adopted "steer"
  pattern (mailbox drained at turn boundary, injected as a pinned user turn)
  — this is extending an accepted pattern to a second event source, not
  adopting a new mechanism, and it never touches enforcement, only context
  legibility, so there's no fail-closed concern blocking it on principle.
  Every touch point (`setPlanMode`/`isPlanMode`/`handlePlanMode`/
  `handleApprovePlan`/`planMode` map in `daemon.go`, and the turn-loop
  consumer in `agent.go`) sits in the dirty files, with mailbox/plan-mode
  state as private `*Daemon` fields — no exported seam a new file could hook
  without editing either.
  **Status after two attempts**: blocked each time on the same dirty-file
  precondition as `agentic_summary_template` above (the two items share the
  same blocking files and were checked together this pass); most recently
  `agent.go`/`daemon.go`/`subagent.go` were all modified by the concurrent
  wave described there. Zero edits made. Re-run once the same three files
  clear; note the now-landed two-tier `steer_vs_queue_priority` mailbox
  (`daemon.go`'s `taskMailbox`) is the pattern to extend for the second event
  source, not the old single-tier mailbox this item's design was originally
  sketched against.

## Trade-offs

This review round absorbed five previously-deferred mechanisms outright once
`agent.go`/`daemon.go`/`subagent.go` returned to a clean, reviewable base:
tightened loop detection (canonical action signature + cumulative hard-stop
threshold), a consecutive-failure circuit breaker (`MistakeTracker`),
path-keyed stale-read elision, a two-tier urgent/normal steering mailbox, and
head+tail-aware artifact preview truncation (completing the
`mid_truncation`/artifact-store integration this doc had previously called
"blocked on both: a half-built carina mechanism already covers the target
problem"). Combined with the two design-revision fixes from the prior pass
(dual-threshold compaction trigger, line-shift-tolerant diagnostics delta),
seven of the nine mechanisms this review found real value in are now landed.
Two mechanisms were rejected outright because they solve a failure mode
(fuzzy anchor-matching in hunk-based patches) that carina's
full-file-replacement-plus-hash-conflict patch model does not have and has
already declined to introduce, per `kilocode-absorption.md`.

The lesson from the earlier rounds held: "blocked on design revision" and
"blocked on dirty file" are different kinds of blockers and should be
triaged differently. The design-revision fixes were cheap once identified
(both under 150 lines including tests) and landed the same session they were
caught. The dirty-file blocks were a genuine external constraint — a
concurrent, unrelated wave actively committing to `agent.go`/`daemon.go`/
`subagent.go` — that no amount of design work resolved; only a wait for a
stable base did, and once that base arrived, all five ready items landed in
one coherent pass rather than six separate contentious edits, exactly as
earlier attempts had planned. The two mechanisms still open
(`agentic_summary_template`, `mode_switch_notice`) remain blocked on that
same seam as of this pass — the wave has not been a one-time event but a
recurring condition of this codebase's churn rate, so both should be
re-attempted opportunistically rather than scheduled for a specific future
session.
