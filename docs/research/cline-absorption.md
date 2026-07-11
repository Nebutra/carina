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

Everything else from this review either mismatches carina's architecture,
contradicts a prior rejection, or lacked a clean (non-dirty-blocklisted)
integration seam at review time. See Deferred below for the items with real
value once their seam clears.

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

Every remaining candidate from this review has real value but was blocked on
one of two grounds: no clean (non-dirty) file seam existed at review time, or
the design as reviewed needed revision before it was safe to land. None of
these are rejections — they are scoped for a follow-on pass once the relevant
files settle, matching the `absorption-status.md` Wave 13–16 pattern where
"follow-on phases remain separate" rather than abandoned. Two of the
design-revision items have since been fixed and landed standalone (see below,
and Absorbed above); the rest remain open.

**Blocked on dirty-file seam (`go/daemon/agent.go`, `go/daemon/daemon.go`)**

`agent.go` carried a large in-flight diff this session (154 insertions / 34
deletions, plus new `tool_lifecycle.go` / `artifact_rpc.go`), and
`daemon.go` is on the same dirty blocklist. Six candidates need a wiring
change inside one of these two files and therefore stay parked until the file
returns to a clean, reviewable base:

- **Read-path dedup for stale re-reads** (`stale_read_dedup`). The narrow,
  useful slice of Cline's mechanism — supersede a repeated read of the same
  path within a `Transcript` with an elision placeholder, reusing the
  existing `Observation.Elided`/`OriginalRef`/`OriginalSHA256` fields — is
  real incremental value analogous to `compact()`'s existing age-based
  elision, just keyed on path identity instead of turn age. Cline's other
  half, batching stale rewrites until ~65KB to protect an incrementally
  extending KV-cache prefix, does not transplant: carina's `CacheBreakpoint`
  only covers system+task, and the transcript (all reads included) is already
  fully in the volatile suffix re-encoded every turn, so there is no rolling
  cache-invalidation problem to protect against. The blocker is that the only
  integration point — tracking "this turn was a read of path X" — is
  `agent.go`'s `addTurn` call sites (~lines 412-437), inside the dirty diff.

- **Consecutive-failure circuit breaker** (`mistake_tracker`). The signal
  already exists as `toolExecutionOutcome.status`
  (`go/daemon/tool_lifecycle.go:20-42`), computed in `dispatchActionOutcome`,
  but `executeAction` narrows its return type to a display `string`
  (`agent.go:595,618`) before the status reaches `runLoop`, the only place a
  per-turn tracker could consume it. The standalone tracker struct is a
  trivial, fully unit-testable addition directly analogous to the
  already-adopted `LoopGuard` and `compactionCircuitBreaker` shapes — no
  architectural objection — but `runLoop`'s action-dispatch section
  (`agent.go` ~lines 424-437) is interleaved with in-progress `LoopGuard`/
  `guard.tick()`/checkpoint edits in the current diff.

- **Tightened loop detection** (`loop_detection`, partially present).
  `LoopGuard` (`go/daemon/transcript.go:184-211`) already does soft-nudge-at-3
  with a tested fingerprint+stall detector (`TestLoopGuardRepeatAndStall`)
  wired into both the main and subagent loops. Cline adds a canonicalized
  full-param signature (vs. five hand-picked fields) and a second hard
  threshold with mistake-count bookkeeping. The signature/struct improvement
  could land standalone in the clean `transcript.go`, but the call sites that
  would consume a hard-threshold signal — `guard.repeated(...)`, the
  nudge-vs-degrade branch — sit in `agent.go` ~lines 391-432, inside the dirty
  diff (`newLoopGuard()` itself is inside a dirty hunk). `subagent.go`'s call
  sites are clean, but shipping a hard interrupt only for subagents and not
  the primary loop would be an inconsistent half-measure, worse than not
  shipping. Existing soft-nudge + `stalled()`/`MaxTurns` already bound
  worst-case damage, so this is real polish, not an urgent gap-fill.

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
  call site — the wrong moment to stack onto an already-large in-flight diff.

- **Plan/Act mode-switch notice injection** (`mode_switch_notice`, partially
  present). Architecturally the same shape as the already-adopted "steer"
  pattern (`agent.go:229-234`: mailbox drained at turn boundary, injected as
  a pinned user turn) — this is extending an accepted pattern to a second
  event source, not adopting a new mechanism, and it never touches
  enforcement, only context legibility, so there's no fail-closed concern
  blocking it on principle. Every touch point (`setPlanMode`/`isPlanMode`/
  `handlePlanMode`/`handleApprovePlan`/`planMode` map at `daemon.go:1557-1610,
  1860-1874`, and the turn-loop consumer at `agent.go:229-234,607-609`) sits
  in the two dirty files, with mailbox/plan-mode state as private `*Daemon`
  fields — no exported seam a new file could hook without editing either.

- **Steer-vs-queue delivery priority** (`steer_vs_queue_priority`, partially
  present). Today there is exactly one delivery mode — unconditional append
  to a per-task `[]string`, drained only at the next turn boundary
  (`daemon.go:2020-2036`, `agent.go:222-234`) — with no priority field and no
  mid-flight interruption anywhere in the codebase. Wave 5's
  `absorption-status.md` claim ("redirect a running/background agent via
  `task.steer`") matches the code exactly; this is an unbuilt enhancement, not
  a broken promise. A real fix needs a two-tier mailbox touching the mailbox
  struct, `steer()`, `drainMailbox()`, and turn-loop consumption — all in
  `daemon.go`/`agent.go` — plus the `ecosystem.go` call site (also dirty)
  choosing which channel to write into. A standalone `SteerPriority`
  enum/policy type would sit inert and untestable-in-context until wired in.

**Design-revision items — fixed and landed standalone (wiring still deferred)**

Both items below were adversarially downgraded pending a design fix, not a
dirty-file seam. The fix was S-sized and touched only clean files, so it
landed immediately as its own commit; only the behavioral wiring into
`agent.go` remains deferred.

- **Pre/post-edit diagnostics diff** (`pre_post_diagnostics_diff`, **fixed,
  landed as `1c778bf`**). The reviewed algorithm — exact-line-match set-diff
  against diagnostics that embed absolute line numbers — was functionally
  broken for the common case: any edit that shifts line count above a
  pre-existing unrelated error would report that stale error as newly
  introduced, the opposite of the goal (reproduced directly with `gofmt`).
  `diagnosticsDelta(before, after)` in `go/daemon/diagnostics.go` now groups
  checker output into blocks (`diagnosticBlocks`) and matches by message
  content with the line/col location stripped (`diagnosticKey`), covering
  gofmt/node's single-line and py_compile's multi-line
  `File "...", line N` shapes; rustc's arrow-location line is deliberately
  left as a continuation, not a block boundary, to avoid fragmenting one
  diagnostic into two. Five new tests in `diagnostics_test.go` cover the
  reproduced line-shift case, a genuinely new error, the Python multi-line
  case, and the two empty-input edges. What's still deferred: calling
  `checkEdited` before AND after an edit (today it only runs after) is a
  one-line change at the `agent.go` call site — parked until that file is
  off the in-flight-work blocklist.

- **Dual-threshold compaction trigger formula** (`dual_threshold_compaction`,
  **fixed, landed as `a817f21`**). The reviewed design was incomplete in a
  way that mattered: it would have patched only one of `compact()`'s two
  `MaxChars`-gated branches (`transcript.go`), leaving the other on stale
  semantics — lowering the effective trigger would have caused
  destructive-but-unreceipted elision to fire silently more often, undermining
  the audit-completeness invariant the proposal claimed was untouched. Both
  gates in `go/daemon/transcript.go`'s `compact()` now read a single
  `triggerChars()` (`trigger = max(MaxChars-ReserveChars,
  MaxChars*ThresholdRatio)`, mirroring a token-budget technique adapted to
  carina's char-based policy), so the two gates cannot structurally drift
  again. Default `ReserveChars=0`/`ThresholdRatio=0` reduces to exactly
  today's `MaxChars` behavior — zero runtime change unless a caller opts in.
  New tests cover the default no-op case, the dual-bound formula across
  large/small/degenerate windows, and a regression test
  (`TestCompactUsesConsistentTriggerAcrossBothGates`) that only passes if
  both gates honor the same lowered trigger. Nothing further deferred here —
  `triggerChars()` is real, live logic inside `compact()` today; only opting
  a caller into non-default `ReserveChars`/`ThresholdRatio` values (a policy
  choice, not a code change) remains open.

**Blocked on both: a currently-half-built carina mechanism already covers the
target problem**

- **Mid-command-output truncation, head+tail preserved** (`mid_truncation`).
  Not a new idea for carina — `absorption-plan.md:55-56` already scopes
  "Tool-result disk offload... substitute a head+tail preview with a
  `read_result` pagination signal" verbatim, and `go/artifact/store.go`
  (untracked, new, tested via `store_test.go`) has already started this work
  with `Store.Put`'s `PreviewBytes`/`PreviewLines` — except `makePreview()`
  today is still pure head-only truncation (`raw[:end]`), so even the new
  package hasn't yet delivered head+tail. Three call sites currently do
  independent head-only cuts (`agent.go:1213` `truncate`, `transcript.go:88-
  91` `addTurn`, and `artifact/store.go`'s `makePreview`), with `artifact_rpc.go`
  exposing the store over RPC but not yet wired into `agent.go`'s
  `CommandOutput`/transcript path. Landing an isolated head+tail patch on any
  one of the three would very likely be obsoleted by the in-flight artifact-
  offload wiring within the same wave — the correct integration surface, per
  precedent set by `kilocode-absorption.md`'s already-adopted "content-
  addressed tool-output artifacts... bounded reads" line, is finishing
  `go/artifact.Store`'s wiring into `agent.go`'s `CommandOutput` path
  (oversized output → `Store.Put` with a head+tail-aware `makePreview` →
  transcript shows preview + pointer), not an ad hoc slice in `transcript.go`.

## Trade-offs

This review round absorbed two mechanisms outright (dual-threshold compaction
trigger consistency, line-shift-tolerant diagnostics delta) — both needed a
design fix the adversarial pass caught before they were safe to land, and
both fixes were small enough (S-sized, clean files) to land the same session
rather than sit in the queue behind the dirty-file blocker. Everything else
with real value either (a) only integrates through `agent.go` or `daemon.go`,
both mid-diff and dirty-blocklisted this session, or (b) targets a problem
carina is already mid-solving with a different, half-built mechanism
(artifact-backed truncation). Two mechanisms were rejected outright because
they solve a failure mode (fuzzy anchor-matching in hunk-based patches) that
carina's full-file-replacement-plus-hash-conflict patch model does not have
and has already declined to introduce, per `kilocode-absorption.md`.

The lesson worth keeping: "blocked on design revision" and "blocked on dirty
file" are different kinds of blockers and should be triaged differently. The
former is often cheap to actually fix once identified (both fixes above were
under 150 lines including tests); the latter is a genuine external
constraint (a concurrent, unrelated wave actively committing to the same
files) that no amount of design work resolves — only time and a stable base
does.

The cost of deferring instead of forcing these in now is coordination debt,
not lost value: standalone struct/policy code (loop-guard signature, mistake
tracker, summary template, path-dedup policy) can be written and unit-tested
in clean files today, but each delivers zero behavioral change until a
follow-on commit wires it into the same handful of `agent.go`/`daemon.go`
call sites once those files next come off the dirty blocklist. Bundling that
wiring into one coherent pass, rather than six separate contentious edits to
the busiest files in the tree, is worth the wait. The one exception —
mid-truncation — has no standalone piece worth writing separately at all,
since its outcome is fully subsumed by finishing the artifact-store wiring
already in progress.
