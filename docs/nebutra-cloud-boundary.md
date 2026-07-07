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
- explicit export surfaces such as `audit.export` and `session.items`.

The cloud boundary must not become a bypass around the capability kernel. A
cloud connector can request work, sync metadata, or upload audit bundles, but it
cannot directly grant local file/command/network access.

## What Belongs To Nebutra Cloud

Nebutra Cloud should own:

- user account and organization identity;
- SSO/OIDC integration and role mapping for approvals;
- device or endpoint registration;
- multi-endpoint session index and handoff metadata;
- policy bundle distribution;
- optional audit-bundle upload and retention;
- remote-worker fleet enrollment and routing metadata;
- billing, entitlement, and product analytics outside the local runtime.

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

Raw BYOK keys, local workspace files, and unrestricted transcripts should not
sync by default.

## Integration Points

| Boundary | Carina hook | Nebutra responsibility |
|---|---|---|
| Identity | `go/daemon/identity.go` `IdentityProvider` | Resolve OIDC/SSO access tokens to users and roles. |
| Model auth | `CARINA_NEBUTRA_TOKEN` OAuth fallback | Provide managed Nebutra access tokens only when BYOK keys are absent. |
| Config | `nebutra_cloud_endpoint`, `nebutra_sync_mode` | Define which Nebutra product endpoint a future connector talks to. |
| Audit | `audit.export`, `session.items` | Store, search, and present synced bundles without rewriting local history. |
| Remote workers | worker registry and work-dispatch lease protocol | Enroll endpoints and route work metadata without bypassing local policy. |

## Security Rules

- Sync must be opt-in.
- The local daemon must be able to run with `nebutra_sync_mode=off`.
- Nebutra OAuth is a fallback identity path; BYOK API keys remain higher
  priority for model providers.
- Cloud-originated work must enter through the same RPC and approval paths as
  local work.
- Audit sync should preserve local event hashes instead of rewriting events.
- Secrets should sync only as handles or provenance, never as raw values.

