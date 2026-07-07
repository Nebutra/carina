# OpenClaw Gateway Source Review

Source reviewed: `openclaw/openclaw` `main` at
`e72dadbb3bb15e7baa42a6bb91514749c3f3aaf9` (2026-07-07).

Scope: Gateway only. The repository is very large and noisy; this review ignores
ordinary UI/product sprawl and focuses on the mechanism that is genuinely
valuable: the Gateway as a cross-device, multi-surface control plane.

Primary files reviewed:

- `docs/gateway/index.md`
- `docs/gateway/protocol.md`
- `docs/gateway/openai-http-api.md`
- `docs/gateway/tools-invoke-http-api.md`
- `docs/gateway/operator-scopes.md`
- `src/gateway/server-http.ts`
- `src/gateway/server-methods.ts`
- `src/gateway/method-scopes.ts`
- `src/gateway/methods/core-descriptors.ts`
- `src/gateway/auth.ts`
- `src/gateway/auth-resolve.ts`
- `src/gateway/server/ws-connection.ts`
- `src/gateway/server/ws-connection/message-handler.ts`
- `src/gateway/server/ws-connection/auth-context.ts`
- `src/gateway/server/ws-connection/connect-policy.ts`
- `src/gateway/tools-invoke-http.ts`
- `src/gateway/tools-invoke-shared.ts`
- `src/gateway/openai-http.ts`
- `src/gateway/openresponses-http.ts`
- `src/gateway/plugin-node-capability.ts`
- `src/gateway/server/plugins-http.ts`
- `src/plugin-sdk/gateway-method-runtime.ts`
- `src/plugins/runtime/gateway-request-scope.ts`
- `apps/shared/OpenClawKit/.../GatewayChannel.swift`
- `apps/android/.../gateway/GatewaySession.kt`

## Verdict

OpenClaw's strongest reusable idea is not a particular TypeScript file, nor the
brand/product shell. It is the Gateway philosophy:

> a local-first assistant runtime should expose one explicit, authenticated,
> role/scoped, multi-protocol control plane, and every local, remote, mobile,
> plugin, HTTP-compatibility, and node capability surface should pass through
> that control plane instead of each subsystem inventing its own back door.

Carina already has stronger low-level safety primitives than OpenClaw: a kernel
capability model, audit chain, approval overlays, transactional patching, secret
broker, egress boundary, MCP server/client, and durable work dispatch. The gap
is product/control-plane shape: Carina does not yet have a first-class Gateway
surface that unifies WebSocket clients, OpenAI-compatible HTTP, tool invoke,
remote devices, plugin HTTP routes, and scoped node capabilities.

The valuable absorption target is therefore architectural, not a port.

## Mechanism Analysis

### 1. One-port, multi-protocol runtime

OpenClaw Gateway runs one always-on process and multiplexes:

- WebSocket RPC/control protocol;
- HTTP OpenAI-compatible endpoints: `/v1/models`, `/v1/embeddings`,
  `/v1/chat/completions`, `/v1/responses`;
- direct tool invoke: `/tools/invoke`;
- plugin HTTP routes and plugin WebSocket upgrades;
- Control UI static/media routes;
- health/readiness probes.

The source confirms this is not accidental routing. `server-http.ts` builds an
ordered request-stage pipeline:

1. liveness/readiness probes;
2. hooks;
3. built-in OpenAI-compatible routes;
4. `/tools/invoke`;
5. session routes;
6. plugin-node capability auth;
7. plugin HTTP routes;
8. media routes;
9. Control UI catch-all.

Core routes have precedence over plugin and UI routes. That is a good boundary:
plugins cannot shadow `/v1/*`, `/tools/invoke`, session control, or Gateway
core surfaces.

### 2. Gateway protocol is a real handshake, not "token then JSON-RPC"

`docs/gateway/protocol.md` and `ws-connection/message-handler.ts` show a proper
WebSocket protocol:

- server sends `connect.challenge` with nonce and timestamp;
- first client frame must be `connect`;
- client declares protocol range, client identity, role, scopes, caps,
  commands, permissions, auth, locale, user agent, and device signature;
- server returns `hello-ok` with protocol version, server version/conn id,
  feature lists, initial snapshot, negotiated role/scopes, optional device
  token, and payload/buffer/tick policy.

This is a useful pattern. The handshake is where transport identity, device
identity, protocol compatibility, runtime feature discovery, and authorization
become one contract.

Carina currently has JSON-RPC and daemon sessions, but no equivalent
role/scoped WebSocket client protocol with a formal `hello-ok` capability
contract.

### 3. Roles and scopes are method-level, descriptor-backed, and fail closed

OpenClaw separates:

- `operator` role: CLI/UI/automation/control clients;
- `node` role: capability hosts such as mobile/desktop/headless nodes.

The useful part is not the exact scope names. It is the descriptor model:

- `core-descriptors.ts` is the canonical method policy table;
- every core method must have a descriptor, or startup fails;
- descriptors carry method name, scope, advertise flag, startup availability,
  and control-plane-write status;
- `method-scopes.ts` resolves static and dynamic scope requirements;
- unknown/unclassified methods default-deny;
- plugin methods default to admin unless explicitly described;
- reserved method prefixes such as `config.*`, `wizard.*`, `update.*`, and
  `exec.approvals.*` are coerced to admin even if a plugin declares a weaker
  scope.

The best detail is param-sensitive least privilege. `sessions.patch` allows
write-scoped mutation only for safe chat-organization fields such as label,
category, pinned, archived, and unread. Unknown or sensitive fields require
admin. That is much better than "PATCH equals write".

Carina should absorb this into its daemon RPC surface: every RPC method should
have an explicit descriptor with role/scope, startup availability, and
control-plane-write rate limit metadata. Dynamic/param-sensitive scope should be
supported for methods that mutate mixed-risk resources.

### 4. Auth boundary is pragmatic and mostly honest

OpenClaw supports `none`, token, password, trusted-proxy, Tailscale, device
token, and bootstrap token flows. Important implementation details:

- default auth does not silently become `none`; missing token produces a
  startup diagnostic;
- forwarded headers disqualify loopback requests from being treated as clean
  local direct requests;
- Tailscale/proxy auth is not treated as generic HTTP bearer auth;
- failed auth has per-IP/per-surface rate limiting;
- unauthenticated WebSocket upgrades are capped by a preauth connection budget;
- shared-secret HTTP bearer is explicitly documented as full trusted-operator
  access, not a narrow user/session scope.

The last point is both a strength and a risk. It is honest, but Carina should
not copy it as the default for public product surfaces. If Carina adds
OpenAI-compatible HTTP and `/tools/invoke`, it should issue explicit scoped
Gateway tokens/capabilities instead of treating one bearer token as full owner
power unless the operator opts into that local-only mode.

### 5. OpenAI-compatible HTTP is agent-first, not provider-first

OpenClaw's `/v1/*` facade maps OpenAI client expectations onto the Gateway's
agent model:

- `model=openclaw` or `openclaw/default` targets the configured default agent;
- `openclaw/<agentId>`, `openclaw:<agentId>`, and `agent:<agentId>` target an
  explicit agent;
- `/v1/models` lists agent targets, not raw backend provider models;
- `x-openclaw-model` is the backend provider/model override and is admin/owner
  gated for identity-bearing HTTP callers;
- `x-openclaw-session-key` is rejected for reserved internal namespaces;
- Chat Completions and OpenResponses are translated into normal agent runs;
- `tool_choice` is not merely passed through; the exposed client tools are
  narrowed and the result is validated after the run;
- `/v1/responses` remembers `previous_response_id -> sessionKey` in a bounded,
  scoped, in-memory map to preserve continuity.

This is a strong product decision. The public compatibility surface stays
"talk to my agent" rather than "pick a raw provider model from my private
provider config." Carina should absorb that philosophy if it exposes OpenAI
compatibility.

### 6. `/tools/invoke` is useful, but dangerous by design

OpenClaw exposes direct tool invocation over HTTP and RPC. The valuable parts:

- one shared implementation backs HTTP and RPC;
- request shape normalizes `tool/name`, `action`, `args`, `sessionKey`,
  `agentId`, and `idempotencyKey`;
- tool availability goes through the same policy chain as agent-visible tools;
- plugin approval refusals are returned as structured blocked results;
- HTTP docs explicitly call this full operator access;
- HTTP has a default deny list for direct RCE/filesystem/control-plane tools:
  `exec`, `spawn`, `shell`, `fs_write`, `fs_delete`, `fs_move`,
  `apply_patch`, `sessions_spawn`, `sessions_send`, `cron`, `gateway`,
  `nodes`.

For Carina, direct tool invoke should be absorbed only after it is mapped onto
the kernel capability model. The default should be stricter than OpenClaw:
direct HTTP tool invocation should require a scoped capability token and should
deny process execution, patching, filesystem writes, session injection, node
relay, and Gateway mutation unless an explicit local owner policy enables them.

### 7. Plugin HTTP routes are powerful without being ambient authority

OpenClaw plugins can register HTTP routes and upgrades. The useful guardrails:

- plugin routes run after core routes;
- protected plugin route paths require Gateway auth unless explicitly bypassed
  by activated channel-plugin artifacts;
- route handlers receive a derived runtime client with caller scopes;
- routes fail closed when auth/scope context is missing;
- plugin code cannot call arbitrary Gateway methods from ambient runtime code;
- `dispatchGatewayMethod` works only inside request-local
  `AsyncLocalStorage` scope and only when the route declares
  `gatewayMethodDispatchAllowed`.

This request-local scope model is worth absorbing conceptually. Carina's plugin
runtime is different, but any future plugin HTTP surface should have the same
rule: no ambient Gateway calls; Gateway dispatch is allowed only from an
authenticated route context with explicit declared contract and inherited
caller scope.

### 8. Nodes are capability hosts, not just remote workers

OpenClaw nodes declare capabilities and commands at connect time. The Gateway:

- records connected and paired nodes;
- keeps operator and node roles separate;
- filters declared commands against platform defaults, dangerous command policy,
  plugin policy, runtime approvals, and config allow/deny;
- refuses commands not both declared by the node and allowed by Gateway policy;
- handles mobile/offline foreground-restricted actions with a bounded pending
  queue;
- issues plugin surface capability URLs with short TTLs;
- lets plugin-hosted node routes authenticate by bearer first, then by scoped
  capability token fallback;
- binds `system.run` approval replay to canonical command/cwd/session/node
  context, preventing "approve A, execute B" mutation.

This is the part closest to "OpenClaw Gateway is real." It treats remote mobile
or desktop clients as capability-bearing devices under a Gateway-owned policy,
not as generic workers with full trust.

Carina already has a work-dispatch bridge. The missing piece is device/node
role protocol: durable device identity, command declaration, pairing approval,
bounded node command allowlists, and capability URLs for cross-device plugin
surfaces. If Carina builds multi-endpoint Nebutra sync, it should follow this
shape but put brand/cloud boundary under Nebutra (云毓智能, `nebutra.com`).

## What Not To Absorb

- Do not copy the TypeScript mega-surface. The useful model is smaller than the
  implementation. Carina's Go daemon and Rust kernel are better foundations.
- Do not copy shared-secret bearer as the default public trust model. OpenClaw
  documents it honestly, but Carina should prefer scoped Gateway capabilities.
- Do not copy the huge product/UI route matrix. Carina should add staged
  routing only for surfaces it actually ships.
- Do not make plugin HTTP an all-powerful in-process escape hatch. Absorb the
  request-local, authenticated dispatch contract only.
- Do not move multi-device identity/sync into generic Carina config without a
  product boundary. That belongs to Nebutra Cloud if it is built.

## Carina Absorption Candidates

Recommended order:

1. **Gateway method descriptor registry.** Add a daemon RPC descriptor table
   with method, role, scope, advertise, startup availability, and
   control-plane-write metadata. Default-deny unclassified RPC methods. Add
   dynamic/param-sensitive scope for mixed-risk methods.
2. **Role/scoped WebSocket Gateway protocol.** Add a single Gateway endpoint
   around existing daemon sessions: challenge -> connect -> hello-ok, with
   negotiated role/scopes, feature discovery, snapshot, max payload/buffer, and
   tick policy.
3. **OpenAI-compatible local HTTP facade.** Add `/v1/models`,
   `/v1/chat/completions`, `/v1/responses`, and later `/v1/embeddings` as
   agent-first endpoints: `carina`, `carina/default`, `carina/<agent>`.
   Backend provider override should be owner/admin gated and validated against
   the provider catalog visibility policy.
4. **Scoped `/tools/invoke`.** Expose direct tool invoke only through kernel
   capabilities and a hard deny list. This should not bypass Carina's approval,
   audit, or policy stack.
5. **Plugin route request scope.** If Carina adds plugin HTTP routes, require
   route-auth context and request-local inherited scope before plugin code can
   call daemon/Gateway methods.
6. **Device/node role protocol.** For Nebutra multi-endpoint work, add device
   identity, pairing/approval, node command declaration, command allowlist, and
   short-TTL capability URLs for device-hosted/plugin-hosted surfaces.

## Current Carina Gap Summary

Carina already covers:

- capability kernel and approvals;
- audit/session event projection;
- BYOK/provider catalog;
- MCP interop;
- egress boundary and credential injection;
- durable background runs and work dispatch;
- Nebutra Cloud product boundary with sync off by default.

Carina lacks the OpenClaw-style Gateway product surface:

- no formal role/scoped WebSocket handshake;
- no Gateway method descriptor table covering every daemon RPC method;
- no OpenAI-compatible agent-first local HTTP facade;
- no direct HTTP tool invoke with kernel-mapped capability tokens;
- no plugin HTTP route request-scope contract;
- no device/node pairing and command declaration protocol.

## Bottom Line

OpenClaw's Gateway is worth absorbing by philosophy. The precise implementation
is too broad and too entangled to port. The Carina version should be smaller,
kernel-first, auditable, and Nebutra-boundary-aware:

- one explicit Gateway surface;
- every method classified;
- every HTTP compatibility surface scoped;
- every plugin/remote/device route authenticated through the same control
  plane;
- no ambient full-power plugin or bearer shortcuts unless the local owner
  explicitly opts in.
