# Using Carina as a Coding Agent

Carina is not just a runtime — it drives a real ReAct coding agent. The model
**only decides**; every side effect is authorized by the Rust capability
kernel and executed by the Zig toolchain, and the whole run is a
tamper-evident audit trail you can replay and roll back.

```
Configured provider  →  Go agent loop  →  Rust kernel  →  Zig tools
         ▲                                  (authorizes)    (execute)
         └────────────── observation ◀─────────────────────────┘
```

## The loop

Each turn the reasoner emits one JSON action — or a batch of **read-only**
actions (`{"actions":[…]}`, run in parallel; writes stay one per turn) — and
carina runs it and feeds back an observation:

| Action | Goes through | Runs on |
|--------|-------------|---------|
| `{"tool":"list"}` | FileRead capability | Zig `carina-scan` |
| `{"tool":"read","path":"…"}` | FileRead capability | kernel-gated read |
| `{"tool":"search","pattern":"…"}` | FileRead capability | Zig `carina-grep` |
| `{"tool":"run","command":["…"]}` | CommandExec capability (risk-classified) | Zig `carina-run` |
| `{"tool":"patch","path":"…","content":"…"}` | PatchApply capability | Rust transaction → Zig `carina-patch-native` |
| `{"tool":"memory","target":"…","action":"…"}` | MemoryWrite capability | governed long-term memory store |
| `{"tool":"ask_user","prompt":"…","options":[…]}` | — | pauses for a structured operator choice |
| `{"tool":"code.search/symbols/map/def/refs/impact"}` | FileRead capability | code-intelligence index (+LSP when available) |
| `{"tool":"mcp"}` / `{"tool":"mcp_find"}` | governed MCP manager | external MCP servers, policy-gated |
| `{"tool":"spawn","agent":"…","task":"…"}` | SubagentSpawn capability | isolated subagent (declarative manifest, per-agent tool allow-list; `"tasks":[…]` runs them in parallel) |
| `{"tool":"workflow","workflow":"…"}` | PluginLoad capability | named dependency DAG of subagents |
| `{"tool":"best_of_n","task":"…","n":3}` | opt-in (advertised only when enabled) | N parallel candidate patches, judge + optional verify command; only the winner is applied |
| `{"tool":"done","summary":"…"}` | — | ends the task |

Destructive commands (`rm -rf`, `curl … | sh`) are **denied** before they run;
risky ones (installs) are surfaced for approval. Secret files (`.env`, `.ssh`)
are refused. Every file the agent edits is a rollbackable patch transaction.

The loop is hardened against pathological runs: a **LoopGuard** breaks
canonical-signature action repetition, a **MistakeTracker** breaks consecutive
failure streaks, and token-triggered **compaction** folds old turns into a
summary (user-authored turns keep a verbatim tier; every fold is recorded as an
auditable `CompactionReceipt`). Operators can steer a running task through a
two-tier (urgent/normal) mailbox drained at turn boundaries, and plan/act mode
switches are injected as urgent notices.

## The reasoner (model backend)

`go/daemon/reasoner.go` defines the `Reasoner` interface (a pure "think"
step). Three implementations:

- **model-router** — routes through `go/model-router` provider adapters (BYOK).
  Supports prompt segments (stable prefix / volatile suffix) for prompt caching
  and media parts for catalog-gated vision delivery.
- **claude-cli** — uses the local `claude` binary in headless mode
  (`claude -p … --allowedTools "" `) as a pure inference engine, running in an
  isolated empty cwd so it cannot touch the workspace. This works with **CC
  Switch / gateway setups that only admit the Claude Code client** (e.g. the
  Mox gateway), because the request comes from the real `claude` binary. The
  agent's actual file/command/patch work happens in carina, not in Claude Code.
- **scripted** — replays fixed decisions; used by tests to drive the full loop
  deterministically with no model and no cost.

The daemon is provider-first: it selects `model-router` only when an enabled
provider has a BYOK credential, provider environment variable, or an explicitly
configured keyless local endpoint. Setting `CARINA_REASONER_MODEL` pins a model but does not make an
unavailable provider runnable. Disable inherited providers with
`disabled_providers` in `~/.carina/config.json` or the comma-separated
`CARINA_DISABLED_PROVIDERS`; the gate applies to completion, embeddings,
rerank, and automatic reasoner selection after daemon restart. Claude CLI is
an explicit compatibility backend only; enable it with
`CARINA_REASONER_BACKEND=claude-cli` for gateways that require the Claude Code
client. Provider endpoint variables such as `OPENAI_BASE_URL` override catalog
defaults, which supports OpenAI-compatible gateways without a vendor CLI. The
OpenAI adapter prefers the Responses API and falls back to Chat Completions
only when the gateway reports that the Responses endpoint is unsupported.
Optional tiering uses the selected backend:
`CARINA_SUMMARIZER_MODEL` (cheaper model for compaction/summarization) and
`CARINA_VERIFIER_MODEL` (independent done-verifier).

## Run it

```bash
# start the runtime (after `make install`; model-router is auto-wired when a
# provider credential or explicitly configured keyless local endpoint is
# available). The daemon
# discovers the kernel service and native tools next to its own binary; use
# -tools/-kernel only to point at other build outputs.
carina-daemon &

cd your-repo
carina run "fix the failing test in parser.go"     # agent works autonomously
carina audit <session>        # replay every model decision + kernel-gated effect
carina audit verify <session> # confirm the hash chain wasn't tampered with
carina patch list <session>   # every edit, rollbackable
carina patch rollback <id>    # undo an edit
```

## Verified

- `TestAgentLoopExecutesThroughKernel` — scripted list→read→patch→run→done
  actually edits a file through the kernel; audit chain verifies.
- `TestAgentLoopBlocksDestructive` — the agent cannot `rm -rf` even when asked.
- Provider registration tests prove disabled providers cannot receive
  completion, embedding, or rerank traffic even when matching credentials are
  inherited from the environment.
