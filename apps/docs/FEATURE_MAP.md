# Carina docs — Feature map

Authoritative map from **product capability → docs page → source of truth**.  
Update this file when you ship a feature or rename a page.

**Rules**

1. Each feature has **one** canonical docs page; other pages only link to it.
2. Commands and flags must match the current CLI / protocol (not aspirational).
3. Alpha surfaces get a `Badge` and an honest boundary note.
4. Chinese pages mirror EN structure (`zh-cn/...` same relative path).

## Release / channel SSOT (0.8 line)

| Fact | Source of truth |
| --- | --- |
| Latest **published** GitHub Release | `gh release view` → currently **v0.8.25** |
| Monorepo source / crate tags | may be at `0.8.2+` before a Release is cut |
| Docs stable channel | **`0.8.x`** (`versions.json`, header Version control) |
| Docs preview channel | **`next`** (tracks `protocol/jsonrpc/methods.json` at head) |
| Protocol registry | `protocol/jsonrpc/methods.json` |
| Generated catalogs | `pnpm sync-protocol` → `src/data/rpc-catalog-0.8.x.json` + `rpc-catalog-next.json` |

Do **not** advertise a release tag in README unless that tag has a GitHub Release with installable assets.

## Differentiation pillars (always surface early)

| Pillar | One-liner | Canonical page |
| --- | --- | --- |
| Policy before effect | Capability kernel + profiles + approvals | `/concepts/policy/` |
| Hash-chained audit | Append-only events, verifiable chain | `/concepts/audit/` |
| Transactional rollback | Patch apply with rollback pointer | `/concepts/audit/` (+ patch CLI) |
| Local authority | Machine stays source of control | `/getting-started/introduction/` |

## Feature matrix

| Feature | Canonical page | Source of truth | UI / CLI surface |
| --- | --- | --- | --- |
| Install / packages | `/getting-started/installation/` | installer, release scripts | `install.sh`, `Nebutra/tap/carina`, npm |
| Daemon lifecycle | `/getting-started/quickstart/` | `carina-daemon` | `carina daemon`, `doctor` |
| Workspace runtimes | `/reference/cli/` | `carina runtime` / `runtimes` | CLI |
| Sessions | `/api/sessions/` | `session.*` RPC | TUI session, SDK |
| Permission profiles | `/concepts/policy/` | `docs/security-model.md`, profiles | session create `profile` |
| Capability types | `/concepts/policy/` | `protocol/capabilities/` | kernel decisions |
| Approvals / dual-axis HITL | `/concepts/policy/` + `/use/cli-tui/` | permission decisions, named presets over existing modes | TUI overlay, `/approval-mode`, `approve`/`deny` |
| Agent ReAct loop | `/agents/overview/` | `docs/agent.md` | `carina run`, TUI |
| Sub-agents | `/agents/sub-agents/` | SubagentSpawn attenuation | spawn tool / `agent.view` / TUI `/agents` |
| Built-in tools | `/tools/overview/` | agent tool table, Zig bins | tool calls |
| MCP | `/tools/mcp/` | MCP manager | inventory / mcp tools |
| Local plugins | `/tools/overview/` | `extension.list` + WASM inspect | TUI `/plugins`, `carina plugin inspect` |
| Memory | `/memory/overview/` | MemoryWrite capability | `carina memory *` |
| Workflows | `/workflows/overview/` | `docs/workflows.md`, schema | `carina workflow run` |
| Workflow tutorial | `/workflows/tutorial-review/` | `examples/workflows/review.json` | review pipeline |
| Workers | `/deployment/workers/` | `docs/worker-executor.md` | worker register / poll |
| Audit log | `/concepts/audit/` | event log | `carina audit` |
| Hash verify | `/concepts/audit/` | chain verify | `carina audit verify` |
| Patches / Changes workbench | `/concepts/audit/` + `/use/cli-tui/` | patch pipeline | `carina patch *`, `/changes` |
| Transcript density | `/use/cli-tui/#transcript-density` | TUI DensityMode | `/density`, Settings, `tui_density` |
| Terminal symbols | `/use/cli-tui/#terminal-symbols` | TUI GlyphPreference + semantic glyph registry | `/symbols`, Settings, `tui_glyphs` |
| Composer status chrome | `/use/cli-tui/#composer-status` | TUI ComposerChrome | Run / Queue / HITL / Isolation / Context / ScreenMode |
| Semantic motion | `/use/cli-tui/#motion--accessibility` | frame scheduler + glyph policy | activity, validation, `CARINA_REDUCED_MOTION` |
| Fullscreen transaction review (next) | `/use/cli-tui/#review-workbench` + `/concepts/audit/#transaction-review` | typed PatchReview projection | `/changes` in Fullscreen |
| Checkpoints | `/reference/cli/` | `session.checkpoint.*` | `carina checkpoint *` |
| Context pressure | `/use/cli-tui/` | `Transcript.compact` + TUI `/context` | `/context`, `carina context *` (no-op adapter) |
| Follow-up queue | `/use/cli-tui/` | `execution.queue.*` | `/queue` |
| Screen modes | `/use/cli-tui/` | TUI ScreenMode | `/minimal` `/fullscreen` `/inline` |
| Traces / items | `/observability/traces/` | item stream, events | session items / events |
| JSON-RPC surface | `/api/overview/`, `/api/json-rpc/` | `docs/rpc-api.md`, `protocol/jsonrpc/` | socket / gateway |
| Method catalog | `/api/methods/` | `methods.json` → catalogs | Playground TryIt |
| API versions | `/api/versions/` | channels **0.8.x** / **next** | version selector |
| Gateway HTTP | `/api/overview/` | gateway docs | scoped `/v1` |
| Cost reporting | `/reference/cli/` | `carina cost` | cost CLI |
| Context engine CLI | `/reference/cli/` | no-op `context.*` boundary; product compressor is `Transcript.compact` | `carina context *` |
| Nebutra Cloud boundary | introduction + enterprise notes | `docs/nebutra-cloud-boundary.md` | sync off by default |
| Math / KaTeX | `/reference/math/` | KaTeX pipeline | authoring only |
| Glossary | `/reference/glossary/` | this map + product.md | — |
| CLI reference | `/reference/cli/` | live `carina --help` | all subcommands |
| Common workflows | `/getting-started/common-workflows/` | recipes | day-to-day |
| CLI & TUI guide | `/use/cli-tui/` | CLI help + TUI slash/keys (`command.rs`, `CAPABILITIES.md`) | operator path |

## Common workflows (recipes)

| Recipe | Page anchor | Uses |
| --- | --- | --- |
| First governed session | `/getting-started/common-workflows/#first-session` | daemon, run, audit |
| Fix a failing test | `#fix-failing-test` | run, policy |
| Review a change safely | `#review-change` | workflow or run |
| Roll back a bad patch | `#rollback-patch` | patch rollback |
| Inspect what the agent did | `#inspect-audit` | audit verify/tail |
| Spawn a narrower sub-agent | `#sub-agent` | sub-agents |
| Call the runtime from code | `#embed-rpc` | JSON-RPC / SDK |

## Maintenance checklist (release)

- [ ] `carina --help` vs `/reference/cli/` (especially `steer <run_id>`, `runtime`/`runtimes`)
- [ ] `pnpm sync-protocol` — catalog `method_count` == methods.json method count
- [ ] Header channel default is current release line (`0.8.x` until 0.9 ships)
- [ ] README release badge links a **published** GitHub Release (not a bare tag)
- [ ] TUI slash table matches `crates/carina-tui/src/command.rs`
- [ ] FEATURE_MAP row added/updated
- [ ] EN + zh-cn page pair if user-facing
- [ ] Screenshots / TUI SVG still accurate
