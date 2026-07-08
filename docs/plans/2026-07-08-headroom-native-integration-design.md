# Headroom Native Context Engine Integration

Source reviewed: `headroomlabs-ai/headroom` at
`4f22cbb05cecd08b42433b6ec19bb926c67e4613` on 2026-07-08.

Implementation status:

- implemented: Carina `contextengine` interface, noop fallback, Headroom binary
  discovery, config/env/flags, `context.status`, `context.doctor`, daemon
  status/doctor reporting, and managed MCP connection wiring;
- not implemented yet: model-facing transcript compression, CCR retrieval
  action, release vendoring, proxy routing, and upstream
  `headroom carina serve --stdio`.

## Decision

Integrate Headroom as a bundled, Carina-managed context engine instead of
asking users to install or wrap Carina manually.

Carina should own the runtime contract, policy boundary, audit events, and
packaging pin. Headroom upstream should own compression quality, CCR retrieval,
MCP/proxy behavior, dashboard/statistics, and the Carina-facing sidecar command.

This gives users a native Carina experience:

```bash
carina run "debug the failing integration tests"
carina context status
carina context retrieve <ref>
```

No user should need to discover `pip install headroom-ai`, edit MCP config, or
run `headroom wrap carina`.

## Upstream Surface To Depend On

Headroom currently exposes these useful integration surfaces:

- Python package `headroom-ai[all]`, installed by `uv tool install` or `pip`;
- a `headroom` CLI (`headroom = "headroom.cli:main"`), shipped by the Python
  package only;
- `headroom proxy --port <port>` for OpenAI-compatible traffic interception;
- `headroom wrap <agent>` for agent-specific setup;
- `headroom mcp serve` / MCP tools `headroom_compress`,
  `headroom_retrieve`, and `headroom_stats`;
- Python and TypeScript SDK-level `compress(...)` APIs;
- local CCR storage for reversible compression and on-demand original retrieval;
- `headroom doctor`, `headroom perf`, `headroom dashboard`, and savings stats.

The npm package is SDK-only and should not be treated as a CLI distribution.
The proxy does not expose HTTP MCP; Carina must use stdio MCP if it uses MCP at
all. `headroom_read` is behind `HEADROOM_MCP_READ` and should stay disabled in
Carina's managed integration unless a future policy review explicitly allows
it.

Carina should not depend on `wrap` as the primary mechanism. `wrap` is useful
for generic agents, but Carina can integrate below the UX layer:

- direct transcript/tool-output compression before model calls;
- explicit retrieval via a Carina-native tool;
- daemon health integration;
- release-managed sidecar lifecycle.

## Runtime Contract

Add a narrow Carina-owned context engine interface:

```go
type Engine interface {
    Compress(ctx context.Context, req CompressRequest) (CompressResponse, error)
    Retrieve(ctx context.Context, ref string) (RetrieveResponse, error)
    Stats(ctx context.Context) (Stats, error)
    Close() error
}
```

Initial request/response shape:

```go
type CompressRequest struct {
    SessionID string
    TaskID    string
    Turn      int
    Kind      string // observation | transcript | file | command_output | model_output
    Tool      string
    Content   string
    Pinned    bool
}

type CompressResponse struct {
    Content         string
    OriginalRef     string
    OriginalSHA256  string
    OriginalBytes   int
    CompressedBytes int
    Ratio           float64
    Engine          string
}
```

The interface should have two implementations:

- `noop`: current behavior, always available;
- `headroom`: local sidecar adapter, default-on only when bundled and healthy.

Context sources must remain Carina-governed. If the adapter needs repository
context, it should consume existing kernel-gated code intelligence/index
results rather than reading files or SQLite directly.

## Supported Integration Modes

Use three modes, each with a different stability contract:

1. `managed_mcp`: Carina starts bundled `headroom mcp serve` as a managed stdio
   process and exposes it through Carina-owned commands/tools. This is the
   fastest upstream-compatible bridge, but it remains an adapter layer.
2. `proxy`: Carina starts bundled `headroom proxy --host 127.0.0.1 --port ...`
   and routes selected model traffic through it. This is powerful but can see
   provider credentials, so it must stay explicit and local-only.
3. `sidecar`: future preferred mode, `headroom carina serve --stdio`, with a
   Carina-specific JSON protocol.

Do not make `headroom wrap carina` part of the productized path. `wrap` mutates
agent/provider/MCP config for external hosts and is not a stable embedding API.

## Headroom Sidecar Protocol

Prefer a Carina-specific upstream command:

```bash
headroom carina serve --stdio --state-dir <dir> --json
```

If upstream has not landed this command yet, Carina can start with an adapter
over `headroom mcp serve` and treat the MCP tool names as an implementation
detail. The first-class Carina command is still the target contract because it
avoids binding Carina to generic MCP prompt/tool semantics.

Required methods:

- `compress`;
- `retrieve`;
- `stats`;
- `health`;
- `shutdown`.

The sidecar must be local-only. It must not receive model provider credentials
unless Carina is explicitly configured to use Headroom proxy mode.

Temporary MCP adapter contract:

- command: bundled absolute `headroom`;
- args: `mcp serve --proxy-url http://127.0.0.1:<managed-proxy-port>`;
- tools allowed: `headroom_compress`, `headroom_retrieve`, `headroom_stats`;
- tools denied: `headroom_read` unless a later policy design enables it;
- no dependency on `/mcp` over HTTP.

Proxy adapter contract:

- bind only to `127.0.0.1`;
- support health checks through `/health`;
- support stats/retrieval through `/stats`, `/v1/retrieve`,
  `/v1/retrieve/stats`, and `/v1/compress`;
- keep separate from Carina's egress proxy.

## Agent Loop Integration

The first code hook should sit where Carina converts tool results into
`Observation` values:

- execute the tool normally;
- keep raw output in the existing audit log path;
- pass non-pinned observation content to the context engine;
- store compressed content in the model-facing transcript;
- record `OriginalRef` and digest metadata on the observation.

Pinned outputs stay verbatim by default:

- failing tests;
- patch apply results;
- permission decisions;
- goal/verifier feedback;
- user steering.

This preserves Carina's existing rule: the audit chain is authoritative, while
the model-facing transcript is a bounded projection.

## Retrieval Tool

Expose a native agent action:

```json
{"tool":"context_retrieve","ref":"hr_..."}
```

The tool should:

- require a live session/task;
- call `Engine.Retrieve`;
- return bounded original content to the transcript;
- audit `ContextRetrieved` with ref, size, and content hash;
- never bypass file/secret/plugin policy.

The retrieval result should be pinned only when it is directly requested by the
model. This keeps normal compression effective while preserving reversibility.

## Audit Events

Add non-authoritative audit events:

- `ContextCompressed`;
- `ContextRetrieved`;
- `ContextEngineStarted`;
- `ContextEngineFailed`.

Event payloads should include:

- engine name/version;
- session/task/turn;
- content kind/tool;
- original/compressed byte counts;
- original SHA-256;
- original ref;
- error class when degraded.

Do not write raw original content to new events. Raw command/file/model events
already remain in the existing audit stream where applicable.

## Configuration

Add daemon config fields:

```toml
context_engine = "auto"      # auto | off | headroom | noop
headroom_bin = ""            # override bundled binary
headroom_state_dir = ""      # default: <carina state>/headroom
headroom_mode = "managed_mcp" # managed_mcp | sidecar | proxy
headroom_proxy_port = 0      # 0 = daemon chooses a localhost port
headroom_token_budget = 4000
```

Semantics:

- `off`: no compression and no sidecar startup;
- `noop`: exercise the interface without changing content;
- `auto`: use bundled Headroom if healthy, otherwise degrade to noop;
- `headroom`: fail daemon startup if Headroom is missing or unhealthy.

Managed environment:

- `HEADROOM_WORKSPACE_DIR=<headroom_state_dir>`;
- `HEADROOM_CCR_BACKEND=sqlite` by default;
- `HEADROOM_CCR_SQLITE_PATH=<headroom_state_dir>/ccr_store.db`;
- optional `HEADROOM_CCR_TTL_SECONDS`, surfaced in `doctor`.

Current upstream defaults are not a Carina API guarantee. Treat TTL and storage
settings as runtime-discovered metadata from Headroom stats/health when
available.

## Packaging

Carina release packages should include a pinned Headroom artifact under
`vendor/headroom/` or as `bin/headroom`.

Add:

- `integrations/headroom.lock` with version, source URL, SHA-256, and protocol;
- packaging manifest entries for the bundled artifact;
- `carina context doctor` or `daemon.doctor` probe output;
- release script validation that the pinned artifact exists and matches hash.

Do not install Headroom into the user's global Python, npm, pipx, or uv
environment. Carina should be self-contained.

Because the CLI currently ships from the Python package, the release design must
choose one of:

- upstream standalone Headroom artifacts;
- a Carina-vendored virtual environment with pinned wheels and hashes;
- a small upstream `headroom-carina` sidecar artifact with no global install.

Do not silently download Python packages at daemon startup.

## Upstream Ownership Contract

Ask Headroom upstream to maintain:

- `headroom carina serve --stdio`;
- protocol version negotiation;
- stable JSON schema for compress/retrieve/stats/health;
- CCR storage compatibility;
- documented TTL/storage semantics surfaced by stats/health;
- release artifacts suitable for Carina vendoring;
- conformance fixtures that Carina can run in CI.

Carina should maintain:

- Go adapter;
- daemon config;
- audit and policy boundaries;
- release pin and hash verification;
- fallback behavior;
- agent prompt/tool semantics.

## Incremental Rollout

1. Land the `contextengine` interface and noop implementation.
2. Add config parsing, status, and doctor reporting with no runtime dependency.
3. Add managed MCP/proxy process lifecycle behind default-off config.
4. Add native `context_retrieve` action and audit event projection.
5. Add observation compression before transcript insertion.
6. Persist compressed-context metadata in run checkpoints so crash resume does
   not rebuild a different prompt.
7. Add release packaging pin and manifest checks.
8. Switch `auto` to use bundled Headroom only after offline behavior and
   upstream conformance fixtures are stable.

## Tests

Required Carina-side tests:

- noop engine preserves current transcript rendering byte-for-byte;
- compression rewrites only model-facing transcript content, not audit replay;
- pinned observations are not compressed;
- retrieval returns bounded content and records `ContextRetrieved`;
- sidecar failure degrades to noop in `auto` and fails in `headroom`;
- `daemon.doctor` reports bundled version, protocol version, and health;
- release packaging fails on missing or mismatched Headroom artifact;
- config tests cover strict parsing of the new Headroom fields;
- checkpoint tests prove compressed metadata survives daemon restart/resume;
- subagent tests prove child sessions do not inherit broader parent context
  than their attenuated policy permits.

## Risks

- Python distribution can be heavy for a self-contained Carina archive. Prefer
  an upstream standalone artifact or bundled virtual environment with hash pin.
- Headroom may fetch model/runtime assets on first use. Carina needs offline and
  air-gapped behavior before default-on release.
- Proxy mode may receive provider credentials. Keep proxy mode explicit and do
  not make it the default native integration path.
- CCR retention must match Carina privacy expectations. State paths and deletion
  should be session-scoped and visible in `doctor`.
- Compression can hide relevant details. Pinned observations, retrieval, and
  verifier feedback are the guardrails.
