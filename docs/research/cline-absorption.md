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
- **Structured compaction summary template** (`agentic_summary_template`,
  `go/daemon/agent.go`, `go/daemon/transcript.go`, commit `1de7fcc`). Landed
  on the fourth attempt once `agent.go`/`daemon.go`/`subagent.go` cleared the
  concurrent-work seam that blocked the first three. `SummaryContent`
  (Goal/Done/InProgress/Blocked/Highlights/Next/FilesRead/FilesModified) plus
  `renderSummaryTemplate` and a fail-closed `parseSummaryContent` (falls back
  to raw prose, never fabricates structure, if the model doesn't follow the
  shape) replace the old unstructured "Summarize... <=200 words" instruction.
  `filesTouched` derives the Files section deterministically from the
  transcript's own turns (`ActionBrief` read/patch entries), not model
  recall. Wired into `runLoopContext`'s summarize closure in `agent.go`;
  `Transcript.Summary` stays a plain string, so `checkpoint.go`/`subagent.go`
  needed no changes. `subagent.go`'s separate lighter-weight summarizer was
  deliberately left untouched, out of this item's scope. 5 new tests in
  `transcript_test.go` cover render/parse round-trip, fail-closed parsing of
  unstructured prose, deterministic files dedup/ordering, and an end-to-end
  `compact()` pass through the full pipeline.
- **Plan/Act mode-switch notice injection** (`mode_switch_notice`,
  `go/daemon/daemon.go`, commit `1fbb4b8`). Landed by extending the now-landed
  two-tier `steer_vs_queue_priority` `taskMailbox` rather than the old
  single-tier mailbox this item's design was originally sketched against.
  `noticePlanModeSwitch(sessionID, on)` looks up the session's active task via
  the existing `activeChannelTask` helper and queues an urgent-tier
  `steerWithPriority` notice; wired into `handlePlanMode` and
  `handleApprovePlan`, the two RPC handlers that can flip plan mode mid-run.
  Deliberately does not touch the two task-start-time `setPlanMode` call
  sites in `agent.go` (mode is already reflected in the first turn's prompt
  there, so a mailbox notice would be redundant) or `isPlanMode`'s
  enforcement gate — this is context legibility only, no enforcement change.
  2 new tests in `planmode_test.go` cover urgent-ahead-of-normal draining and
  the no-active-task no-op case.

All nine mechanisms this review found real value in are now landed. The two
rejections stand on their original architectural grounds (see below).

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

None. Both items that were open as of the prior pass —
`agentic_summary_template` and `mode_switch_notice` — landed this pass once
`agent.go`/`daemon.go`/`subagent.go` presented a clean, reviewable window;
see Absorbed above for the final shape and commits. This closes out every
mechanism this review found real value in.

## Trade-offs

This is the closing pass for the Cline absorption review: all nine
mechanisms this review found real value in are now landed, across four
separate passes as `agent.go`/`daemon.go`/`subagent.go` cycled between dirty
and clean. Two design-revision fixes (dual-threshold compaction trigger,
line-shift-tolerant diagnostics delta) landed early once identified — both
were cheap, under 150 lines including tests, and shipped the same session
they were caught. Five mechanisms (tightened loop detection, the
consecutive-failure circuit breaker, path-keyed stale-read elision, the
two-tier steering mailbox, and head+tail artifact preview truncation) landed
together in one coherent pass once a genuinely concurrent, unrelated wave of
edits to the three core files finally settled. The last two
(`agentic_summary_template`, `mode_switch_notice`) took three and two
respective attempts to find that same clean window before finally landing —
`mode_switch_notice` in particular ended up depending on
`steer_vs_queue_priority` having landed first, since its design was revised
to extend the new two-tier mailbox rather than the single-tier one it was
originally sketched against.

The recurring lesson across this whole review: "blocked on design revision"
and "blocked on dirty file" are different kinds of blockers and should be
triaged differently. Design-revision blockers get fixed with design work.
Dirty-file blockers are a property of the codebase's churn rate, not of the
item — they get fixed by waiting for (or opportunistically catching) a clean
window and then moving fast once it appears, exactly as happened here. Two
mechanisms were rejected outright, on architectural grounds unrelated to
file-dirtiness, because they solve a failure mode (fuzzy anchor-matching in
hunk-based patches) that carina's full-file-replacement-plus-hash-conflict
patch model does not have and has already declined to introduce, per
`kilocode-absorption.md`. With those two rejections and all nine absorptions
final, this review is fully closed out.
