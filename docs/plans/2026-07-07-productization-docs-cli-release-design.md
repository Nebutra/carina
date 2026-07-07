# Productization Pass: README, CLI Help, Docs, and Release Checks

## Goal

Move Carina from an absorption/research-oriented repository into a source-first
alpha product repository that a new user can understand in a few minutes.

This pass does not add runtime capability. It improves positioning, user-facing
commands, documentation, and verification packaging.

## README Strategy

The README should lead with use case and value, not implementation language:

- what Carina is;
- who should use it;
- what workflow it enables;
- what is implemented today;
- what is intentionally not done yet.

The hero image stays. Go/Rust/Zig belongs in architecture, not the first product
argument. Competitive positioning must be objective and scenario-based: editor
assistants, CLI coding agents, terminal-first multi-provider agents, and cloud
sandbox runtimes solve adjacent but different jobs. The README should not claim
that a competitor lacks a capability unless that claim is tied to an official
source and still current.

English is canonical. Chinese and Japanese READMEs should keep the same facts
and structure, with natural wording rather than literal machine translation.

## CLI Help

`carina --help` should be a user map, not a command dump. Group commands by
workflow:

- start and run;
- inspect sessions;
- audit and rollback;
- approvals, secrets, plugins;
- providers and BYOK;
- native tools;
- daemon note.

User-visible output should consistently use `carina`. Historical `pi` naming
may remain in research notes only; it should not appear in CLI help, README, or
product docs.

## Product Docs

Add product-facing docs:

- `docs/product.md`: use cases, value, non-goals, objective alternative
  positioning, alpha limits.
- `docs/release.md`: source-first alpha release flow, artifacts, versioning,
  future signed release/Homebrew checklist.
- `SECURITY.md`: current security boundary, limitations, secret handling,
  egress MITM scope, reporting channel placeholder.
- `CONTRIBUTING.md`: build/test matrix and contribution expectations.

## Release Check

Add a lightweight `scripts/release-check.sh` that runs the source-first release
validation path without publishing artifacts:

- build all components;
- run Go tests;
- run Rust tests;
- build Zig tools;
- run targeted Go race coverage for control-plane risk areas.

The script is a local release gate, not a public release system.

## Verification

Run at least:

- `go test ./go/... ./apps/...`
- `cargo test`
- `go test -race ./go/daemon ./go/config ./apps/carina-daemon`
- `bash scripts/release-check.sh` if runtime is acceptable in the current
  environment.
