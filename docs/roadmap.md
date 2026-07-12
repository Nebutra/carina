# Roadmap

Carina is an alpha Agent Runtime. Repository-owned product work identified in
the July 2026 productization audit is closed on `main`; this document now tracks
only activation, publication, and validation work that depends on external
services, credentials, hardware, tenants, or repository-administrator access.
It does not assign committed dates.

## Repository Closure

The source tree and release workflows include:

- macOS and Linux `arm64`/`amd64` archives, Linux deb/rpm packages, and a
  Windows `carina-worker` package;
- Homebrew tap and Linuxbrew Formula rendering plus install/upgrade contracts;
- npm launcher/native package assembly with immutable retry bundles and OIDC
  trusted-publishing support;
- a checksum-enforcing shell installer;
- non-root daemon and worker container images that are built in CI;
- packaged VS Code and static Web Operator clients with checksums and release
  provenance;
- TypeScript, Python, and Go typed SDK parity for common session, workspace,
  patch, command, audit, workflow, worker, approval, checkpoint, artifact, and
  event-stream operations;
- remote executor token accounting, idempotent scheduler rollups, Unix process
  groups, and Windows kill-on-close Job Object containment;
- production remote-worker deployment guidance and hardened systemd examples;
- audit durability tests and a measured EPS/p99 performance decision gate;
- security policy, contributor guidance, issue forms, and a pull-request
  checklist.

The Nebutra Cloud boundary remains intentionally disabled by default. Carina
does not invent a cloud protocol before the external service contract exists,
and local policy, audit, approval, and rollback remain authoritative.

## External Activation

| Work | Why it is external | Completion evidence |
| --- | --- | --- |
| Apple signing and notarization | Requires Developer ID certificate, Apple notary account, protected GitHub environment, secrets, and approvers | Both Darwin assets have Accepted notary JSON, verified Team ID, signature, and Gatekeeper reports |
| npm public packages | Requires `@nebutra` package ownership, `npm-release` environment, and five trusted-publisher bindings | OIDC-only `npm publish --provenance` succeeds and immutable package integrity matches the release bundle |
| Homebrew Core `brew install carina` | Requires a Homebrew Core formula submission and upstream review; `Nebutra/tap/carina` is already the maintained channel | Core formula is merged and clean-machine install/upgrade passes on macOS and Linux |
| VS Code Marketplace / Open VSX | Requires publisher identities, marketplace tokens/OIDC support, listings, and review | Published extension digest matches the release VSIX and installs on a clean profile |
| Container registry | Requires registry namespace, credentials/OIDC, retention, signing policy, and public visibility decision | Multi-arch daemon/worker manifests, provenance, SBOM, and signature verification are public |
| Hosted Web Operator and installer URL | Requires DNS, TLS, hosting/CDN, origin allowlists, and operating ownership | Hosted assets match release digests and WSS/origin/caching checks pass |
| Real provider and terminal matrix | Requires paid provider credentials plus representative terminal/OS hardware | Recorded canaries cover CJK input, narrow layouts, reconnect, approval/question flows, and provider streaming |
| Nebutra Cloud connector activation | Requires a versioned API contract, staging tenant, OIDC/device identity, client credentials, retention/redaction policy, and service SLOs | Contract tests pass against staging and sync/handoff is opt-in, revocable, audited, and local-authority preserving |
| GitHub governance | Requires repository administrator access | Branch protection, required checks/reviews, private vulnerability reporting, and environment protection are enabled |
| Bundled Headroom artifacts | Requires reproducible upstream binaries and checksums for every supported release target | All platform artifacts are pinned, verified, and packaged without `SKIP_HEADROOM=1` |
| Public release promotion | Requires release/tag write permission and the external gates above | A non-draft tag release passes the immutable full-asset verification path |

## Release Credentials

Secret values must never be committed. Apple automation expects these secrets
in a protected `codesigning` GitHub environment:

- `APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64`;
- `APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD`;
- `APPLE_DEVELOPER_ID_APPLICATION_IDENTITY`;
- `APPLE_NOTARY_APPLE_ID`;
- `APPLE_NOTARY_TEAM_ID`;
- `APPLE_NOTARY_PASSWORD`.

The npm bootstrap requires public ownership of `@nebutra/carina` and its four
native packages, a protected `npm-release` environment, and trusted-publisher
bindings to `Nebutra/carina` / `release.yml`. Set
`NPM_TRUSTED_PUBLISHERS_CONFIRMED=true` only after all five bindings are
verified. Do not add long-lived token fallbacks.

The Homebrew tap update requires `HOMEBREW_TAP_DEPLOY_KEY`. Homebrew Core
acceptance is a separate upstream process and is not implied by tap readiness.

## Deliberate Non-Goals

- A Windows desktop daemon/CLI is not claimed; the supported Windows artifact
  is the contained remote worker.
- Carina is not a VM or a replacement for an executor's workspace/container
  isolation.
- Cloud sync is not enabled until the Nebutra service contract and tenant
  controls exist.
- Release scripts and local tests are not evidence that an external registry,
  marketplace, Apple service, or hosted deployment accepted an artifact.

See [release operations](release.md), [remote workers](deployment/remote-workers.md),
and the [Nebutra Cloud boundary](nebutra-cloud-boundary.md).
