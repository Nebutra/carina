# HMS Memory Integration

Carina can use ShadowWeave HMS as an optional recall provider. The local
governed memory store remains authoritative; HMS is never a policy source,
checkpoint store, or replacement for local delete and rollback semantics.

## Modes

- `off` (default): no HMS requests.
- `hms-shadow`: recall once at fresh-task start, update cached health, never
  change the model prompt.
- `hms-hybrid`: recall once at fresh-task start and append bounded, fenced
  evidence as a pinned low-trust tool observation.

Forked and resumed tasks reuse the checkpointed transcript and never query HMS
again. The evidence is not promoted into the system prompt. Replay therefore
does not depend on later HMS writes, ranking changes, or outages.

HMS projection is separately opt-in. `MemoryWrite` authorizes only the local
mutation; remote disclosure additionally requires both `NetworkAccess` and
`MemoryExternalize`. Local canonical state is committed first and remains the
authority if HMS is unavailable or an externalization request is denied.

## Configuration

The provider is restart-only and should be set and locked in managed/global
deployment configuration so a project-controlled file cannot enable or
redirect it.

```json
{
  "memory_provider": "hms-shadow",
  "memory_hms_endpoint": "https://hms.example.internal",
  "memory_hms_api_key_env": "CARINA_DEPLOYMENT_HMS_TOKEN",
  "memory_hms_bank_key_env": "CARINA_DEPLOYMENT_HMS_BANK_KEY",
  "memory_hms_timeout_ms": 3000,
  "memory_hms_max_evidence": 8,
  "memory_hms_projection_enabled": true,
  "memory_hms_projection_poll_ms": 1000
}
```

The named variables must exist in the daemon environment. The bank key must be
at least 32 bytes of high-entropy secret material. HMAC-derived user/workspace
bank IDs do not expose profile names, user IDs, or filesystem paths.

Remote endpoints must use HTTPS; loopback HTTP is accepted for development.
Redirects are rejected so bearer credentials cannot cross origins. HMS must
also enforce server-side authentication and tenant isolation: a Bearer header
does not make the default HMS tenant extension a multi-tenant ACL.

Projection is disabled by default and cannot be enabled from project config.
It also requires an HMS recall mode rather than `off`. The projection poll
interval is bounded to 100-60000 ms.

## Governed Projection

After an approved local write, Carina records the canonical target state in a
durable desired-state outbox. The document ID and generation are stable across
retries. Updates use HMS `replace` semantics; an empty local target produces an
idempotent delete tombstone. A crash between the local commit and outbox
completion is repaired from local authority on restart. Leases expire, retry
with bounded backoff, and preserve the latest desired generation.

Projection does not inherit `MemoryWrite` approval. Carina first requests
`NetworkAccess` for the configured HMS host and then `MemoryExternalize` for
the opaque bank/document plus target, action, and content revision. Approval is
therefore revision-bound; approving a retain does not approve a later update or
delete. If either decision requires approval, the
outbox remains blocked and no HMS request is sent. Resolve the emitted decision
with `task.action.approve`, or reissue stale/missing decisions after restart:

```bash
carina memory projection-authorize <session_id>
```

Failed documents are never retried implicitly by that command. Inspect
`carina memory status <session_id>`, repair the classified cause, and select the
exact document:

```bash
carina memory projection-retry <session_id> <document_id>
carina memory projection-authorize <session_id>
```

If a synchronous HMS request may have committed but its response could not be
validated, the document enters `reconcile` and later generations stop. HMS
does not expose a conditional revision fence or persistent delete tombstone,
so Carina cannot prove remote quiescence. After confirming externally that all
prior HMS requests stopped, explicitly reseed and reauthorize:

```bash
carina memory projection-reseed <session_id> <document_id> --remote-quiesced
carina memory projection-authorize <session_id>
```

Reseed reports `remote_state_known=false`; it never marks the document
complete. Status exposes attempts, maximum attempts, next retry time, and safe
error codes, never memory content, bank IDs, response bodies, URLs, or secrets.

Denial leaves local memory intact and the projection blocked. Authorization
changes and worker outcomes emit metadata-only `MemoryProjectionChanged`
events. Tokens and memory content are never included in capability resources,
status responses, or audit payloads.

HMS retain requests write one versioned canonical document per local target
using synchronous `replace`. This is deliberate: HMS asynchronous operations
do not provide a conditional revision fence, so an older operation could
otherwise finish after a newer update or delete. Projection has its own longer
execution deadline rather than reusing the bounded recall deadline.

On daemon restart, every pending or previously completed desired projection
returns to blocked state and must pass `NetworkAccess` and
`MemoryExternalize` again. This both prevents an approval
issued for an old endpoint or policy generation from authorizing traffic under
new deployment configuration and rebuilds canonical state into a replacement
HMS endpoint. Contract/authentication errors fail permanently. Known retryable
failures use bounded backoff. Any outcome that may have committed remotely but
cannot be verified enters `reconcile` instead of being retried. Failed items
require the document-specific retry command after the operator repairs the
cause.

## Governance And Evidence

Before recall, Carina requests `NetworkAccess` for the normalized endpoint
hostname (lowercase, without the port). If policy does not return `allowed`,
no request is sent and the local snapshot is
used with an explicit cached degraded reason. The query never appears in the
capability resource.

Carina queries separate opaque user and workspace banks with fixed budgets.
Trace, chunks, source facts, and entity expansion are disabled. Results are
threat-scanned, content-hash deduplicated, stably ordered, capped, and rendered
as `trust="untrusted"` historical evidence. Raw HMS IDs are hashed before
prompt injection. Credentials, queries, response bodies, and evidence text are
not returned by `memory.status`.

Every attempt records a task-scoped `MemoryRecalled` audit event containing
only provider/mode, status, classified reason, evidence count, and adapter
version. It never records the query, token, response body, or evidence text.

Timeouts, authorization failures, non-200 responses, malformed or oversized
JSON, redirects, and policy denial all produce explicit degraded states rather
than empty successful recall. Late responses cannot replace a frozen task
snapshot.

Use `carina memory status <session_id>` to inspect cached provider mode,
endpoint host, adapter version, last recall state, projection counts, and
classified reason. Status performs no network request. Doctor reports only
configuration, credential source, cached health, and projection state; it does
not bypass `NetworkAccess` to probe HMS.

Run shadow mode first against Carina-specific cross-session coding tasks.
Promote to hybrid only after measuring answer quality, false recall, latency,
cost, tenant isolation, and injection behavior. Public benchmark scores are
supporting evidence, not the production gate.
