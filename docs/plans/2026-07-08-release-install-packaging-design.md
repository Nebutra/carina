# Release And Install Packaging Closure

## Decision

Add local release packaging without claiming public distribution channels are
live.

This pass turns a source checkout into a reproducible local release candidate:

- `make release-package` builds current-platform binaries;
- `scripts/package-release.sh` assembles `dist/` archives, checksums, and a
  machine-readable manifest;
- the manifest records split version sources instead of hiding them;
- `SKIP_BUILD=1` and `SKIP_ZIG=1` are explicit escape hatches and are recorded
  as warnings;
- Homebrew and npm receive templates only, with clear publish-time placeholders;
- docs explain how to verify the generated package.

## Non-Goals

- No GitHub release publishing.
- No signed artifacts.
- No published Homebrew tap.
- No published npm package.
- No hosted installer.

## Package Contents

The local archive contains:

- Go CLIs: `carina`, `carina-daemon`, `carina-worker`, `carina-tui`;
- Rust kernel service: `carina-kernel-service`;
- Zig native tools matching `zig/zig-out/bin/carina-*`;
- README, LICENSE, SECURITY, release docs, roadmap;
- `MANIFEST.json`, `VERSION_CHECK.txt`, and per-file checksums.

## Version Policy

`VERSION` may be supplied by the release operator. If omitted, the package
version defaults to the CLI version. The script records CLI, daemon, Cargo,
TypeScript SDK, and Python SDK versions and emits warnings when they differ from
the package version.

## Verification

Run:

```bash
make release-check
make release-package
```

Then verify the archive checksum and smoke-test `bin/carina --version` from the
extracted package.
