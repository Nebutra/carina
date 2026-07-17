# Carina docs — Feature map

Authoritative map from **product capability → docs page → source of truth**.  
Update this file when you ship a feature or rename a page.

**Rules**

1. Each feature has **one** canonical docs page; other pages only link to it.
2. Commands and flags must match the current CLI / protocol (not aspirational).
3. Alpha surfaces get a `Badge` and an honest boundary note.
4. Chinese pages mirror EN structure (`zh-cn/...` same relative path).

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
| Install / packages | `/getting-started/installation/` | installer, release scripts | `install.sh`, brew, npm |
| Daemon lifecycle | `/getting-started/quickstart/` | `carina-daemon` | `carina daemon`, `doctor` |
| Sessions | `/api/sessions/` | `session.*` RPC, `docs/tui-session-lifecycle.md` | TUI session, SDK |
| Permission profiles | `/concepts/policy/` | `docs/security-model.md`, profiles JSON | session create `profile` |
| Capability types | `/concepts/policy/` | `protocol/capabilities/` | kernel decisions |
| Approvals | `/concepts/policy/` | permission decisions | TUI approval prompts |
| Agent ReAct loop | `/agents/overview/` | `docs/agent.md` | `carina run`, TUI |
| Sub-agents | `/agents/sub-agents/` | SubagentSpawn attenuation | spawn tool / subagent status |
| Built-in tools | `/tools/overview/` | agent tool table, Zig bins | tool calls |
| MCP | `/tools/mcp/` | MCP manager | inventory / mcp tools |
| Memory | `/memory/overview/` | MemoryWrite capability | memory tools / RPC |
| Workflows | `/workflows/overview/` | `docs/workflows.md`, schema | `carina workflow run` |
| Workflow tutorial | `/workflows/tutorial-review/` | `examples/workflows/review.json` | review pipeline |
| Workers | `/deployment/workers/` | `docs/worker-executor.md` | worker register / poll |
| Audit log | `/concepts/audit/` | event log | `carina audit` |
| Hash verify | `/concepts/audit/` | chain verify | `carina audit verify` |
| Patches | `/concepts/audit/` | patch pipeline | `carina patch` |
| Traces / items | `/observability/traces/` | item stream, events | session items / events |
| JSON-RPC surface | `/api/overview/`, `/api/json-rpc/` | `docs/rpc-api.md`, `protocol/jsonrpc/` | socket / gateway |
| Method catalog | `/api/methods/` | `methods.json` catalogs | Playground TryIt |
| API versions | `/api/versions/` | dual catalogs 0.6 / next | version selector |
| Gateway HTTP | `/api/overview/` | gateway docs | scoped `/v1` |
| Cost reporting | (link from runtime; expand later) | `carina cost` | cost CLI |
| Context engine | `/api/json-rpc/` (highlights) | context.* methods | local-only |
| Nebutra Cloud boundary | introduction + enterprise notes | `docs/nebutra-cloud-boundary.md` | sync off by default |
| Math / KaTeX | `/reference/math/` | KaTeX pipeline | authoring only |
| Glossary | `/reference/glossary/` | this map + product.md | — |

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

- [ ] CLI flags in docs match `--help`
- [ ] New RPC methods appear in catalog JSON + methods page
- [ ] FEATURE_MAP row added/updated
- [ ] EN + zh-cn page pair if user-facing
- [ ] Screenshots / TUI SVG still accurate
