# Release Process

Carina is currently an alpha with automated macOS GitHub releases and an
official Homebrew tap. This document describes the local release gate and the
tag-driven public release path. Future channels are tracked in
[docs/roadmap.md](roadmap.md).

## Current Source-First Release Gate

Before tagging or publishing anything from this repository, run:

```bash
make release-check
```

The script validates:

- Go app builds;
- Rust workspace checks/tests;
- Zig native tool build;
- Go package tests;
- targeted race coverage for the daemon/config control plane;
- Homebrew Formula template rendering.

Manual equivalent:

```bash
make all
go test ./go/... ./apps/...
cargo test
go test -race ./go/daemon ./go/config ./apps/carina-daemon
```

## Local Release Package

Build a current-platform release candidate with:

```bash
make release-package
```

To force a release version:

```bash
VERSION=0.6.0 make release-package
```

The package command writes to `dist/`:

- `carina_<version>_<goos>_<goarch>.tar.gz`;
- `carina_<version>_<goos>_<goarch>.tar.gz.sha256`;
- `SHA256SUMS`;
- extracted staging directory with `MANIFEST.json`, `VERSION_CHECK.txt`, and
  `checksums.txt`.

The archive includes Go CLIs, the Rust kernel service, Zig native tools matching
`zig/zig-out/bin/carina-*`, README, LICENSE, SECURITY, and release docs. It
smoke-tests `bin/carina --version` from the staged package.

Headroom is an upstream-maintained component pinned by
`integrations/headroom.lock`. Release builders must provide the prepared
platform executable at the lockfile's `bundle_path`; `package-release.sh`
verifies the pinned SHA-256 and fails the package if the artifact is missing or
does not match. The daemon does not download Headroom at startup and does not
install anything into the user's global Python, npm, pipx, or uv environment.

`VERSION_CHECK.txt` records CLI, daemon, Rust workspace, TypeScript SDK, and
Python SDK versions, plus the bundled Headroom pin and source artifact. Version
mismatches are warnings in the package manifest, not hidden state.

Use existing artifacts without rebuilding:

```bash
SKIP_BUILD=1 VERSION=0.6.0 ./scripts/package-release.sh
```

If Zig is unavailable but `zig/zig-out/bin/carina-*` artifacts already exist,
reuse them explicitly:

```bash
SKIP_ZIG=1 VERSION=0.6.0 make release-package
```

`SKIP_BUILD=1` and `SKIP_ZIG=1` are recorded as warnings in `MANIFEST.json` and
`VERSION_CHECK.txt`.

`SKIP_HEADROOM=1` packages without the optional Headroom integration and records
that decision in the manifest. The Homebrew release uses this mode because
Headroom does not yet publish a reproducible standalone executable for both
supported macOS architectures. In `context_engine=auto`, Carina safely falls
back to the noop context engine.

Verify an archive:

```bash
cd dist
shasum -a 256 -c carina_<version>_<goos>_<goarch>.tar.gz.sha256
tar -xzf carina_<version>_<goos>_<goarch>.tar.gz
./carina_<version>_<goos>_<goarch>/bin/carina --version
./carina_<version>_<goos>_<goarch>/bin/carina-daemon --context-engine=off &
./carina_<version>_<goos>_<goarch>/bin/carina context doctor
```

## Current Artifacts

`make all` writes local binaries into `bin/`:

- `carina`
- `carina-daemon`
- `carina-worker`
- `carina-tui`
- `headroom` in release packages only, pinned by `integrations/headroom.lock`
- Zig tools from `zig/zig-out/bin`
- Rust `carina-kernel-service` under `target/release`

These are local build outputs, not public release artifacts.

## Versioning

Current version declarations are split while the project is alpha:

- CLI version: `apps/carina-cli/main.go`
- daemon version: `go/daemon/daemon.go`
- Rust workspace version: `Cargo.toml`
- SDK package versions under each SDK directory

A public release should align these or document why they differ.

## Automated macOS Release

Pushing a tag matching `v<major>.<minor>.<patch>` runs
`.github/workflows/release.yml`. The workflow:

- requires the tag version to match the CLI version and the tag commit to be on
  `main`;
- builds on native Apple Silicon and Intel GitHub-hosted runners;
- installs each archive through a temporary Homebrew tap and runs `brew test`;
- publishes archives, per-archive checksums, and `SHA256SUMS`;
- creates GitHub build provenance attestations;
- renders and pushes `Formula/carina.rb` to `Nebutra/homebrew-tap` through a
  repository-scoped SSH deploy key.

The release is rejected before publication if either architecture fails to
build or install.

## Homebrew Channel

Install the published Formula with:

```bash
brew install Nebutra/tap/carina
```

Upgrade with `brew update && brew upgrade carina`. The Formula source template
is `packaging/homebrew/carina.rb.template`; `scripts/render-homebrew-formula.sh`
injects versioned release URLs and both architecture checksums.

## Not Yet Implemented

- hosted installer;
- published npm install package;
- Apple code signing and notarization;
- SBOM publication and provenance verification documentation;
- Linux release and Linuxbrew path;
- Windows release path.
