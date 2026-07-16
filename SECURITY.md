# Security

Carina is an alpha local-first runtime for coding agents. It contains real
security boundaries, but it is not yet a mature production distribution.

## Reporting

For now, report security issues through the repository's private vulnerability
reporting channel if available, or contact the Nebutra maintainers directly.
Do not publish exploit details before maintainers have had a chance to assess
and patch the issue.

## Security Model Summary

Carina routes agent side effects through a capability kernel:

- file access;
- command execution;
- network access;
- secret access;
- patch application;
- plugin execution;
- remote work dispatch.

The kernel decides whether an action is allowed, denied, or requires approval.
Approved effects are recorded in a hash-chained audit log. File writes go
through transactional patch apply/rollback.

## Current Boundaries

Implemented:

- built-in permission profiles;
- approval modes and approval overlays;
- independent risk review for autonomous approvals;
- audit verification;
- secret broker handles instead of raw secret exposure;
- egress allowlists and daemon-side credential injection;
- explicit per-host HTTPS MITM only for configured credential injection;
- optional OS sandbox backends for command execution;
- plugin permission checks and signature support.

## Important Limits

- Carina is not a VM.
- Carina is not a complete container isolation platform by itself.
- OS sandbox behavior depends on host platform and installed tools.
- Policy enforcement assumes commands run through the Carina daemon/toolchain.
- HTTPS MITM credential injection expands trust inside the child process and
  must be enabled only for reviewed hosts.
- Tag-release automation fails closed unless Developer ID signing and Apple
  notarization succeed. Existing releases must still be treated as unsigned
  unless their release page contains the generated notary JSON and signing
  report; repository tests alone are not proof of Apple acceptance.

## Secrets

Secrets should be granted to Carina as handles or daemon-side credentials.
Agent command children should not receive broad process environments containing
raw API keys. Audit logs should contain secret handles and provenance, not
secret values.

## Supported Versions

The current source line targets Carina Runtime `0.6.3`; the TypeScript and
Python SDK packages are independently versioned at `0.2.0`, while the Go SDK
follows the repository tag. Each declares runtime compatibility in its package
API. No stable support window exists yet, so use the latest `main` only if you
are comfortable with source-first alpha behavior.
