# Nebutra Device/Node Pairing Boundary

Source review: `docs/research/openclaw-gateway-source-review.md`.
Builds on cloud boundary: `docs/nebutra-cloud-boundary.md`.
Builds on Gateway phases A-C:
`docs/plans/2026-07-07-openclaw-gateway-phase-a-design.md`,
`docs/plans/2026-07-07-openclaw-gateway-phase-b-design.md`, and
`docs/plans/2026-07-07-openclaw-gateway-phase-c-design.md`.

## Decision

Document the future Nebutra device/node pairing shape without adding runtime
code. Pairing belongs to Nebutra identity and sync, while local side effects
remain under the local Gateway and capability kernel.

Nebutra may own account identity, organization role mapping, device records,
pairing approval metadata, endpoint presence, and sync hints. It must not own
command execution authority for a repository.

## Boundary Rules

- A paired Nebutra device is an identified endpoint, not a local owner.
- Cloud pairing metadata can authorize an endpoint to attempt attachment only;
  local Gateway role/scope negotiation still decides what methods are visible.
- Node commands must be declared at connect or handshake time and filtered
  against Gateway descriptors, dynamic scope rules, dangerous-command policy,
  plugin policy, local approvals, and config allow/deny.
- Undeclared commands, commands outside the negotiated role/scope envelope, and
  commands rejected by local policy fail before dispatch.
- Device-hosted and plugin-hosted surfaces use scoped capability URLs with short
  TTLs, route/session/node binding, auditability, and revocation.
- Nebutra identity tokens and local owner/admin tokens stay separate. Neither
  token is a fallback, wrapper, or exchange format for the other.

## Future Config Skeleton

No config is implemented in this slice. If pairing ships later, the config
surface should stay default-off and narrow:

- `nebutra_pairing_mode`: default `off`; future non-off modes require an
  implemented Nebutra connector and explicit operator opt-in.
- `nebutra_capability_url_ttl`: maximum TTL for scoped device/plugin capability
  URLs; should be short and capped by local policy.
- `nebutra_node_command_allow`: optional local allowlist layered after node
  declaration and Gateway descriptor checks.
- `nebutra_node_command_deny`: local denylist that wins over declaration,
  pairing, and cloud metadata.

These keys are design placeholders, not accepted runtime settings.

## Non-Goals

- No new Nebutra connector.
- No code or config parser changes.
- No owner-token changes.
- No long-lived device capability URLs.
- No direct cloud-to-command dispatch.
- No bypass around Gateway descriptors, dynamic scope resolution, local
  approvals, audit, or the capability kernel.

## Validation

When this moves beyond docs, acceptance should prove:

- sync remains opt-in and disabled by default;
- cloud pairing metadata cannot grant owner/admin authority;
- local Gateway policy rejects undeclared or disallowed node commands;
- capability URLs expire quickly and bind to route, session, node, and scope;
- audit records preserve the Nebutra identity, local Gateway decision, and
  capability-kernel result as separate facts.
