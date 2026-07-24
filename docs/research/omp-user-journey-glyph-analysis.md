# Oh My Pi user journey and glyph analysis

Analysis date: 2026-07-22

> Evidence status: historical research input. OMP source claims below are tied
> to the stated OMP revision. The Carina comparison was not tied to a Carina
> commit, and the referenced screenshot was not archived as a reproducible
> artifact. Statements describing "current Carina", visual quality, or relative
> product quality are withdrawn as current evidence. Recheck Carina against a
> fixed commit before reusing those conclusions.

## Executive conclusion

The screenshot supplied on 2026-07-22 recorded a historical interaction
concern. At that unpinned Carina baseline, the transcript appeared to read like
an event ledger:
successful tool-list and file-read operations, generic agent completion rows,
and repeated low-value system text occupy the same visual level as the user's
request and the final answer. At the same time, the sidebar notification can
say `Task finished`, the task rail can say `degraded`, and the footer can say
`ready`. The reported concern was that the operator had to infer actual state
from conflicting projections. This is historical user feedback, not a current
source finding or an objective visual-quality measurement.

Oh My Pi (OMP) is useful because it treats setup, conversation, activity,
governance, interruption, and session continuation as distinct interaction
surfaces. Its strongest ideas are information architecture and state contracts,
not a particular checkmark or spinner.

Carina should adopt a pre-composer readiness surface, a conversation-level
status reducer shared by transcript/rail/footer/notifications, a semantic glyph
registry with explicit compatibility modes, and grouped or ephemeral rendering
for routine successful reads and listings. Carina should adapt OMP's setup,
approval, session-picker, and animation mechanics to Carina's daemon and durable
continuity model. It should reject OMP branding, literal visual copying, and any
provider selection rule based on an installed CLI binary.

The provider invariant remains:

> Only an explicitly configured and runnable provider may become the implicit
> backend. Claude CLI, Codex CLI, Mox, and binary presence remain explicit
> compatibility choices and never become automatic defaults.

## Evidence boundary

The authoritative OMP source is `can1357/oh-my-pi` at commit
`7b141199d524b859c357fc89654f10b62b9f3df1`, tagged `v17.0.7`. OMP describes
itself as a fork of `badlogic/pi-mono` and documents its install paths in the
root README (OMP: `README.md:21-61`, `README.md:96-99`).

OMP evidence below is source-derived from that fixed revision, primarily under
`packages/coding-agent`, `packages/agent`, and `packages/tui`. Carina references
came from the then-current worktree under `go/tui`, `go/tuiapp`, and `go/daemon`,
but no Carina commit or screenshot artifact was recorded. They must not be
treated as current runtime evidence.

Line references prefixed with `OMP:` are relative to the OMP repository. Line
references prefixed with `Carina:` are relative to this repository.

## Historical Screenshot Observations

The unarchived screenshot motivated five hypotheses at the time. These are not
proof of current behavior and the Carina paths below may now be stale.

1. **Conversation hierarchy is weak.** Rows such as `agent completed`, tool
   list/read completion, and `file fileread` compete with the user request and
   assistant response. The recorded Carina snapshot filtered routing and several runtime
   events, but it intentionally keeps authoritative tool completions, model
   `done` blocks, and file reads visible (Carina: `go/tui/model.go:508-547`,
   `go/tui/transcript.go:404-416`, `go/tui/transcript.go:571-593`,
   `go/tui/transcript.go:643-684`, `go/tui/transcript.go:773-787`).
2. **Success is over-recorded.** Every routine successful operation can become
   permanent transcript history instead of transient activity or a grouped
   summary. The transcript is typed, but the projection is still event-first
   (Carina: `go/tui/transcript.go:17-83`).
3. **Readiness is asserted without a runnable reasoner.** The footer substitutes
   a missing model with the literal `default`; its activity defaults to `ready`
   whenever no local editor/submission/in-flight flag is set (Carina:
   `go/tui/product_shell.go:299-330`, `go/tui/product_shell.go:403-445`). The
   model picker also inserts a `default` row before proving that any provider is
   runnable (Carina: `go/tui/model_picker.go:138-169`).
4. **Terminal outcome is fragmented.** With no reasoner, the daemon records a
   durable `degraded` task and publishes the generic transient
   `task.completed` envelope (Carina: `go/daemon/agent.go:168-170`,
   `go/daemon/agent.go:740-763`, `go/daemon/notify.go:10-41`). The task rail
   correctly normalizes `degraded`, while the attention layer maps every
   terminal family to the fixed copy `Task finished` (Carina:
   `go/tui/taskgraph.go:62-85`, `go/tui/taskgraph.go:155-160`,
   `go/tui/attention.go:20-27`).
5. **The composer is visually detached from state.** After a terminal event,
   Carina clears `inFlightTaskID`; footer activity therefore falls back to
   `ready` even when the result was degraded (Carina:
   `go/tui/followup_flow.go:218-266`). The large empty viewport then makes the
   composer look available without explaining that no runnable provider exists.

The root cause is distributed presentation state:

```text
daemon events
  -> transcript projection
  -> task graph reducer
  -> footer local flags
  -> attention notification mapping
  -> session/recovery picker
```

Each consumer answers “what is happening?” independently. There is no single
conversation-level reducer that owns readiness, active/waiting/terminal state,
outcome severity, or the distinction between permanent conversation history
and ephemeral execution activity.

## OMP journey map

### First run

| Step | User goal and entry | State transition | Owning surface and feedback | Escape or recovery | Evidence |
| --- | --- | --- | --- | --- | --- |
| 1. Install | Obtain a runnable `omp` command | Package absent -> installed | Shell installer, Homebrew, Bun, PowerShell, or mise | Use another documented install path | OMP: `README.md:29-61` |
| 2. Launch | Enter the product from a workspace | Process start -> interactive host | Bare `omp` is interactive; initial prompt is also accepted | `--help`, explicit mode flags | OMP: `packages/coding-agent/src/commands/launch.ts:180-189` |
| 3. Setup gate | Configure a stale or new profile | Stored setup version -> selected scenes | Setup is loaded before ordinary transcript replay; TTY, resume, env skip, feature flag, and force control whether scenes run | Resume/skip/disabled setup bypasses scenes | OMP: `packages/coding-agent/src/main.ts:433-464`; `packages/coding-agent/src/modes/setup-wizard/index.ts:37-60` |
| 4. Provider sign-in | Make at least one provider usable | Unauthenticated -> OAuth in progress -> credential saved/failed | Full-screen provider scene, provider tabs, browser URL, progress text, success/error result | Esc/Ctrl+C cancels login; choose another provider or continue | OMP: `packages/coding-agent/src/modes/setup-wizard/scenes/providers.ts:7-27`; `packages/coding-agent/src/modes/setup-wizard/scenes/sign-in.ts:70-99`, `182-262` |
| 5. Model selection | Choose the model used for new sessions | Available models discovered -> persisted default role | Searchable model browser shows discovery, save progress, and failure | Cancel skips; failed refresh/selection stays in scene | OMP: `packages/coding-agent/src/modes/setup-wizard/scenes/model.ts:15-37`, `72-124` |
| 6. Glyph compatibility | Ensure symbols render correctly | Preset preview -> persisted Unicode/Nerd/ASCII preset | Live samples update the whole UI; copy explicitly warns about boxes and misalignment | Cancel skips; number keys preview alternatives | OMP: `packages/coding-agent/src/modes/setup-wizard/scenes/glyph.ts:5-24`, `53-95` |
| 7. Theme | Choose presentation theme | Default appearance -> persisted theme | Dedicated setup scene after glyph compatibility | Skip/cancel | OMP: `packages/coding-agent/src/modes/setup-wizard/index.ts:16-21` |
| 8. Composer arrival | Begin ordinary work | Setup complete -> welcome/composer | Setup overlay closes and welcome intro begins | Re-open configuration later | OMP: `packages/coding-agent/src/modes/setup-wizard/index.ts:76-102` |

Important limitation: OMP's setup scenes are skippable. The wizard is strong
on discoverability, but it is not the authority for readiness. Submission still
validates that a model and API key exist, and produces actionable errors when
they do not (OMP: `packages/coding-agent/src/session/agent-session.ts:8847-8890`).
Carina should learn the pre-composer explanation, not copy the possibility of
showing an ordinary “ready” composer while no backend can run.

### Normal recurring use

| Step | User goal and entry | State transition | Owning surface and feedback | Escape or recovery | Evidence |
| --- | --- | --- | --- | --- | --- |
| 1. Select context | Start new work or return to a session | New/continued/resumed session -> interactive transcript | CLI/session manager resolves explicit continuation contracts | Missing IDs get a useful error and picker hint | OMP: `packages/coding-agent/src/main.ts:667-779` |
| 2. Compose | State intent with text, files, or images | Draft -> submitted user message | Editor remains the primary interaction surface | Dispatch failure restores text/images | OMP: `packages/coding-agent/src/modes/controllers/input-controller.ts:592-645`, `784-815` |
| 3. Stream | Observe reasoning output and actions | Assistant stream -> message/tool segments | Message-oriented transcript reconstructs user, assistant, and tool components | Esc interrupts; transcript keeps durable prior content | OMP: `packages/coding-agent/src/modes/components/chat-transcript-builder.ts:80-115`, `219-230` |
| 4. Tool progress | Understand current activity without reading an audit log | Tool call pending -> running/update -> terminal | One tool component mutates in place; read calls can share a compact group | Expand for details; terminal result seals the component | OMP: `packages/coding-agent/src/modes/controllers/event-controller.ts:940-1076`; `packages/coding-agent/src/modes/components/read-tool-group.ts:29-40` |
| 5. Approval | Decide whether a governed tool may run | Requested -> approved/denied | Dedicated selector emits request/resolution events and offers Approve/Deny | Deny is explicit; headless mode fails with configuration choices | OMP: `packages/coding-agent/src/tools/approval.ts:13-38`, `94-168`; `packages/coding-agent/src/extensibility/extensions/wrapper.ts:123-203` |
| 6. Question | Supply structured input during execution | Running -> waiting for input -> answer/cancel | Stable-height overlay supports multiple questions, single/multi choice, notes, previews, and timeout | Cancel aborts; “chat instead” returns to conversation | OMP: `packages/coding-agent/src/tools/ask.ts:750-843`, `878-943`; `packages/coding-agent/src/modes/components/ask-dialog.ts:282-305`, `329-455`, `533-547`, `916-950` |
| 7. Steer or follow up | Redirect now or enqueue later | Streaming -> steer queue or follow-up queue | Enter steers; Ctrl+Enter queues a follow-up. Steering is drained before follow-ups | Failed queueing restores draft | OMP: `packages/coding-agent/src/modes/controllers/input-controller.ts:719-800`, `1253-1339`; `packages/agent/src/agent.ts:874-887`; `packages/agent/src/agent-loop.ts:1162-1183` |
| 8. Complete | Read the answer and terminal tool outcomes | Active -> terminal | Final assistant response remains primary; tool blocks settle in place | Continue with another prompt or resume later | OMP: `packages/coding-agent/src/modes/controllers/event-controller.ts:826-937`, `1032-1095` |

### Error and recovery

| Step | Failure or interruption | State transition | Visible feedback | Recovery contract | Evidence |
| --- | --- | --- | --- | --- | --- |
| 1. Missing model/key | Submission reaches runtime validation without a runnable model | Draft submission -> rejected before provider call | Specific missing-model/API-key message points to login/config/model | Configure provider, select model, resubmit | OMP: `packages/coding-agent/src/session/agent-session.ts:8874-8890` |
| 2. OAuth failure | Provider login fails or is cancelled | Login in progress -> failed/cancelled | Error and next action stay inside provider scene | Retry, choose another provider, or Esc | OMP: `packages/coding-agent/src/modes/setup-wizard/scenes/sign-in.ts:243-262` |
| 3. Tool denial | Approval policy or operator blocks a tool | Requested -> denied | Denial is an explicit resolution, not an inferred error string | Change policy or retry with a different action | OMP: `packages/coding-agent/src/extensibility/extensions/wrapper.ts:147-203` |
| 4. Immediate steering | User needs to change direction during a long tool batch | Streaming/tools -> interruptible tools aborted -> queued steer injected | Current component updates; remaining tools are skipped | Agent resumes from new instruction | OMP: `packages/agent/src/agent.ts:874-887`; `packages/agent/src/agent-loop.ts:1770-1887` |
| 5. Esc interruption | User stops current work | Streaming -> aborted while draft/queue is preserved | Redundant visible `Interrupted by user` transcript text was removed | `.` or `c` sends a hidden continuation directive; queued input can be restored | OMP: `packages/coding-agent/src/modes/controllers/input-controller.ts:268-380`, `620-635`, `1341-1404`; `packages/coding-agent/CHANGELOG.md:3335-3339` |
| 6. Resume | Process ended or user returns later | Session list -> selected session -> open/fork/cancel | Full-screen searchable picker, current/all-project scopes, lifecycle statuses | Cross-project sessions are moved/forked explicitly; missing paths degrade gracefully | OMP: `packages/coding-agent/src/main.ts:1269-1326`; `packages/coding-agent/src/cli/session-picker.ts:9-92`; `packages/coding-agent/src/modes/components/session-selector.ts:24-44` |
| 7. Print-mode error | Headless task fails or aborts | Running -> stderr + non-zero exit | `Working...` is emitted once; final text or error is separated from progress | Caller handles exit status and may rerun interactively | OMP: `packages/coding-agent/src/modes/print-mode.ts:179-249` |

### Advanced and power-user use

| Entry | Contract | Why it matters | Evidence |
| --- | --- | --- | --- |
| `omp -p/--print` | Process prompt and exit; piped input auto-selects print mode unless a protocol mode owns stdin | Automation has a predictable non-interactive contract rather than pretending to be the TUI | OMP: `packages/coding-agent/src/commands/launch.ts:86-98`; `packages/coding-agent/src/main.ts:1166-1174`; `packages/coding-agent/src/modes/print-mode.ts:179-249` |
| `omp -c/--continue` | Continue the recent session | Recency continuation is explicit at CLI entry | OMP: `packages/coding-agent/src/commands/launch.ts:90-93`; `packages/coding-agent/src/main.ts:759-779` |
| `omp -r/--resume` | Resolve ID/path or open a full-screen picker when omitted | Recovery and selection are not hidden inside bare launch | OMP: `packages/coding-agent/src/commands/launch.ts:94-97`; `packages/coding-agent/src/main.ts:1269-1326` |
| `--no-session`, `--session-dir`, `--fork` | Choose ephemeral, alternate storage, or explicit branch semantics | Session durability is a user-visible mode | OMP: `packages/coding-agent/src/commands/launch.ts:98-103`; `packages/coding-agent/src/main.ts:667-779` |
| `--approval-mode`, `--auto-approve` | Select `always-ask`, `write`, or `yolo` for this run | Governance is explicit and available in automation | OMP: `packages/coding-agent/src/commands/launch.ts:162-177`; `packages/coding-agent/src/tools/approval.ts:13-38` |
| Plan approval overlay | Approve/execute, compact, preserve context, select execution model, or refine | A consequential transition owns a dedicated review surface | OMP: `packages/coding-agent/src/modes/interactive-mode.ts:3503-3661` |
| Extensions and plugins | Explicit paths load extension packages; `--no-extensions`, `--no-skills`, and `--no-rules` narrow discovery for the run | Extensibility has explicit entry/disable contracts, and one extension root may contribute skills, hooks, tools, commands, rules, prompts, and MCP configuration | OMP: `packages/coding-agent/src/commands/launch.ts:129-149`; `packages/coding-agent/src/main.ts:1035-1057`, `1125-1141` |
| Sub-agents | The governed `task` tool spawns one agent or a batch, optionally asynchronous, bounded by a per-session concurrency semaphore; Agent Hub exposes live and persisted children | Delegation has one mutable task block and a dedicated observation/focus surface instead of flattening every child event into the main transcript | OMP: `packages/coding-agent/src/task/index.ts:476-535`; `packages/coding-agent/src/modes/controllers/selector-controller.ts:1731-1802` |
| `workflowz` | A standalone lowercase prose keyword appends a hidden notice that asks the model to construct a deterministic multi-subagent workflow through the active task schema | The operational mechanism is the hidden structured steering notice; the colored keyword highlight is only discoverability/styling | OMP: `packages/coding-agent/src/modes/workflow.ts:7-48` |
| Tools, LSP, PTY, model roles | CLI flags disable/filter tools, LSP, PTY and select role models | Power controls remain explicit rather than overloading normal composer state | OMP: `packages/coding-agent/src/commands/launch.ts:104-160` |

## OMP glyph and compact status language

### Architecture

OMP defines semantic keys first, maps them to one of three symbol presets, and
lets renderers request meaning rather than hard-code a literal. The status
contract includes success, error, warning, info, pending, disabled, enabled,
running, shadowed, aborted, and done. Separate keys cover navigation, trees,
checkboxes, radio choices, formatting, icons, and tool identity (OMP:
`packages/coding-agent/src/modes/theme/theme.ts:29-240`).

The presets are `unicode`, `nerd`, and `ascii`; status and activity have separate
spinner families (OMP: `packages/coding-agent/src/modes/theme/theme.ts:970-999`).
The active preset can be changed at runtime and causes a theme reload rather
than requiring renderer-specific rewrites (OMP:
`packages/coding-agent/src/modes/theme/theme.ts:2332-2351`).

This is an operational contract because producers normalize state and shared
formatters map it into symbols. It is not just a bag of decorative icons (OMP:
`packages/coding-agent/src/tools/render-utils.ts:144-180`).

### Representative inventory

| Semantic key/family | Unicode literal | ASCII fallback | Triggering state | Main consumers and treatment | Stability |
| --- | --- | --- | --- | --- | --- |
| Success | `✔` | `[ok]` | Tool/session/action completed successfully | Success color; terminal, non-animated | Stable semantic key |
| Error | `✘` | `[!!]` | Tool/session/action failed | Error color; terminal, non-animated | Stable semantic key |
| Warning | `⚠` | `[!]` | Interrupted, risky, degraded, or warning state | Warning color; non-animated | Stable semantic key, contextual copy |
| Pending | `⏳` | `[*]` | Work accepted but not running/settled | Muted pending row or preview | Stable semantic key |
| Running | `⟳` or spinner frame | `[~]` or `|/-\\` frames | Live tool/status activity | Accent color; animation only when component declares it useful | Stable semantic key, contextual animation |
| Aborted | `⏹` | `[-]` | User/system interruption | Error or muted treatment depending on surface | Stable semantic key |
| Done/neutral terminal | `•` | `*` | Compact completion without full success emphasis | Success/muted depending on renderer | Stable semantic key |
| Checkbox | `☑` / `☐` | `[x]` / `[ ]` | Multi-select answer state | Success for checked, dim for unchecked | Stable selection contract |
| Radio | `◉` / `○` | `(o)` / `( )` | Single-select answer state | Success for selected, dim for unselected | Stable selection contract |
| Expand/collapse | `▸` / `▾` | `+` / `-` | Hidden/visible detail | Navigation color; static | Stable navigation contract |
| Cursor/selected | `❯` / `➤` | `>` / `->` | Current list row or committed selection | Accent color; static | Stable navigation contract |
| Tree | `├─`, `└─`, `│` | `|--`, `'--`, `|` | Parent/child hierarchy | Structural, usually muted | Stable layout contract |
| Tool and domain icons | Examples include model, folder, git, task, ask | Text abbreviations such as `[M]`, `[D]`, `>>>`, `[?]` | Identifies object/tool kind, not outcome | Color and meaning depend on consumer | Contextual/decorative unless paired with status |

Literal mappings come from OMP:
`packages/coding-agent/src/modes/theme/theme.ts:244-363` and
`packages/coding-agent/src/modes/theme/theme.ts:764-882`.

### Producer-to-renderer traces

1. **Tool lifecycle**

   ```text
   tool_execution_start/update/end
     -> pending ToolExecutionComponent keyed by toolCallId
     -> ToolUIStatus
     -> formatStatusIcon(status, theme, spinnerFrame)
     -> status glyph + color in one mutable tool block
   ```

   Source: OMP: `packages/coding-agent/src/modes/controllers/event-controller.ts:940-1076`,
   `packages/coding-agent/src/tools/render-utils.ts:144-170`.

2. **Grouped reads**

   ```text
   read tool call with filesystem/external target
     -> readArgsCollapseIntoGroup
     -> one ReadToolGroupComponent for the run
     -> pending entries updated by call ID
     -> compact settled summary, expandable details
   ```

   Source: OMP: `packages/coding-agent/src/modes/components/read-tool-group.ts:29-40`,
   `packages/coding-agent/src/modes/components/chat-transcript-builder.ts:186-217`,
   `329-350`.

3. **Session lifecycle**

   ```text
   complete/interrupted/aborted/error/pending
     -> formatSessionStatus
     -> theme.status.success/warning/aborted/error/pending
     -> searchable resume row
   ```

   Source: OMP: `packages/coding-agent/src/modes/components/session-selector.ts:24-44`.

4. **Approval/question selection**

   ```text
   question.multi + selectedOptions
     -> checkbox or radio semantic key
     -> selected/success or unselected/dim treatment
     -> stable-height dialog row
   ```

   Source: OMP: `packages/coding-agent/src/modes/components/ask-dialog.ts:282-305`,
   `329-455`.

5. **Animation**

   ```text
   renderer opts into animated pending/partial state
     -> component computes needsSpinner
     -> shared preset frame
     -> component-scoped repaint
     -> interval stops and frame clears at terminal state
   ```

   Source: OMP: `packages/coding-agent/src/modes/components/tool-execution.ts:642-712`.

OMP deliberately removed animated pending borders from common execution blocks,
showing that motion is treated as a liveness signal rather than a universal
decoration (OMP: `packages/coding-agent/CHANGELOG.md:3350-3353`).

## Historical Carina comparison at an unpinned baseline

### What the audit snapshot appeared to contain

At that unpinned snapshot, the following mechanisms appeared to exist. These
bullets are historical observations and must be rechecked before reuse:

- The primary transcript filters several routing/runtime/audit events and uses
  typed presentations for agent, tool, command, file, context, governance,
  subagent, workflow, and system output (Carina: `go/tui/model.go:508-547`,
  `go/tui/transcript.go:17-83`).
- Default Enter is already “submit or steer”; a running task routes to
  `task.steer` (Carina: `go/tui/keymap.go:227-235`,
  `go/tui/update.go:1067-1166`, `go/tui/update.go:1307-1317`).
- Follow-up queueing and lossless recall appeared in the snapshot (Carina:
  `go/tui/followup_flow.go:15-58`). The gap is visibility and hierarchy, not
  missing steering semantics.
- Approval and question overlays already preserve durable decision/question
  IDs, serialize concurrent prompts instead of replacing them, and resolve
  through daemon RPCs. Reconnect tracking replays unresolved requests without
  reopening resolved ones (Carina: `go/tui/approval.go:18-83`,
  `go/tui/approval.go:131-219`, `go/tui/question.go:22-78`,
  `go/tui/question.go:116-192`, `go/tui/conn.go:118-203`,
  `go/tui/conn.go:220-265`). The opportunity is stronger hierarchy, stable
  layout, multi-question semantics, and shared waiting-state presentation.
- The in-app session picker exposed activity/outcome/progress, recovery
  disposition, interruption certainty, billing uncertainty, checkpoint IDs,
  and proof flags. It also appeared to prioritize sessions requiring attention
  (Carina: `go/tui/session_picker.go:16-45`,
  `go/tui/session_picker.go:139-187`, `go/tui/session_picker.go:318-351`).
- Provider selection appeared to enforce the recorded trust boundary. Auto mode
  chooses the router only when a configured provider is runnable; Claude CLI
  and Codex CLI are selected only by explicit configuration (Carina:
  `go/daemon/reasoner.go:101-129`, `go/daemon/daemon.go:687-698`).

### Historical Carina journey trace

| Stage | Recorded state transition and owner | Recorded user-visible result | Historical assessment |
| --- | --- | --- | --- |
| Bare launch | `tuiapp` ensures the daemon is reachable, then selects the latest pending-submission session, last active session, or newest workspace session before constructing the TUI | Continuity is convenient but implicit; no provider-readiness gate precedes the composer | Keep continuity, add an explicit readiness state (Carina: `go/tuiapp/tuiapp.go:115-168`, `171-209`) |
| Compose/submit | Idle Enter creates a task; running Enter submits `task.steer`; Tab queues a later turn | Steering and follow-up semantics exist, but the composer does not visibly explain which state it is in | Improve mode/state feedback rather than reimplement queue semantics (Carina: `go/tui/keymap.go:227-236`, `go/tui/update.go:1067-1166`, `go/tui/followup_flow.go:15-58`) |
| Provider failure | The daemon checks `d.reasoner`; absence immediately produces a durable degraded terminal result | The missing provider is learned after submission, while footer/model chrome may still imply `default` and `ready` | Move readiness discovery before ordinary submission (Carina: `go/daemon/agent.go:151-170`, `go/daemon/agent.go:1578-1583`) |
| Tool/model activity | Audit/transient events become typed transcript presentations; some lifecycle events update by key | Filtering has improved, but routine successful tool/file/model rows still dominate the screenshot | Reclassify permanent vs grouped vs ephemeral content (Carina: `go/tui/model.go:508-547`, `go/tui/transcript.go:404-416`, `571-684`, `773-787`) |
| Approval | `permission.request` opens an ID-bound overlay; later requests queue; allow/deny resolves through the daemon and durable resolution advances the queue | Governance is robust, but waiting state is not normalized across footer, rail, transcript, and notification | Preserve authority and queueing; unify status and refine layout (Carina: `go/tui/approval.go:18-83`, `85-123`, `148-219`) |
| Question | `user.question` opens an ID-bound option/free-text overlay; answers call `task.user.answer`; reconnect reconciliation suppresses stale prompts | Single questions and recovery work; OMP offers richer multi-question/preview/selection semantics | Adapt richer question presentation without changing daemon ownership (Carina: `go/tui/question.go:22-78`, `80-192`, `go/tui/conn.go:118-203`) |
| Terminal outcome | Daemon emits `task.completed` with the real status; task rail, attention layer, follow-up flow, and footer each reduce it separately | `Task finished`, `degraded`, and `ready` can coexist | Replace independent reductions with one conversation outcome projection (Carina: `go/daemon/notify.go:10-41`, `go/tui/taskgraph.go:62-85`, `go/tui/attention.go:20-27`, `go/tui/followup_flow.go:218-266`) |
| Session recovery | Session picker ranks recovery needs and displays proofs; checkpoint picker previews, restores, pauses, and explicitly resumes a task | The snapshot exposed more recovery fields than the reviewed OMP paths; this was not a user-quality comparison | Recheck the contract, then retain its safety semantics if the current source still matches (Carina: `go/tui/session_picker.go:139-187`, `318-351`; `go/tui/checkpoint_picker.go:13-43`, `78-205`) |
| Configuration | `/settings` opens a tabbed control shell; `/model` opens the model picker; approval modes are explicit product settings | Configuration exists after entry, but does not prevent false readiness | Surface the relevant subset during pre-composer readiness (Carina: `go/tui/product_shell.go:37-109`, `go/tui/model_picker.go:119-181`) |

### Historical comparison hypotheses

| Problem | OMP | Recorded Carina snapshot | Historical hypothesis |
| --- | --- | --- | --- |
| Readiness before composer | Setup scenes explain provider/model/glyph/theme before normal use; submission revalidates | Bare launch resolved a session and opened TUI; no shared readiness state prevented `ready` with no reasoner | The source shapes suggested an OMP discoverability advantage, but this was not user-tested |
| Conversation hierarchy | Message-oriented transcript; routine reads group; progress mutates in place | Typed event projection still permanently rendered many successful tool/file/model events | The snapshot suggested an architectural gap, not only styling |
| Outcome consistency | Components consume normalized semantic statuses | Transcript, task rail, footer, notification, and picker derived status independently | The snapshot suggested this as a high-priority gap |
| Steering | Enter steers, Ctrl+Enter follows up, queue state is explicit | Enter steers and Tab queues; behavior exists | Adapt only the visibility and queue feedback |
| Approval/question UI | Dedicated stable overlays, rich selection markers, multi-question support | Durable ID-bound overlays and reconnect-safe queues appeared to exist, while question richness and cross-surface waiting state looked weaker | The historical proposal was to adapt presentation while retaining Carina policy/RPC authority |
| Session recovery | Searchable picker with lifecycle statuses and cross-project handling | The snapshot exposed more recovery fields and prioritization | Preserve authority only after current-source verification; picker ergonomics remained a hypothesis |
| Glyph compatibility | Central semantic registry, Unicode/Nerd/ASCII presets, live preview | Five local status helpers with Mono fallback; task rail reuses them | Adopt semantic registry, adapt literals to Carina |
| Animation | Selective, component-scoped, renderer opt-in | Static status helpers and broader layout updates | Adapt only where liveness is otherwise ambiguous |

At that snapshot, Carina's glyph vocabulary was compact but incomplete: success `✓`, auth
`⚿`, failure `✗`, neutral `·`, running `›`; Mono fallback `+`, `!`, `x`, `-`,
`>` (Carina: `go/tui/transcript.go:267-301`). The task rail reuses these
helpers, which is good reuse, but the helpers do not constitute a complete
semantic registry for pending, warning, interrupted, degraded, selected,
expanded, disabled, or compatibility presets (Carina:
`go/tui/taskgraph.go:267-302`).

## Provider and CLI onboarding assessment

The audit hypothesized that OMP's *onboarding surface* was more discoverable
because provider sign-in and model selection happen before ordinary composer
use and failure recovery is contained inside those scenes. That comparison was
not user-tested and the Carina baseline was not pinned. OMP does not establish
a better authority rule for Carina to copy; its setup scenes can be skipped and
submission validation still remains necessary afterward.

The historical Carina adaptation proposal was:

1. Compute `readiness` from the same provider catalog/auth/runtime checks used
   by daemon reasoner selection.
2. Show a setup/readiness surface before an ordinary composer can claim
   `ready`.
3. List only explicitly configured and runnable providers/models as selectable
   implicit choices.
4. Offer Claude CLI, Codex CLI, Mox, and other compatibility adapters only in
   an explicit “external reasoner” configuration path.
5. Never infer preference or permission from `PATH`, binary discovery, or an
   adapter being installed.

This preserves the source-backed provider-first behavior in
`go/daemon/reasoner.go:116-129` and `go/daemon/daemon.go:687-698`.

## Adoption matrix

| Verdict | Mechanism | User problem solved | Required Carina boundary | Main risks | Validation |
| --- | --- | --- | --- | --- | --- |
| adopt | Shared conversation/readiness reducer | `ready`, `finished`, and `degraded` cannot contradict each other | Normalize provider readiness plus task active/waiting/terminal outcome once; transcript, rail, footer, notifications, and session picker consume that projection | Migration can hide useful audit facts or mishandle replay | Reducer table tests for no-provider, running, waiting approval, success, degraded, failed, interrupted, reconnect, and replay; golden tests assert all surfaces agree |
| adopt | Readiness/setup surface before normal composer | Missing provider is discovered before the user submits | Add a TUI state driven by daemon provider/model availability, with explicit configure/select/retry actions | Startup can become blocking or overly modal | Launch with zero/one/multiple providers; verify no false `ready`; verify escape path does not imply runnable state |
| adopt | Conversation-first transcript policy | Routine events no longer dominate intent and answer | Define permanent conversation, grouped activity, ephemeral activity, and audit-only classes; keep raw audit unchanged | Over-collapsing can hide failures or governance | Snapshot realistic sessions; every failure/denial/question remains visible; repeated successful reads/lists collapse; final answer remains first-order |
| adopt | Semantic glyph registry with compatibility presets | Status meaning is consistent and terminals render safely | Central semantic keys consumed by transcript, task rail, footer, pickers, approvals, and trees; Unicode and ASCII are required, Nerd Font optional | Width differences and color-only ambiguity | Width tests in supported terminals; NO_COLOR/Mono snapshots; live preview; every state has text or shape distinction beyond color |
| adopt | Explicit interactive/print/resume/continue entry contracts | Automation and recovery do not depend on hidden bare-launch behavior | Document and implement distinct CLI dispatch paths while preserving daemon/session authority | Flag compatibility and ambiguous bare launch | CLI contract tests for TTY/non-TTY/piped input, missing session, continue, resume picker, and exit codes |
| adapt | Grouped reads and routine successful tools | Transcript stops becoming a permanent activity ledger | Group by turn/tool family/call IDs; failures, writes, diffs, approvals, and user-relevant outputs remain first-class | Grouping may obscure which file caused a later failure | Expand/collapse tests; group counts and paths are deterministic; audit jump identifies original event IDs |
| adapt | In-place activity components | Progress reads as one evolving action instead of repeated rows | Mutable TUI projection keyed by stable call/task IDs; append-only audit remains unchanged | Reconnect/replay may duplicate or freeze components | Replay and reconnect produce the same final projection as live streaming; unseen counters do not inflate on updates |
| adapt | Richer approval/question presentation | Waiting states become obvious and ergonomic without replacing existing durable overlays | Extend current ID-bound overlays with stable sizing, semantic option markers, previews, and multi-question grouping; UI continues to consume Carina governance events and never decides authorization | Modal focus loss, timeout ambiguity, policy/UI divergence | Keyboard/mouse/resize/timeout/cancel tests; reconnect never reopens resolved IDs; approval result is durable before tool execution continues |
| adapt | OMP session picker ergonomics | Resume/search is faster without losing safety evidence | Improve full-screen search, scope switching, and lifecycle glyphs while retaining Carina continuity proofs and recommended recovery action | Simpler rows may hide billing/recovery uncertainty | Recovery evidence remains visible for selected row; priority ordering and cross-project behavior stay covered |
| adapt | Selective component-scoped animation | Operator can distinguish live from stalled work without visual noise | Opt-in animation on a small set of active components; reduced-motion/static mode; stop intervals at terminal states | Repaint cost, flicker, inaccessible motion | Frame lifecycle tests, CPU/render benchmark, terminal resize tests, reduced-motion snapshot |
| reject | Copy OMP branding, literal identity, or visual personality | Does not solve state ambiguity | None | Brand confusion, licensing, inconsistent Carina identity | Design review should reject direct copying |
| reject | Auto-select Claude/Codex/Mox from installed binaries | Appears convenient but violates explicit provider authority | None; keep explicit adapter configuration | Surprise execution, credential leakage, inconsistent environments | Tests assert binary presence alone never changes selected backend |
| reject | Animate every running state | Adds motion without adding information | None | Jank, scrollback churn, attention fatigue | Review should require a named ambiguity each animation solves |
| reject | Permanent transcript row for every successful low-level event | Preserves audit detail at the cost of conversation legibility | Keep these facts in audit/replay, not primary conversation | User cannot find intent, answer, or failure | Transcript density budget and screenshot review |
| reject | Replace Carina recovery evidence with recency-only resume | Would discard a stronger existing contract | None | Unsafe resume, hidden interruption/billing uncertainty | Continuity proof tests remain mandatory |

## Prioritized implementation sequence

This analysis does not authorize implementation. A separate reviewed task should
sequence the work as follows:

1. **Define the state contract.** Specify `readiness`, `activity`, `attention`,
   and terminal `outcome` as one replayable reducer over daemon/runtime events.
2. **Make surfaces agree.** Move footer, task rail, attention notifications,
   session rows, and transcript terminal headers onto that reducer before
   changing appearance.
3. **Add provider readiness UX.** Prevent `ready` and `model:default` when no
   runnable provider/model exists; expose configure/select/retry actions.
4. **Reclassify transcript content.** Keep user/assistant/governance/failure
   history primary; group routine successful reads/lists and make transient
   progress mutable.
5. **Introduce semantic glyph tokens.** Add compatibility presets and a preview,
   then migrate existing consumers without changing outcome semantics.
6. **Improve overlays and recovery navigation.** Adapt OMP's stable approval,
   question, and picker ergonomics while preserving Carina's durable policy and
   continuity proofs.
7. **Add selective motion last.** Animation is only useful after state ownership
   and transcript hierarchy are correct.

The sequence matters. Changing glyphs or adding spinners before unifying status
would make contradictory state more polished, not more understandable.

## What the preceding Codex CLI work failed to learn from OMP

The preceding Codex CLI work solved a backend consistency issue: Codex could be
an explicit reasoner adapter just as Claude CLI could, without allowing either
binary to become an automatic default. That was necessary, but it examined the
provider protocol and not the product journey.

It therefore missed six OMP lessons:

1. Provider/model readiness should be explained before ordinary composition,
   not discovered only after submission.
2. Interactive, print, continue, and resume are distinct user contracts, not
   merely flags routed into one ambiguous experience.
3. The transcript should organize a conversation and mutate activity in place;
   the audit log can retain every operational fact separately.
4. Status glyphs should be semantic tokens with compatibility fallbacks, not
   local literals attached independently to each surface.
5. Approvals, questions, interruption, and plan transitions deserve dedicated
   interaction surfaces with explicit recovery actions.
6. A terminal outcome must have one owner. `Task finished`, `degraded`, and
   `ready` cannot all be valid descriptions of the same moment.

The practical correction is not “make Carina look like OMP.” It is “give
Carina one coherent journey and one coherent status language while preserving
its stronger provider authority, auditability, and recovery evidence.”
