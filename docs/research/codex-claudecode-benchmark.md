# Codex / Claude Code Benchmark — Final 7 Open Items

Sources reviewed:

1. `openai/codex` source, `codex-rs/` (compaction, state/SQLite migrations,
   agent roles, config-layer/constraint system, plugin marketplace, tool
   search, image content blocks, skills).
2. Claude Code official docs + changelog/whats-new evolution
   (code.claude.com/docs, changelog.md, ~40 point releases spanning roughly
   Jan–Jul 2026).
3. Codex CLI docs (developers.openai.com/codex, learn.chatgpt.com/docs/codex —
   several fetches ECONNRESET'd and fell back to WebSearch corroboration,
   noted per-item below).
4. A local Claude Code source-analysis notes collection
   (`claude-code-notes`, reverse-engineered from actual product internals —
   valuable specifically where it documents an *older or internal* shape
   that differs from the current public docs, which is itself evidence of
   evolution rather than noise).

Carina absorbs mechanisms only when they preserve the daemon, capability
kernel, and append-only audit log as the single authority: additive-only
diffs, kernel-gated seams (nothing bypasses `PluginLoad`/capability checks),
tighten-only precedence (project/repo-sourced config can only narrow, never
widen, what an operator already granted), and no unbounded cost or
concurrency without an explicit gate. This benchmark runs those four sources
against the final 7 open items in `absorption-plan.md`'s tracking checklist.
Every item below was originally seeded as "adopt" or "adopt-now" by the
initial research pass; an adversarial review pass then re-checked each
against carina's actual current code state (not just the design doc) and,
in three of seven cases, downgraded the verdict after finding a real
specification gap or an active same-seam concurrent-work collision. Those
downgrades are recorded verbatim below because the reasoning is itself part
of the record — "the idea is sound but the landing window or the spec isn't
ready" is a different kind of finding than "reject," and conflating the two
would lose information a future pass needs.

Outcome for this pass: **0 of 7 items reached commit.** 4 reached
`design_only` (an architecture/interface decision is recorded, no code
lands), and 3 reached `defer` (downgraded from `adopt` by adversarial
review after a design claim didn't hold up against actual current code).
No item was rejected
outright — all seven are real, externally-corroborated gaps; none were
found to be a poor architectural fit for carina. See Trade-offs at the end
for why a 0-commit pass is still a productive outcome for this campaign.

---

## Design-only

Architecturally validated by two-plus independent sources, with a concrete
Go-shaped design recorded, but code deliberately withheld this pass because
of a genuine prerequisite gap (a Go abstraction that doesn't exist yet) or
because the natural landing file is a hot, actively-churned seam this task
was instructed not to touch.

### Versioned idempotent config/state schema migration

**Codex approach.** Codex (`codex-rs/state`) uses real SQLite with the
`sqlx::migrate!` macro, not a hand-rolled version field: five separate
`Migrator` statics (`STATE_MIGRATOR`, `LOGS_MIGRATOR`, `GOALS_MIGRATOR`,
`MEMORIES_MIGRATOR`, `THREAD_HISTORY_MIGRATOR`), each backed by a numbered
`.sql` ladder (`0001_threads.sql` … `0040_threads_history_mode.sql`, 40
migrations observed), forward-only and checksummed via sqlx's
`_sqlx_migrations` table, applied automatically on `StateRuntime::init()`
before any query runs. `runtime_migrator()` sets `ignore_missing: true` so
an older binary can open a DB a newer binary already migrated further —
known versions are still checksum-validated; only "DB is ahead of me" is
relaxed. Breaking schema changes bump the *filename* itself
(`state_5.sqlite`, `logs_2.sqlite`, `goals_1.sqlite`), so old and new
binaries with incompatible schemas simply open different files and never
corrupt each other. Migration `0039` shows additive-only discipline: new
columns get `DEFAULT`, are backfilled via `UPDATE`, plus an `AFTER INSERT`
trigger so an older binary inserting rows without the new columns still
gets sane values. Corruption handling is a separate, composable concern:
`state/src/runtime/recovery.rs` detects SQLite corruption codes and
`backup_runtime_db_for_fresh_start()` moves only the affected DB file (plus
WAL/SHM sidecars) into a `db-backups/` directory — never deletes — letting
the caller rebuild fresh, per-database, so one corrupt file doesn't take
down goals/memories/logs together. WebSearch corroborated: a shared
`CODEX_HOME` across WSL/Windows builds produces "migration N was previously
applied but has been modified" (checksum mismatch, fail-closed), and that
"corrupted SQLite state databases are backed up and rebuilt automatically
from rollout data" — the SQLite state DB is a derived/rebuildable index over
append-only JSONL rollout files, the actual source of truth, which carries
no explicit schema-version field at all (mostly-additive fields tolerate
old readers by construction).

**Claude Code approach.** No `STATE_VERSION` ladder for either persistence
layer, and the two layers are treated differently. (1) Session transcripts
(`~/.claude/projects/<project>/<session-id>.jsonl`): `sessions.md` states
outright that "the entry format is internal to Claude Code and changes
between versions, so scripts that parse these files directly can break on
any release. To build on session data, use `/export` or the script
interfaces instead" — Anthropic deliberately declines to make the raw JSONL
a versioned/stable contract; compatibility is achieved by the format being
append-only/mostly-additive plus pushing external consumers to a stable API
surface (`/export`, `--output-format json`, `transcript_path` in hooks, the
Agent SDK) instead of a migrate-on-read mechanism. Retention is time-based
(`cleanupPeriodDays`, default 30 days), not version-based. (2) Local
settings/config migrations (per `claude-code-notes`,
`08-constants-types-migrations.md`, `src/migrations/`): a small set of
named, idempotent, self-guarding functions (`migrateSonnet45ToSonnet46`,
`migrateAutoUpdatesToSettings`, etc.) run unconditionally at every startup
via `setup.ts`. Each inspects the current data shape/value with guard
clauses (provider type, subscription tier, exact old model string) and
either transforms or no-ops — explicitly **no** schema-version field or
migration-history table. The notes give the rationale verbatim: a
version-number list requires its own persistence and corruption-handling
burden, while state-shape/value inspection is robust to manually-edited
files and settings synced across machines, and idempotency alone (not a
run-once ledger) guarantees safety on repeat execution. Checkpointing/
rewind evolved iteratively through the changelog with no format-version
bump ever mentioned: shipped as a "research preview" under auto mode (Week
13, ~v2.1.83), gained "Summarize up to here" (Week 20, v2.1.139–142), then
gained resume-past-`/clear` support (Week 26, v2.1.191) — each addition
purely additive (new UI entry points, new SDK option
`enable_file_checkpointing`). The Agent SDK `SessionStore` adapter
(`session-storage.md`) formalizes this further: append/load treat entries
as opaque JSON-safe values, dedup is by `entry.uuid` (not schema version),
and the docs warn "a retried batch can re-deliver entries that already
landed... deduplicate by `entry.uuid`" — durability and idempotency
substitute for versioning at the wire-contract level too.

**Best-practice synthesis.** Both agree on a deeper principle despite
opposite surface mechanisms: never let a store's own internal version tag
be the sole safety net, and never silently discard on mismatch. Codex needs
a real migration ladder because it uses SQLite with actual DDL across 40+
increments feeding structured queries — schema shape genuinely needs
forward migration — so it adopts checksummed migrations plus
belt-and-suspenders design (new filename generation for breaking changes,
`DEFAULT`+trigger for additive changes, SQLite as a rebuildable derived
index over an append-only JSONL source of truth). Claude Code has
genuinely simple, flat, append-only stores where the dominant risk isn't
"the shape changed and old readers choke," it's "a stale version tag either
falsely blocks a compatible file or falsely trusts an incompatible one" —
its answer is to avoid the version tag entirely for transcripts and replace
version-gating with idempotent, self-guarding, value-inspecting migration
functions for config. For a Go-daemon-with-JSON-files context like carina —
much closer to Claude Code's shape than Codex's SQL shape — the synthesis
is: don't build a generic `STATE_VERSION` ladder/registry (carina has no
SQL DDL and no cross-store relational shape to migrate); do close the real
gap, which is that carina's current per-file "`Version int`; stamp 1;
require `== 1` on read" pattern is fail-closed in the wrong direction — a
version bump silently *drops* the whole file's data instead of migrating
forward or quarantining the old file the way Codex's `recovery.rs` does;
combine Codex's quarantine-not-delete instinct with Claude Code's
idempotent-transform instinct: on version mismatch, rename the old file
into a sidecar rather than treating it as absent, and support small, named,
idempotent per-field upgrade functions for any store that actually needs a
cross-version field rename — not a schema/DDL migration system.

**Carina's response (design_only).** Adopt-and-code-now is too strong: the
seed's implied shape (a general `STATE_VERSION` ladder) is not what either
external source actually recommends once you look past the surface —
Codex's sqlx ladder answers a relational-DDL problem carina doesn't have,
and Claude Code explicitly rejected version-tag migration in favor of
idempotent value-inspection for exactly carina's kind of flat-JSON store.
Reject-outright is too weak: there is a real, concrete defect
(`usage.go` silently discarding recorded model-usage data on any future
version bump — fail-closed on read but fail-open on data loss, proceeding
with an empty store rather than refusing to start or preserving the file)
that violates carina's own tighten-only ethos, already flagged in
`absorption-plan.md` as blocking later schema-evolution waves.
`design_only` is correct specifically because (1) this pass is scoped to
no-code; (2) the right primitive (idempotent value-inspecting upgrades, not
a version registry) only becomes provably correct once there is a real
second-version consumer to test against, so locking the API now is
premature; (3) even a small, additive touch to `usage.go`'s read path sits
downstream of `daemon.go`'s construction sequence and deserves a dedicated
reviewed PR, not a research-pass side effect. Planned shape for that PR:
quarantine-not-delete on version mismatch (rename to a `.v<N>.bak` sidecar,
never silently drop), plus small named idempotent upgrade functions
per-field, mirroring Claude Code's `src/migrations/` pattern rather than a
generic ladder.

### Coordinator restricted-orchestrator permission role

**Codex approach.** Codex's `agent_roles` mechanism
(`codex-rs/core/src/agent/role.rs`,
`codex-rs/core/src/config/agent_roles.rs`) is a config-layer overlay
system, not a permission-capability system. `apply_role_to_config()`
resolves a named role (built-in: default/explorer/worker, formerly
`awaiter`) or a user TOML file, merged as a high-precedence
`ConfigLayerEntry` on top of the spawned agent's `Config` — it can set
model, reasoning effort, service tier, `developer_instructions`,
`background_terminal_max_timeout`, etc. There is no `AgentRoleConfig` field
for tool/capability restriction; the closest lever is `sandbox_mode` inside
a role's config TOML (confirmed via WebSearch/community docs: "an explorer
that should never write files gets `sandbox_mode = \"read-only\"` in its
role config"), piggybacking on Codex's existing per-turn sandbox policy
rather than a dedicated coordinator-only capability. Exec-policy
inheritance is separate machinery
(`inherited_exec_policy_for_source()`/`child_uses_parent_exec_policy()` in
`agent/control.rs`) — general policy plumbing, not a coordinator/worker
distinction. `codex-rs/agent-graph-store` (parent/child thread-spawn
topology) and `collaboration-mode-templates` (Default/Plan/Execute/
PairProgramming prompt templates) were both read from source and confirmed
to contain no role/permission-scoping concept for multi-agent delegation.
Community docs (`developers.openai.com/codex/subagents`) corroborate:
subagents inherit the parent's sandbox/approval mode by default, and this
area is actively unstable (GitHub issues show custom-role regressions on
Windows/Ubuntu, `agent_type` dropped from the spawn-tool schema in some
builds). No built-in "coordinator that can only spawn, nothing else" role
exists in Codex.

**Claude Code approach.** Claude Code has the exact mechanism this item
asks for, and ships an explicit worked example literally named
"coordinator" in official docs (`sub-agents.md`, "Restrict which subagents
can be spawned"):

```yaml
---
name: coordinator
description: Coordinates work across specialized agents
tools: Agent(worker, researcher), Read, Bash
---
```

General mechanism: subagent frontmatter has `tools` (allowlist) and
`disallowedTools` (denylist, applied first); omitting `Agent` from `tools`
means that subagent cannot spawn anything. The inverse — "can spawn, but
only specific types" — uses `Agent(type1, type2)` parenthesized-allowlist
syntax. A pure coordinator (only `Agent(...)`, no Write/Edit/Bash) is fully
expressible today by simply omitting those tools. Depth is capped at 5
levels, enforced at spawn time, not just documented. At the team level
(`agent-teams.md`) there's a *structural*, non-overridable version of the
same idea: "No nested teams: teammates cannot spawn their own teammates.
Only the lead can manage the team." Dynamic workflows (`workflows.md`) push
this into a different layer entirely: "No direct filesystem or shell access
from the workflow itself... Agents read, write, and run commands. The
script coordinates the agents" — the orchestrator (a JS script) is
structurally incapable of touching files, only spawning/awaiting `agent()`
calls, runtime-enforced rather than list-enforced.

Changelog evolution (the load-bearing finding): the `Task` tool was renamed
to `Agent` at v2.1.63. v2.1.172 added nested-subagent spawning up to a
fixed depth-5 limit (previously unbounded — a *tightening*) and applied
`availableModels` restrictions to subagent model overrides. v2.1.178 added
general `Tool(param:value)` pattern matching (e.g. `Agent(model:opus)` to
block a model tier in subagent spawns) and enforced MCP server-level
`disallowedTools` patterns for subagents. **v2.1.186 is the key hardening
commit for this exact item**: it introduced `Agent(type)` deny rules and —
critically — fixed a real bug where `Agent(x,y)` allowed-types restriction
was **not** being enforced for named-subagent-initiated spawns (only
enforced for main-thread spawns until then). That is a directly on-point
cautionary precedent: Anthropic shipped a coordinator's spawn-scoping, then
discovered it wasn't binding at every delegation hop, and had to patch it.
v2.1.207 added auto-mode classifier evaluation of subagent spawns before
launch. Net story: Anthropic converged on "restriction expressed via a
tools allowlist on an ordinary subagent definition" rather than a dedicated
coordinator agent type baked into the product — but had to harden it once
to cover nested spawns correctly. Separately, the local `claude-code-notes`
collection documents a materially *stronger*, feature-gated internal
"coordinator mode" (`CLAUDE_CODE_COORDINATOR_MODE=1`): a whole-session
persona whose own tool surface is hard-limited to ~6 control-plane tools
(TeamCreate/TeamDelete/SendMessage/Agent/TaskStop/SyntheticOutput) with
literally no Read/Write/Bash — "pure orchestration, does not directly
operate on files" is the design note verbatim — and it enforces the
*inverse* rule too (workers cannot call the coordinator's control-plane
tools via an `INTERNAL_WORKER_TOOLS` exclusion), i.e. bidirectional
isolation.

**Best-practice synthesis.** All three sources converge on the same shape
at different layers. Codex: role-as-config-overlay; capability restriction
(`sandbox_mode`) is an orthogonal, coarser lever inherited from the general
per-turn sandbox system — no coordinator-specific mechanism. Claude Code's
public docs: compositional — a coordinator is just a subagent definition
whose `tools` field includes `Agent(...)` and excludes Write/Edit/Bash, the
same machinery as any other tool restriction — flexible, but ultimately
advisory since it's an LLM-interpreted tool list. Claude Code's internal
"coordinator mode" is a structurally distinct, genuinely capability-limited
implementation with bidirectional isolation. The disagreement is about
mechanism *strength*, not intent. For carina — which already enforces
capability ceilings via a Rust kernel rather than an LLM-interpreted tool
allowlist — the internal Claude Code shape (genuinely capability-limited,
not merely a suggested tools list) is the correct analog, because carina's
whole premise is that the model cannot be trusted to self-restrict via a
hinted tool list. The sharpest cross-source lesson is the v2.1.186 bug:
whatever carina builds must be enforced at the kernel/attenuation layer on
every delegation hop, not just the top-level spawn call — which carina's
existing `attenuate(parent, requested)` monotonic-decrease chain already
guarantees structurally for any new profile added to the catalog, for
free.

**Carina's response (design_only).** This item cleanly decomposes into (a)
a trivial, safe, purely-additive profile-catalog entry that could land with
near-zero risk, and (b) enforcement/spawn-gate wiring that is currently
owned by an active, unmerged sibling worktree (`feat/public-subagent-dsl`)
touching the exact function
(`go/daemon/subagent.go::executeSpawnOutcome`) this item would need to
modify to make the coordinator profile's spawn path meaningfully distinct
from a read-only profile's spawn path (both currently route through the
same uniform `PluginLoad` gate). Landing full enforcement now risks either
duplicating the in-flight `SubagentSpawn` capability split or directly
conflicting with it at merge time. The profile-catalog half
(`crates/carina-policy/src/lib.rs`, `go/daemon/agents.go`,
`protocol/capabilities/*.json`) is safe to land as ordinary additive work
once that branch merges; the full item — a coordinator profile whose spawn
capability is meaningfully privileged relative to its (denied) direct-action
capability — depends on sequencing behind it. All sources converge that
this is architecturally sound and low-risk (Claude Code ships it as a named
worked example; Codex has no equivalent but nothing that conflicts; Claude
Code's internal "coordinator mode" proves the stronger hard-capability-
limited version is production-viable, not just a docs suggestion). Nothing
here weakens carina's invariants: the new profile is just another entry the
existing per-capability dispatch already enforces, coordinator denies
strictly more than any existing profile except sandboxed/read-only's
non-spawn surface, and spawn stays behind the same approval gate it always
was. Once `feat/public-subagent-dsl` merges, this becomes a same-pass adopt
with a two-file diff.

### Deferred lazy tool-schema + health-gated tool-pool + ToolSearch

**Codex approach.** `codex-rs/tools/src/{tool_search.rs, tool_discovery.rs,
tool_spec.rs, responses_api.rs}` plus `codex-rs/core/src/{
mcp_tool_exposure.rs, tools/handlers/tool_search.rs,
tools/handlers/tool_search_spec.rs, tools/spec_plan.rs}` implement a native
OpenAI Responses-API tool type `ToolSpec::ToolSearch{execution:"client",
description, parameters}` plus a `defer_loading: Option<bool>` flag on
individual `ResponsesApiTool`/`ResponsesApiNamespace` entries.
`ToolSearchInfo::from_tool_spec` strips a tool's `output_schema`, sets
`defer_loading=true`, and builds a `search_text` from name, name-with-
underscores-replaced, description, and flattened JSON-schema property
names/descriptions. `ToolSearchHandler` builds a client-side BM25 index
(the `bm25` crate) over all deferred tools' `search_text` and answers
`tool_search(query, limit=8 default)` calls, coalescing results by
namespace; the index is cached per identical search-info set and rebuilt
only when the deferred-tool set changes. Gating is genuinely all-or-nothing,
**not** count-based: `search_tool_enabled(turn_context)` checks
`turn_context.model_info.supports_search_tool` (a model-capability flag)
AND `namespace_tools_enabled` (a provider capability); `build_mcp_tool_
exposure` then puts either ALL non-codex-apps MCP tools into `direct_tools`
(search unsupported) or ALL of them into `deferred_tools` (search
supported) — no per-server or per-count threshold, purely a model/provider
capability switch. GitHub issue `openai/codex#9266` and merged PR `#16944`
show Codex's community explicitly cited Claude Code's MCPSearch as the
design precedent, then generalized from MCP-only to all dynamic/namespace
tools — the same MCP-first-then-generalize trajectory Claude Code itself
took.

**Claude Code approach.** Per `code.claude.com/docs/en/agent-sdk/
tool-search.md`: tool search is ON by default. It withholds full tool
definitions from context; the agent sees only a name+summary index and
calls a `ToolSearch` tool to load 3–5 most relevant matches into context
for the rest of the conversation (until compaction evicts them and
re-search is needed). Concrete numbers: 50 tools costs ~10–20K tokens
upfront; tool-selection accuracy degrades past 30–50 tools loaded
simultaneously; catalog cap 10,000 tools; below ~10 tools upfront loading is
faster. Configuration via `ENABLE_TOOL_SEARCH` env var: unset=platform-
dependent default, `true`=force on, `auto`=activate when tool-definition
tokens exceed 10% of context window, `auto:N`=configurable percentage,
`false`=always load upfront. Requires a `tool_reference` content-block
model capability (unsupported on Haiku); disabled by default on GCP Agent
Platform and through non-first-party `ANTHROPIC_BASE_URL` proxies since
they don't forward `tool_reference` blocks. `mcp.md` adds an MCP-specific
"Scale with MCP tool search" section: per-server `alwaysLoad: true`
bypasses deferral for a small always-needed subset regardless of the global
setting. When a needed server is still connecting, the wait happens
transparently inside the `ToolSearch` call; when a server fails to
connect, that failure surfaces inside `ToolSearch`'s no-match results so
the model reports unavailability instead of hallucinating. `tools-
reference.md` confirms no fixed per-server tool cap — the practical limit
is the context-window budget. Per the changelog, this shipped MCP-scoped
first — literally named `MCPSearch` for its first two weeks (Jan 14–27
2026) — before generalizing to all tool types.

**Best-practice synthesis.** Both converge on the identical mechanism
shape: tool definitions are withheld/deferred from per-turn context by
default once a threshold is crossed; a dedicated search tool (lexical/
keyword matching over name+description+schema text, not embeddings)
resolves a query to a small ranked set of full definitions; resolved tools
stay promoted for the remainder of the session until compaction evicts
them; there is always an escape hatch to force always-loaded status for a
small always-needed subset. They differ in *where* gating happens: Codex's
gate is a coarse model/provider capability switch applied uniformly to the
whole MCP surface (OpenAI controls which models advertise the capability).
Claude Code's gate is a local, tunable threshold (percentage of context
window) plus a platform capability check, because Claude Code spans many
more deployment surfaces where a uniform capability switch isn't
available. The critical shared lesson: both shipped this MCP-scoped first
before generalizing — validating that MCP is the correct, narrower first
target for carina too, not the native 7-tool set with no growth pressure.
Both treat search as a protocol-level primitive rather than pure prompt
engineering; carina, calling a provider chat/completion API without that
specific capability, cannot replicate the protocol trick and must emulate
it via prompt engineering — a local keyword index plus a real `tool_search`
action whose results are injected as a normal transcript `Observation`.
This is a legitimate lesser-privileged emulation, not a carina-specific
compromise — even Claude Code's own `auto`/`false` fallback modes for
non-`tool_reference`-capable proxies effectively do the same thing.

**Carina's response (design_only).** This is a real, externally-validated
gap: two independent frontier coding agents converged on the same
MCP-tool-deferral-plus-lexical-search shape, both went MCP-first before
generalizing, and `absorption-plan.md` already anticipated this item under
Wave 7 with the correct prerequisite noted. Re-verification confirms the
gap is real and in one respect worse than the seed implied (no health
tracking exists at all, not just no ToolSearch) and in another better
(authorization/kernel-gating on the call path is already fully correct —
this is a pure prompt-construction/visibility problem that does not touch
`PluginLoad` gating or fail-closed defaults; it only changes what the model
can see before it asks). `design_only` rather than `adopt` is driven by
concrete, session-specific blockers, not architectural doubt: the natural
landing site is a currently hot file (`agent.go`), the design depends on a
Wave-4 Go abstraction (`buildTool()`) that doesn't exist yet, and
health-gating means adding new shared state to `mcp.go`'s `Manager`/
`Client` that deserves its own review. The design is fully additive (new
files `go/mcp/search.go` plus tests; the one existing-file touch in
`agent.go` is a small, mechanically reversible conditional swap; the new
action lives in the newer, lower-churn `tool_lifecycle.go` rather than
`agent.go`'s hot turn loop), stays entirely within carina's kernel-gated/
audit-authoritative model (search needs no capability since it only
surfaces metadata the daemon already holds; every actual MCP call still
goes through the unchanged `PluginLoad`-then-audit path), and adds no
unbounded cost or concurrency (the index is built from already-connected,
already-fetched tool lists, no new network calls). This is exactly the
kind of item worth landing a design doc for while the actual code waits for
`buildTool()` to exist and the hot files to cool down.

### Content-block (image) support + context-aware dynamic skill prompts

**Codex approach.** Read directly from `codex-rs/protocol/src/models.rs`,
`core/src/image_preparation.rs`, `core/src/tools/handlers/view_image.rs`,
`core/src/original_image_detail.rs`, `protocol/src/openai_models.rs`.
`ContentItem::InputImage { image_url: String, detail: Option<ImageDetail> }`
and `FunctionCallOutputContentItem::InputImage` are dedicated variants
alongside `InputText`/`OutputText` — images are first-class content-block
members of the same enum used for both message content and tool-output
content, not a separate side-channel. `ImageDetail` is
Auto|High|Original|Low, with Low explicitly rejected as unsupported rather
than silently downgraded. Capability gating: the `view_image` tool handler
checks `turn.model_info.input_modalities.contains(&InputModality::Image)`
**before** any file I/O and fails closed with a clear
`FunctionCallError::RespondToModel` if the active model can't accept
images — a per-turn/per-model capability check, not a static feature flag.
File access goes through the existing sandboxed filesystem
(`fs.read_file(&path_uri, Some(&sandbox))`) — image ingestion rides the
same sandbox/permission machinery as any other file read, no bypass path.
Bytes become a `data:` URL; an `ImageViewItem` is emitted as a typed turn
item for session history (auditable). Downstream,
`image_preparation.rs::prepare_response_items` re-validates every
`InputImage` right before sending to the model: rejects remote http(s)
URLs, rejects `detail:low`, resizes/checks size limits, and on **any**
failure replaces the content item in place with a `ContentItem::InputText`
placeholder string rather than dropping the item or erroring the whole
turn — fail-closed-per-item, never-crash-the-turn.
`ToolOutput::log_preview()` for images returns only "&lt;image data URL
omitted: N bytes&gt;", never the actual data URL, keeping image bytes out
of logs by construction. Model capability is metadata-driven
(`input_modalities: Vec<InputModality>` on model-info, default =
[Text, Image]), not hardcoded per-provider. Skills (a separate
`core-skills` crate) implement "progressive disclosure": `SkillMetadata`
(name, description, path, policy) is always resident in the system prompt,
budgeted to a percentage of context window with truncate-and-warn fallback;
full `SKILL.md` body is only read from disk and injected when explicitly
mentioned (`$SkillName` token or structured `UserInput::Skill`). Missing/
unreadable skill files produce a warning string, not a crash.
`SkillMetadata::allows_implicit_invocation()` and per-skill
`SkillPolicy.products` let a skill opt out of auto-triggering.

**Claude Code approach.** Plain image content-blocks (paste/drag-drop/MCP/
dialog attachments, Read-tool image ingestion) are the relevant analog to
carina's gap — "computer use" (screen control) is a distinct, heavier,
opt-in, macOS-only, plan-gated feature layered on top of vision and is not
directly applicable to a daemon/CLI runtime with no desktop-control
surface. `skills.md`: skill descriptions are always loaded into context
(budget = 1% of context window, LRU-drop starting from least-invoked
skills on overflow) while full `SKILL.md` body loads only on invocation —
matching Codex's progressive-disclosure shape exactly, described as a
table: default (description always resident, full body on invoke),
`disable-model-invocation:true` (description not resident, only user can
invoke), `user-invocable:false` (description resident, only Claude can
invoke). Invoked skill content persists as a single message for the rest
of the session; re-invoking an unchanged skill inserts a short "already
loaded" note instead of duplicating (fixed in v2.1.202 — before that every
re-invocation duplicated the full text, a real token-bloat bug).
Auto-compaction re-attaches invoked skills after summarization, capped at
5,000 tokens each with a shared 25,000-token budget, evicting oldest-
invoked first. **Changelog evolution (the most valuable finding):** the
same fail-closed defect — "unprocessable images (zero-byte, corrupt)...
crashing the request instead of becoming a text placeholder" — was fixed
**twice**, in v2.1.157 (May 29) and again in v2.1.187 (June 23), meaning a
regression reintroduced the crash-instead-of-placeholder bug after the
first fix — this fail-closed/never-crash-the-turn invariant for images is
easy to accidentally violate even for Anthropic's own team and needed
dedicated regression coverage. Other image evolution: v2.1.174/v2.1.202
fixed images/files sent via Remote Control being silently dropped without
a caption (silent data loss, same "never lose the content block" spirit);
v2.1.161 fixed pasted-image job rows leaking full filesystem paths instead
of the `[Image #N]` placeholder (privacy/log-hygiene, same spirit as
Codex's `log_preview()` omission). Skills evolution: v2.1.157 made skills
auto-load from `.claude/skills` without a marketplace; v2.1.169 added
`disableBundledSkills`; v2.1.178 added nested-directory skill scoping;
v2.1.186 changed malformed-YAML handling from fail-silently to
load-body-with-empty-metadata (another fail-closed-content, fail-open-on-
parse-errors tightening). Net trajectory: both capabilities started
functional but leaky (crashes, silent drops, duplication, path leakage) and
every subsequent release tightened toward "never lose or crash on the
content, never leak paths/bytes into logs, never duplicate on re-invoke."

**Best-practice synthesis.** Convergent design across both: (1) images are
one more variant of the existing content-block/message-content type, not a
parallel subsystem. (2) Image capability is model-metadata-gated per turn,
checked before any I/O, fails closed with a clear text error — never a
silent drop, never an unconditional attempt. (3) Image bytes ride the
existing sandboxed/permissioned file-read path — no separate "vision file
access" bypass in either codebase. (4) Right before the prompt is sent,
every image is re-validated independent of the ingestion-time check, and
any failure degrades to a text placeholder in place — Claude Code's
changelog shows this exact invariant regressed and had to be re-fixed once,
so it needs an explicit regression test, not just a code path. (5) Image
bytes are explicitly excluded from logs/previews — audit-log hygiene for
image content is a first-class design requirement. For skills: both
converge on progressive disclosure — cheap metadata always resident,
budgeted to a small percent of context window with graceful truncate-and-
warn on overflow, full body loaded on demand, persisted for the rest of the
session, de-duplicated on re-invocation.

**Carina's response (design_only).** Two independent, competing frontier
agent runtimes converge on the same architecture for image content, and
carina already has three of the five prerequisites sitting unused:
`provider.Model.Modalities.Input` (capability metadata),
`go/artifact.Store` with `MediaType` (content-addressed blob storage with
quota/GC), and `Observation.Pinned`/`OriginalRef` (the precedent for
out-of-band-content-by-reference in the transcript type). The missing
piece is narrow: a typed `MediaRef` on `Observation` plus a validating
ingestion helper — safely additive, new files, one small struct field, no
new kernel capability (local image reads ride the existing `FileRead`
gate), no touch to `agent.go`/`daemon.go`/`subagent.go`/
`tool_lifecycle.go`. It does **not** constitute "wiring a vision provider"
— no model actually receives these images this pass; a
`modelSupportsImageInput` helper exists so a future prompt-assembly change
can gate on it, but nothing calls it into the live request path yet — the
correct scope boundary for this pass. The skills half is correctly
excluded from code this pass: its natural landing point is prompt/context
assembly, which lives in the hot, heavily-churned `agent.go`. Landing it
now would either require touching that file against instructions or live
in a disconnected package with no real integration — worse than a clean
design doc. Both sources give enough detail (budget-as-percent-of-
context-window, truncate-and-warn, explicit-mention-triggers-full-load,
`Pinned`-equivalent persistence) that a future pass can implement directly
from this design without further research. Nothing here weakens carina's
fail-closed/kernel-gated/tighten-only invariants: any real file-backed
image read routes through the existing `FileRead` capability, raw bytes
stay out of the audit log by construction (metadata-only journaling), and
a strict media-type allowlist plus magic-byte verification is stricter than
the seed's baseline.

---

## Deferred

Downgraded from an initial `adopt` verdict by adversarial review after
re-checking the design against carina's actual current code state (not
just the design doc). In each case the underlying idea is sound and
externally corroborated by two-plus sources; what's missing is a
specification gap that needs to be closed, or a landing window free of
active same-seam concurrent work, before it is safe to commit.

### Multi-tier compaction: verbatim-user preservation + rebuild-with-key-files

**Codex approach.** `codex-rs/core/src/compact.rs` is the authoritative
local (non-remote) compaction path, and maps directly onto both
sub-items. **(1) Verbatim-user preservation:** `collect_user_messages()`
walks the full pre-compaction history and extracts every
`TurnItem::UserMessage` that is not itself a prior compaction summary
(`is_summary_message` checks for the `SUMMARY_PREFIX` marker).
`build_compacted_history_with_limit()` then re-appends these verbatim user
messages — not reworded, not summarized — to the new post-compaction
history, walking from most-recent backward and packing under a
`COMPACT_USER_MESSAGE_MAX_TOKENS = 20_000` budget; only if a message would
blow the remaining budget is it token-truncated, and only the
oldest-selected message eats that truncation. The freshly generated AI
summary (`SUMMARY_PREFIX` + model output) is appended after these verbatim
messages, always last. Codex's post-compaction history shape is: [reinjected
initial context] + [verbatim user messages, budget-capped] + [AI summary]
— a strict superset of carina's plan, preserving ALL user turns verbatim
(budget permitting), not just the original goal. **(2) "Rebuild with key
files" equivalent:** Codex does **not** have a "top-N recently-edited
files" tier in this OSS clone (grepped `compact*.rs`, `world_state/*.rs` —
no such logic; that shape may be describing OpenAI's proprietary hosted
`/v1/responses/compact` path, opaque/server-side, not present in the
open-sourced Rust). What Codex does have is `WorldState`
(`context/world_state/mod.rs`) — AGENTS.md, environment info, apps/plugin
instructions — deterministically re-rendered and spliced back into
compacted history via `build_compaction_initial_context()` +
`insert_initial_context_before_last_real_user_or_summary()`. This is a
"rebuild deterministic project-level context" tier, analogous to Claude
Code re-injecting CLAUDE.md/memory from disk, but narrower in scope than
what `absorption-plan.md` describes (no modification-frequency-based file
reinjection). Same architectural pattern regardless: deterministic,
disk-sourced re-injection rather than trusting the model's summary to
remember it. On `CodexErr::ContextWindowExceeded` during the compact call
itself, Codex trims from the beginning of history one item at a time and
retries — a defensive last-resort shrink, not a rebuild.

**Claude Code approach.** Two independent sources triangulate the same
two-part mechanism, shaped differently. Official docs
(`context-window.md`, "What survives compaction" table): compaction
replaces conversation history with a single AI-generated structured
summary. What is *not* trusted to the summary — deterministically reloaded
from disk after compaction instead — is: project-root CLAUDE.md +
unscoped rules, auto memory MEMORY.md, and invoked-skill bodies (capped at
5,000 tokens/skill, 25,000 tokens total, oldest dropped first). Nested/
path-scoped CLAUDE.md and rules are explicitly **not** reloaded
automatically — a deliberate scope-down to avoid unbounded reinjection
cost. `/compact focus on X` lets the user steer the one-shot summary. The
local `claude-code-notes` collection (reverse-engineered from actual
source, describing an *older/different internal shape* than current
official docs — itself informative) documents a 4-tier pipeline: (1)
tool-result truncation, (2) image-to-description replacement, (3)
"Context Collapse" progressive folding by message-importance heuristic,
(4) AutoCompact full AI summary. Its AutoCompact system prompt requires
preserving 9 categories, explicitly calling out category 6 in capital
letters: "ALL user messages that are not tool results (copy verbatim where
possible)" — the notes explain this capitalization is a direct engineering
response to an observed LLM bias: summarization models preferentially
retain positive decisions and drop negative/corrective feedback ("don't
use Redux"), so verbatim-copy is prescribed specifically for user turns,
not paraphrase. Separately, the notes describe a `rebuildAfterCompact()`
stage reinjecting up to 5 "key files" (files most frequently touched by
Edit/Write tool calls in pre-compaction history) capped at 5,000 tokens
each, plus skill/CLAUDE.md-equivalent instructions capped at 25,000 tokens
— the literal "rebuild with key files" tier `absorption-plan.md` asks for,
and it is instruction-prompt-based (a verbatim-copy request to the
summarizing model) for user-message preservation, not a separately-coded
verbatim-extraction step like Codex's `collect_user_messages()`. Evolution
story: auto memory (requires v2.1.59+) is a newer layer added specifically
so "notes Claude wrote about itself" survive compaction independent of the
summary; Week 20 (v2.1.139–142) shipped "Summarize up to here" in the
Rewind menu, turning compaction from all-or-nothing into a user-steerable
range operation; v2.1.166 fixed compaction not honoring
`--fallback-model`; v2.1.172 fixed sessions on 1M-context getting
permanently stuck when lacking usage credits — compaction had to auto-fire
to unstick them; v2.1.203 (July 2026, most recent) made compaction's own
summarization call inherit the session's extended-thinking config. Net
arc: compaction went from "one AI summary call" to "a layered system with
a dedicated non-compactable memory tier, user-steerable range control, and
first-class reliability/quality treatment for the compaction call itself"
— the trend is toward NOT relying on the summary alone, exactly the
multi-tier direction `absorption-plan.md` points carina toward.

**Best-practice synthesis.** Both converge on the same two-part shape,
independently arrived at — strong evidence it is actual best practice
rather than house style. Verbatim-user preservation is universally treated
as a *structural* guarantee, not a prompt instruction that trusts the
summarizing model to comply: Codex enforces it in code (hard token cap,
oldest-truncated-first); Claude Code's older documented shape enforces it
as a strong prompt instruction to the summarizer (weaker, still
model-dependent, but explicitly named as an engineering response to a
known failure mode), and Claude Code's current docs show the trend
continuing toward less trust in the single-summary-call (auto memory,
range control). The rebuild/reinjection tier in both is scoped to
*deterministic, disk/history-derived* artifacts, never model-generated
content — the same invariant across all three variants: the rebuild
tier's selection is computed from structured logs of what actually
happened (edit counts, disk state), never from what the summarizing model
claims was important. This is the same principle carina's own
`filesTouched()` already applies for the Files-Read/Files-Modified section
of `SummaryContent` — carina already has the right instinct, just not yet
the tier that acts on it. Codex bakes verbatim-preservation into
strongly-typed history-manipulation code because its history IS the
API-visible object model; Claude Code leans more on prompt-engineering the
summarizer historically, and the changelog shows it moving *toward*
Codex's structural approach over time — convergence, not permanent
divergence. Carina, which already renders a typed `Transcript`/`Turn`/
`Observation` object rather than an opaque string, is the natural fit for
the Codex-style structural approach.

**Carina's response (defer, downgraded from adopt).** The initial pass
called this a clean adopt: additive-only, kernel-gated read-reuse for the
rebuild tier, a new `ContextRebuilt` audit event mirroring the existing
`ContextCompacted` pattern, and bounded everywhere it could grow
unboundedly (`VerbatimUserMaxChars` budget with oldest-first eviction,
top-K=5 files with per-file and total char caps, single-shot rebuild-
trigger guard). Adversarial re-review downgraded this to `defer`: the
design as written has two real specification gaps, not just polish items.
Part B's file-tracking mechanism (`EverModifiedFiles` from `addTurn`)
doesn't match how `Turn.Path` is actually populated today and needs a
redesign pass before it's a safe drop-in. Part A's receipt/hash-preimage
rework for the `verbatimKept`/`toSummarize` partition is acknowledged as
needed by the design's own test plan but was never actually specified, and
getting it wrong breaks audit hash-chain continuity. Combined with
confirmed active, same-seam concurrent work in a sibling worktree this
session, "adopt now" was premature. One part of the original diagnosis
does hold up and sharpens the remaining scope: the "original user goal"
half of the verbatim-preservation sub-item turns out to already be safe by
construction (`StablePrefix`/`TASK` in `promptcache.go`), so the real
remaining risk is narrower than the seed implied — mid-task steering/
correction messages, which are `Pinned` against elision but not against
the summarization fold, reproducing the `claude-code-notes` category-6
failure mode exactly. Plan: defer one cycle to (a) spell out the
`addTurn`/`EverModifiedFiles` mechanism concretely (likely requires
touching the patch call site, not just the steering site), (b) spell out
exactly how `compactionPreimageHash`/`RemovedTurns` are recomputed under
the partition, then re-propose once the seam's concurrent churn has
settled.

### Layered setting-source allowlist (Managed/User/Project/Runtime with managed-locked keys)

**Codex approach.** Codex-rs uses two complementary mechanisms. (1) A
plain layered merge stack: `codex-rs/config/src/config_layer_source.rs`
defines `ConfigLayerSource` (Mdm=0, System=10, EnterpriseManaged=15,
User=20/21-with-profile, Project=25, SessionFlags=30, plus two Legacy
variants) with a numeric `precedence()` — higher wins, last-writer-wins,
`ConfigLayerStack` folds lowest-to-highest. `cloud_config_layers.rs` shows
backend-delivered enterprise config fragments turned into
`ConfigLayerEntry`s tagged `EnterpriseManaged`; a `strict_config` mode
rejects unknown TOML fields per-fragment. (2) A **separate** hard-
constraint mechanism for anything security-sensitive:
`codex-rs/config/src/constraint.rs` defines `Constrained<T>` — a value
wrapped with a `validator: Arc<dyn Fn(&T) -> ConstraintResult<()>>` and
optional normalizer. `allow_only(v)` locks a field to exactly one value;
`set()`/`can_set()` always re-validate. `config_requirements.rs` defines
`RequirementSource` (MdmManagedPreferences, EnterpriseManaged{id,name},
SystemRequirementsToml{file}, Composite, Legacy variants) attached to each
`ConstrainedWithSource<T>` so error messages say *which* admin layer set
the constraint. Per OpenAI docs (WebSearch-corroborated, direct fetch
ECONNRESET'd) and `developers.openai.com/codex/enterprise/managed-
configuration`: enterprises get `requirements.toml` (admin-enforced
constraints users cannot override) composed from
System(`/etc/codex/requirements.toml`) < MDM < cloud-managed, plus
separately `managed_config.toml` (managed defaults — starting values a
user may change mid-session, reapplied fresh at next launch, above local
`config.toml` but below CLI `--config` overrides). Design insight:
Requirements are validators (reject on write), Managed defaults are
merge-priority (overwritten on read/reload) — Codex deliberately keeps
"what wins in the merge" and "what is forbidden regardless of merge order"
as two different types, not one flag on a merge layer.

**Claude Code approach.** Exactly four scopes: Managed (server-managed via
claude.ai admin console OR endpoint-managed via plist/HKLM/managed-
settings.json/HKCU registry, checked in that priority order, sources don't
merge with each other) > Local (`.claude/settings.local.json`, gitignored)
> Project (`.claude/settings.json`, committed) > User
(`~/.claude/settings.json`). Managed is always highest and "can't be
overridden by anything," including CLI flags. Two sub-mechanisms matter:
(1) Managed-only settings — a fixed key list (`allowManagedPermission-
RulesOnly`, `allowManagedMcpServersOnly`, `allowManagedHooksOnly`,
`disableAutoMode`, `disableAgentView`, `disableSideloadFlags`,
`forceLoginMethod`/`OrgUUID`, `forceRemoteSettingsRefresh`, `required-
MinimumVersion`/`MaximumVersion`, `enforceAvailableModels`,
`allowAllClaudeAiMcps`) read only from managed sources — placing them in
user/project settings has literally no effect, by parsing/lookup design,
not by override. (2) Array-merge-with-lock semantics for permissions/MCP
allowlists: normally `permissions.allow`/`deny` and
`allowedMcpServers`/`deniedMcpServers` merge across all scopes (deny always
wins regardless of source); when `allowManagedPermissionRulesOnly`/
`allowManagedMcpServersOnly` is set, only the managed allowlist applies.
Untrusted-project-source filtering is explicit and separate from the
managed system: project `.claude/settings.json` permission allow rules
require a workspace-trust dialog before taking effect (`.claude/
settings.local.json` is user-owned and skips it) — Claude Code treats
"this file came from the repo, which I have not vetted" as an independent
trust gate layered *under* the managed-lock system, not merged into
precedence. Managed settings parse tolerantly since v2.1.169 (a single
malformed entry is stripped + warned, the rest still enforces). Changelog
evolution (the most valuable finding): built incrementally over ~6 months
of 2026 — 2.1.163 added `requiredMinimumVersion`/`MaximumVersion`;
2.1.169/170 fixed managed MCP allow/deny not being enforced on reconnect,
IDE-typed configs, `--mcp-config` sideloads, and before remote settings
finished loading (closing a fail-open startup race); 2.1.174/175/176
hardened `availableModels` so it also constrains subagent/dispatch/
advisor model picks and can't be widened by env-var aliases; 2.1.176
fixed `forceRemoteSettingsRefresh` not actually being honored from MDM/
file policy; and **most tellingly, v2.1.207 (July 11, 2026, one day
before this analysis) removed project-level `.claude/settings.local.json`/
`.claude/settings.json` as a valid source for autoMode/pluginConfigs
respectively**, pushing those reads up to user-level or managed-only —
Anthropic tightening an already-shipped mechanism after finding project/
repo-sourced config was a bigger blast radius than intended for exactly
the categories this carina item asks about. The explicit security posture
elsewhere is blunt about the general trust asymmetry this reflects.

**Best-practice synthesis.** Codex and Claude Code converge on the same
core insight despite very different implementations: "higher tier wins in
the merge" and "this key is forbidden to lower tiers regardless of merge
order" are two different mechanisms, not one. Codex makes this explicit as
two types (precedence-ordered enum for ordinary merge vs.
`Constrained<T>` validator-with-provenance for hard locks). Claude Code
achieves the same result with a simpler surface: normal keys follow scope
precedence, but a fixed managed-only key list is architecturally
unreadable from any non-managed source, and array-valued security fields
get bespoke merge-with-override-flag semantics. Both treat "project/repo
source is less trusted than user/managed source" as orthogonal to
tier-precedence: Claude Code gates project-sourced permission allow rules
behind a workspace-trust dialog independent of precedence; Codex pulls
security-sensitive fields out of the ordinary stack entirely into
Requirements. The single most load-bearing lesson from the changelog is
Claude Code's v2.1.207 change: even a mature, long-shipped managed-
settings system keeps discovering that project/repo-sourced config for
auto-approval-adjacent and plugin-adjacent keys was a wider blast radius
than intended, and the fix is always to narrow what project scope can
touch, never widen it — exactly carina's own tighten-only invariant,
independently arrived at by Anthropic's admin-settings team through
incident-driven iteration.

**Carina's response (defer, downgraded from adopt).** The initial pass
called this a clean adopt (purely additive to `go/config`, a 217-line
isolated well-tested package, plus a small defense-in-depth addition to
`reload.go`, reusing the existing `PolicyDir` delivery mechanism
`orgpolicy.go` already uses for kernel-level org policy). Adversarial
review downgraded to `defer` — not because of blast-radius/hot-file risk
(that concern resolved favorably: `go/config`/`reload.go` are genuinely
isolated, and `config.Load`'s real call sites in
`apps/carina-daemon/main.go` confirm no `agent.go`/`daemon.go`/
`subagent.go` structural touch is needed) but because the security
justification is overstated relative to what the code actually shows. The
threat model conflates the daemon process's own launch-time cwd config
with per-task untrusted-repo config, when carina already has a separate,
correctly-scoped per-root trust mechanism (`trustStore`) for the latter.
The item's own source document (`absorption-plan.md`, Wave 6 note) flags
this exact shape as unconfirmed, which the adopt writeup elided. Plan:
defer until (a) the threat model is re-scoped to what `config.Load`'s
`projectDir` actually represents (the operator's daemon-launch cwd, not an
agent task workspace) and a decision is made on whether that narrower
threat still justifies the machinery, and (b) item 3's baseline-diff
mechanism is sized and speced as its own increment rather than folded into
"small additive edit to `Load()`." The `tier.go`/`managed.go` pieces
(ordinary precedence + hard-lock managed keys) are lower-risk and could be
adopted on their own if re-justified against the narrower, accurate
threat.

### Composable plugin bundles + git-based marketplace + tri-level enable merge

**Codex approach.** Codex has a mature, git-native plugin marketplace
across `core-plugins/src/{manifest,marketplace,marketplace_add,
marketplace_upgrade,marketplace_policy,toggles,store}.rs`. Manifest shape:
`.codex-plugin/plugin.json` (or legacy `.claude-plugin/plugin.json`
fallback) with name/version/description/keywords/skills/mcpServers/apps/
hooks/interface, parsed with strict path-traversal guards. Marketplace add
supports `owner/repo` GitHub shorthand, full git URLs, SSH URLs, and local
dirs, with `--ref`/`--sparse`; `stage_marketplace_source` does the actual
`git clone`/`git pull`. Enable/disable state resolution runs through a
generic n-tier config-layer stack (not bespoke to plugins):
`RequirementSource` enumerates `EnterpriseManaged{id,name}` (MDM/backend-
delivered) > `SystemRequirementsToml` > `LegacyManagedConfigToml` > user >
project. Values under enterprise control are wrapped in `Constrained<T>` —
the same tighten-only, composed-by-intersection idea carina's own
`PolicyBundle`/`OrgPolicy` already uses, just applied generically across
all requirements (marketplace sources, MCP servers, exec policy, hooks)
rather than being plugin-specific. `MarketplacePolicy::validate_source`/
`validate_install` is the concrete tighten-only gate for plugins:
`restrict_to_allowed_sources` + `allowed_sources` map (exact git URL
match, host-pattern regex, or local absolute path) — undefined=
unrestricted, configured=allowlist-only, checked before any network/
filesystem operation on add AND on every install/update/refresh/
auto-update. `toggles.rs::collect_plugin_enabled_candidates` shows enable
state stored as `plugins.<id>.enabled` boolean, merged last-write-wins
within a layer, then layered per the `RequirementSource` chain.

**Claude Code approach.** `plugin-marketplaces.md` documents the identical
shape: `.claude-plugin/marketplace.json` (name/owner/plugins[]) with each
entry needing name+source (relative path, `github{repo,ref,sha}`,
`url{url,ref,sha}`, `git-subdir{url,path,ref,sha}`, or
`npm{package,version,registry}`); `strict` mode controls whether the
plugin's own `plugin.json` or the marketplace entry is authoritative.
Enable/disable/install state is a 3-scope stack (user default / project
`.claude/settings.json` shared / local `.claude/settings.local.json`
override) plus a 4th "managed" scope from `managed-settings.json` that is
read-only and can force-enable or force-disable regardless of what lower
scopes want. The tighten-only gate is `strictKnownMarketplaces` in managed
settings: undefined=unrestricted, `[]`=complete lockdown, list-of-sources=
exact-match or hostPattern/pathPattern regex allowlist — checked before
every network/filesystem op on add AND on install/update/refresh/
auto-update, and cannot be overridden by user or project config.
Dependency resolution (`plugin-dependencies.md`) adds semver-range
intersection, auto-enable-with-dependencies, and blocked-disable-if-
still-depended-on. Evolution story (roughly a year of point releases):
dependency version constraints shipped first (2.1.110) as a standalone
consumer feature; `--plugin-dir` local-only testing predates marketplace
entirely; git-based marketplace + multiple source types matured over ~40
point releases with incremental hardening (auto-rename migration in
2.1.193, LSP-plugin-as-first-class-component, `--plugin-url` for hosted
zips, per-scope uninstall prompts in 2.1.203); `strictKnownMarketplaces`
enterprise lockdown was layered on *late* relative to the consumer
feature, not co-designed with it — Anthropic shipped an open, git-
installable, trust-on-install plugin system first, and only after it was
mature added the org-lockdown seam as a strict allowlist bolted onto
existing config-scope machinery. The explicit security posture
(`discover-plugins.md` #Security) is blunt: "Plugins and marketplaces are
highly trusted components that can execute arbitrary code on your machine
with your user privileges... Anthropic doesn't control what MCP servers,
files, or other software are included in plugins and can't verify that
they work as intended." Trust-on-install with no sandbox — categorically
different from carina's WASM-execution-boundary model.

**Best-practice synthesis.** Codex and Claude Code converge almost exactly
on mechanism despite independent builds: (1) a marketplace manifest
listing plugins with name+source, source being one of {relative-path,
github-shorthand, generic-git-url, git-subdir/sparse, npm}; (2) install =
clone/fetch + copy into a local cache, never execute-in-place from the git
working tree; (3) enable/disable is a boolean per plugin-id, resolved
through an n-tier scope stack where only the top (enterprise-managed) tier
is read-only and can force a value regardless of what lower tiers request
— this IS the tighten-only invariant carina already codifies
(`Constrained<T>` in Codex, `PolicyBundle`/`OrgPolicy` in carina's own Rust
kernel); (4) the org-lockdown allowlist is checked before every network/
filesystem op, not just on initial add — install, update, refresh, and
auto-update all re-check, the fail-closed detail most naive
implementations miss. Where they diverge is trust model: both are
effectively "trust the plugin at install time, then run its code with
full user privilege" — neither routes plugin-contributed code through a
capability-scoped sandbox at runtime, only gating what gets installed/
enabled. Carina's `docs/plugin-model.md` commits to something stronger:
plugins execute inside carina's own WASM/MCP/worker adapters, permissions
are declared and kernel-enforced per-call, and undeclared capability use
is a kernel-level `PolicyViolation`. Best practice for carina: adopt the
manifest/source-type vocabulary and the tighten-only n-tier enable-merge
pattern (both proven, low-risk, aligned with carina's existing primitives)
but do **not** adopt either source's "clone arbitrary git URL and let its
hooks/MCP-servers run as trusted subprocesses" as the execution model —
that would bypass the kernel and violate carina's stated invariant.

**Carina's response (defer, downgraded from adopt).** The initial pass
called this an adopt, scoped down hard from what Codex/Claude Code
actually ship: take the shape (source-type vocabulary, tighten-only n-tier
merge, allowlist-checked-before-every-fetch discipline), reject the trust
model (git-clone stays a *fetch* mechanism only, still funneled through
the existing local-trusted-root `Install()` path so runtime capability-
scoping is untouched; git source opt-in and default-closed, unlike Codex/
Claude Code's default-open-until-restricted stance), and require ed25519
signature verification at fetch time. Adversarial review downgraded this
to `defer` — not because the tri-level enable-merge is unsound (it is
genuinely sound: pure Go logic, testable in isolation, additive schema, no
kernel/crypto claims to verify, and its tighten-only, org-advisory
semantics are a defensible, explicitly-argued divergence from Codex/Claude
Code rather than a reuse claim that turns out to be false — this piece
could plausibly be adopted standalone) but because the item as scoped
bundles that with git-clone-plus-manifest-signing and audit wiring, and
the git-clone increment's core adopt-now justification — "requires ed25519
signature verification... using kernel machinery that already exists but
sits unused today" (`carina-plugin-runtime::SignatureVerifier`) — is
factually wrong about what that machinery does and how it's invoked. That
is not a minor implementation detail; it is the load-bearing security
claim that let the initial pass call git-clone-based fetch "safe to add
now" rather than "a new execution/trust surface needing its own design
pass." Plan: defer so someone can specify an actual manifest-signing
scheme (or deliberately choose to skip signing and gate purely on
host-allowlist + human-approval, a materially weaker but honestly-labeled
posture) before this lands, and work out how a daemon-global, non-session-
scoped Marketplace attributes actions to the kernel's session-keyed audit
log. The tri-level enable merge could be split off and adopted
independently in a future pass if partial credit is wanted — that is the
strongest counter-argument to a full defer, but it was not split out this
pass since the item was scoped as one unit.

---

## Trade-offs

This benchmark is a genuinely productive 0-commit pass, and that is worth
stating plainly rather than treating as a null result. Every one of the 7
items was independently corroborated by at least two of the four sources
as a real, architecturally sound gap — none were found to be a poor fit
for carina, and none were rejected outright. What the adversarial-review
step caught is a different failure mode than architectural mismatch: three
items (`multi_tier_compaction`, `setting_source_allowlist`,
`plugin_bundles_marketplace`) were initially scored `adopt` by a first
pass that took a design's own claims about "using existing kernel
machinery" or "matches how `Turn.Path` is populated" at face value, and
only a second, code-reading pass caught that those specific claims didn't
hold up against carina's actual current state. That is exactly the kind of
error a research-and-design pass should be catching before code lands, not
after — a defer that costs a research cycle is far cheaper than a commit
that silently breaks audit hash-chain continuity or ships a
load-bearing-but-wrong security claim about signature verification.

The remaining four items reached `design_only` for a more mundane reason:
they depend on a prerequisite that genuinely doesn't exist yet
(`buildTool()` for the ToolSearch item, a real second-version consumer for
config-migration idioms) or their natural landing file was a hot,
actively-churned seam this task was explicitly instructed not to touch
(`agent.go` for skill prompts and coordinator-role enforcement,
`subagent.go` for the coordinator spawn gate). In all four cases the
research itself is complete enough that a future implementation pass can
work directly from the design recorded here without re-deriving it.

Two structural observations recur across multiple items and are worth
carrying into future absorption passes as standing lessons, not just
per-item notes. First, the "tighten-only" precedent shows up independently
in Codex's `Constrained<T>` and Claude Code's managed-only-key-list +
v2.1.207's project-scope narrowing — both ecosystems, built independently,
keep discovering the same fix for the same class of bug (project/repo-
sourced config having a wider blast radius than intended) and the fix is
always to narrow, never widen, exactly carina's own invariant. Second, the
verbatim-preservation-for-user-messages pattern (multi_tier_compaction) and
the deny-rule-must-bind-at-every-delegation-hop pattern
(coordinator_orchestrator_role's v2.1.186 bug) are both instances of the
same meta-lesson: a security or fidelity guarantee that is correct at the
top level but implemented as a prompt hint or a single-entry-point check
will eventually be bypassed at a nested or indirect call path, and both
Anthropic and this benchmark's own adversarial-review step had to
rediscover that the hard way. Carina's kernel-enforced,
`attenuate(parent, requested)`-chained capability model is structurally
immune to that specific class of bug for anything that already routes
through it — which is itself the strongest argument for keeping every one
of these four remaining items on that same enforcement path when they do
eventually land.

---

## Deep-tradeoff follow-up (same day)

The benchmark above was written as a 0-commit pass; a same-day deep-tradeoff
pass then re-ran all 7 items against the repository's *current* state, because
that state changed materially within hours of the benchmark being written.
What changed:

- **`Turn.Path` landed** (`38ba80e`, stale-read dedup via `supersedeStaleReads`),
  together with the rest of the compaction-seam churn the benchmark had flagged
  as in-flight (`5898e17` token trigger, `1de7fcc` summary template, `f15efa1`
  compress-once-over-budget).
- **The `feat/public-subagent-dsl` merge started — and completed — during the
  pass.** At analysis start it was the in-flight hot seam blocking anything
  touching `agent.go`/`subagent.go`/`daemon.go`; by mid-analysis it was fully
  merged into main (`da96a34`, verified via `merge-base --is-ancestor` against
  its tip `f499cd4`). Two of the seven verdicts below flipped because of this
  single fact.
- `best_of_n` landed on main (`a815532`) as a plain `dispatchActionOutcome`
  switch case — direct precedent falsifying the "blocked on `buildTool()`"
  reasoning recorded above.

Per-item final verdicts. **Six items landed as code on isolated feature
branches; these are BRANCHES AWAITING MERGE, not merged commits — nothing
below has reached main.** One item closed as already covered. None were
re-deferred; none were rejected.

| Item | Benchmark verdict | Final verdict | Branch (pending merge) |
|---|---|---|---|
| Multi-tier compaction (verbatim-user + key-files substrate) | defer | land_in_branch | `feat/absorb-multi-tier-compaction` @ `d1f7478657f0` |
| Setting-source allowlist (managed-locked keys slice) | defer | land_in_branch | `feat/absorb-setting-source-allowlist` @ `0984bdb3e892` |
| Plugin bundles + marketplace (tri-level enable-merge slice) | defer | land_in_branch | `feat/absorb-plugin-bundles-marketplace` @ `5e4a7ac16a92` |
| Versioned config/state migration (stamp + quarantine slice) | design_only | land_in_branch | `feat/absorb-state-migration` @ `32f0023e0300` |
| Coordinator/orchestrator restricted role | design_only | **already_covered** | — (covered by the subagent-dsl merge in main) |
| Deferred lazy tool-pool + ToolSearch (MCP-scoped) | design_only | land_in_branch | `feat/absorb-tool-pool-toolsearch` @ `e0a82b57bd91` |
| Content-block images (MediaRef plumbing slice) | design_only | land_in_branch | `feat/absorb-content-block-images` @ `b8c31c9a8e07` |

### Which adversarial objections were resolved vs. conceded

The benchmark's adversarial downgrades were not overturned wholesale; each was
split into the part that held and the part whose factual basis expired.

**Resolved (blocker verifiably gone or falsified):**

- *Multi-tier compaction*: all three named blockers are gone — `Turn.Path`
  landed, `CompactionReceipt.Version` already exists with exactly one preimage
  consumer (so the v2 folded-set preimage is a versioned, non-breaking
  redefinition), and the same-seam concurrent work merged. The failure mode is
  confirmed live (`compact()` Step 2 folds Pinned user steer turns
  unconditionally) and is not self-healing.
- *Setting-source allowlist*: the surviving half targets a verified, uncovered
  failure mode — kernel policy bundles never see config keys, and `policy_dir`
  itself is overridable by env/flag, so the org bundle cannot protect its own
  delivery path. Managed-locked keys close that with zero overlap with the
  in-flight merge.
- *Plugin bundles*: the candidate prerequisite ("no org-policy config channel")
  is factually present — `PolicyDir → loadOrgPolicy` is a live overlay channel;
  the tri-level enable-merge the review itself pre-approved for splitting
  landed on that seam.
- *Tool-pool/ToolSearch*: the `buildTool()` prerequisite is falsified by the
  codebase's own pattern (five tools including hours-old `best_of_n` landed as
  plain switch cases), and the hot-`agent.go` blocker dissolved when the merge
  completed mid-analysis with both wiring hunks byte-identical. Fresh reading
  also strengthened the case: `NamespacedTool` strips `InputSchema`, so carina
  surfaces *no* MCP schemas at any server count — `mcp_find` is schema-on-demand
  correctness, not just token economy.
- *State migration*: the "no v2 exists" blocker binds the upgrade-ladder layer,
  not the reserve slice — and `usage.go:59+121` doesn't merely ignore a
  future-version file, it destroys it on the next write; every deferred week
  ships more binaries with that behavior permanently baked in.
- *Content-block images*: withheld previously only because the benchmark was a
  0-commit pass; the plumbing half was already isolated as safely additive.

**Conceded (objection stands; scope cut around it):**

- *Setting-source allowlist*: the project-source-filtering half stays dead —
  `trustStore` owns the untrusted-repo threat, exactly as the adversarial
  review said.
- *Plugin bundles*: the git-marketplace + signing half stays rejected — the
  `SignatureVerifier` reuse claim was and remains false; git-clone is a new
  trust surface needing its own pass.
- *Multi-tier compaction*: Part-B content reinjection is scoped out (touches
  three merge-hot files; its failure mode self-heals in one turn); only the
  deterministic `KeyFiles` selection substrate landed. The seed's own
  `Turn.Path`-on-patch suggestion was additionally verified to be a regression
  and avoided.
- *State migration*: the seed's blanket-stamping premise is false for the four
  bare-array stores (trust.json, approval-grants, schedules, workflowrun) —
  wrapping them is a breaking shape change; they are excluded.
- *Tool-pool*: health-gated pool assembly is excised — new shared state that
  deserves its own review.
- *Coordinator role*: the conflict objection is conceded and **superseded** —
  the subagent-dsl merge shipped the enforcement substance outright (dedicated
  `Capability::SubagentSpawn`, daemon-enforced `AgentSpec.ToolNames`,
  per-hop `SpawnableAgents` via `spawnAllowed`, with tests). Deeper analysis
  showed the prior Rust "orchestrator" Profile design was mechanistically wrong
  anyway (Profile has no spawn axis; `attenuate()`'s child≤parent clamp would
  make a read-only coordinator's workers read-only too). Verdict:
  already_covered, with three recorded non-blocking follow-ups (built-in
  coordinator preset; primary-session `ToolNames` coverage gap; PolicyBundle
  differentiation on the typed spawn resource) and a kill criterion: if main's
  conflict resolution drops the gates, the item reopens.

### Landing discipline

All six branches were cut to avoid the in-flight merge surface (or, for the
two post-merge items, cut from main's tip `da96a34`), were verified green on
their own and adjacent test suites (pre-existing zig-toolchain environmental
failures reproduced identically at clean base), and are **not pushed and not
merged**. Cross-branch ordering note recorded at land time: if both compaction
and MediaRef land, compaction merges first and the MediaRef slice rebases on
top (same file, disjoint functions).
