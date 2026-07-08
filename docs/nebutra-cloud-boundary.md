# Nebutra Cloud Boundary

Carina is the local-first agent runtime. Nebutra Cloud (云毓智能,
`nebutra.com`) is the product boundary for identity, organization state, and
multi-endpoint sync.

This split is intentional: the local daemon remains the authority for actions
on a repository, while cloud services handle account-level coordination.

## What Stays Local

Carina keeps these responsibilities inside the local runtime:

- capability checks before file, command, network, secret, patch, plugin, or
  worker side effects;
- hash-chained audit logs as the source of truth for what happened;
- transactional patch apply and rollback;
- BYOK provider credentials, with user-supplied keys taking priority;
- local session execution, local workers, and local recovery;
- local governed memory entries unless an explicit future Nebutra memory-sync
  mode is enabled;
- explicit export surfaces such as `audit.export` and `session.items`.

The cloud boundary must not become a bypass around the capability kernel. A
cloud connector can request work, sync metadata, or upload audit bundles, but it
cannot directly grant local file/command/network access.

## What Belongs To Nebutra Cloud

Nebutra Cloud should own:

- user account and organization identity;
- SSO/OIDC integration and role mapping for approvals;
- device or endpoint registration;
- device/node pairing records, approval metadata, and sync hints;
- multi-endpoint session index and handoff metadata;
- policy bundle distribution;
- optional audit-bundle upload and retention;
- optional memory-sync metadata and semantic indexes, when explicitly enabled;
- remote-worker fleet enrollment and routing metadata;
- billing, entitlement, and product analytics outside the local runtime.

## Device And Node Pairing Boundary

Nebutra device/node pairing is an identity and sync surface only. Nebutra may
identify accounts, register devices, record pairing approval metadata, and sync
which paired endpoint is eligible to attach to a session. It must not become a
remote action authority.

The local Gateway remains the authority for repository actions. A paired device
or node can request work only through the existing Gateway role/scope contract,
method descriptors, dynamic scope resolution, local approvals, and capability
kernel. Pairing proves "this Nebutra identity and device are known"; it does
not prove "this device may run arbitrary commands here."

Node command exposure must be declaration-based and filtered:

- nodes declare supported commands and capability surfaces during connection or
  handshake;
- the Gateway filters declarations against method descriptors, negotiated
  role/scopes, platform defaults, dangerous-command policy, plugin policy,
  local approvals, and config allow/deny rules;
- commands not both declared by the node and allowed by local Gateway policy are
  refused before dispatch;
- approval replay must bind to canonical command, cwd, session, node, and
  declared capability context so approval for one action cannot execute another.

Device-hosted or plugin-hosted surfaces should use scoped capability URLs with
short TTLs. These URLs are transport conveniences for a specific route,
capability, node, and session context. They are not reusable Nebutra identity
tokens and should be auditable, revocable, and expired by default.

The local owner token must stay separate from Nebutra identity. A local owner or
admin token unlocks the local daemon boundary only. A Nebutra token identifies a
cloud account, organization role, and device record only. Neither token should
be exchanged for, embedded inside, or treated as a fallback for the other.

## Sync Contract

Current state:

- `nebutra_cloud_endpoint` defaults to `https://nebutra.com`;
- `nebutra_sync_mode` defaults to `off`;
- `off` is the only supported sync mode in the source-first alpha;
- daemon status exposes the configured endpoint and sync mode for observability.

Future sync modes should be added only when the Nebutra connector exists. The
expected progression is:

1. **metadata**: sync endpoint/device/session indexes and status summaries, not
   repository contents or raw secrets.
2. **audit**: upload explicit audit bundles or hash-chain checkpoints with
   redaction rules and retention controls.
3. **handoff**: allow a Nebutra-authenticated endpoint to attach to a session or
   enqueue work through the existing daemon authority path.
4. **memory**: sync selected user/workspace memory metadata through Nebutra
   identity after local owner opt-in. Raw local memory should not sync by
   default; synced entries need scope, provenance, retention, and deletion
   controls.

Raw BYOK keys, local workspace files, and unrestricted transcripts should not
sync by default.

## Integration Points

| Boundary | Carina hook | Nebutra responsibility |
|---|---|---|
| Identity | `go/daemon/identity.go` `IdentityProvider` | Resolve OIDC/SSO access tokens to users and roles. |
| Model auth | `CARINA_NEBUTRA_TOKEN` OAuth fallback | Provide managed Nebutra access tokens only when BYOK keys are absent. |
| Config | `nebutra_cloud_endpoint`, `nebutra_sync_mode` | Define which Nebutra product endpoint a future connector talks to. |
| Memory | local `memory` / `user` targets and `MemoryWrite` audit metadata | Provide identity, scope mapping, sync policy, retention, and deletion controls for any future memory sync. |
| Device/node pairing | Gateway role/scope handshake and method descriptors | Register device identity and pairing metadata without granting local action authority. |
| Audit | `audit.export`, `session.items` | Store, search, and present synced bundles without rewriting local history. |
| Remote workers | worker registry and work-dispatch lease protocol | Enroll endpoints and route work metadata without bypassing local policy. |

## Security Rules

- Sync must be opt-in.
- The local daemon must be able to run with `nebutra_sync_mode=off`.
- Nebutra OAuth is a fallback identity path; BYOK API keys remain higher
  priority for model providers.
- Cloud-originated work must enter through the same RPC and approval paths as
  local work.
- Nebutra pairing must never bypass Gateway role/scope checks, method
  descriptors, dynamic scope resolution, local approvals, or the capability
  kernel.
- Device and node commands must be declared, filtered, and bounded before they
  can be dispatched.
- Capability URLs must be scoped, short-lived, auditable, and revocable.
- Nebutra identity tokens and local owner/admin tokens must remain separate
  trust domains.
- Audit sync should preserve local event hashes instead of rewriting events.
- Memory sync should preserve local provenance and deletion semantics instead
  of silently merging unscoped facts across users or workspaces.
- Secrets should sync only as handles or provenance, never as raw values.
