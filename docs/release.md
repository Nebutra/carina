# Release Process

Carina is currently a source-first alpha. This document describes the local
release gate and the intended path to signed public releases. Install-channel
planning lives in [docs/roadmap.md](roadmap.md).

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
- targeted race coverage for the daemon/config control plane.

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

`VERSION_CHECK.txt` records CLI, daemon, Rust workspace, TypeScript SDK, and
Python SDK versions. Mismatches are warnings in the package manifest, not hidden
state.

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

Verify an archive:

```bash
cd dist
shasum -a 256 -c carina_<version>_<goos>_<goarch>.tar.gz.sha256
tar -xzf carina_<version>_<goos>_<goarch>.tar.gz
./carina_<version>_<goos>_<goarch>/bin/carina --version
```

## Current Artifacts

`make all` writes local binaries into `bin/`:

- `carina`
- `carina-daemon`
- `carina-worker`
- `carina-tui`
- Zig tools from `zig/zig-out/bin`
- Rust `carina-kernel-service` under `target/release`

These are local build outputs, not signed release artifacts.

## Versioning

Current version declarations are split while the project is alpha:

- CLI version: `apps/carina-cli/main.go`
- daemon version: `go/daemon/daemon.go`
- Rust workspace version: `Cargo.toml`
- SDK package versions under each SDK directory

A public release should align these or document why they differ.

## Public Release Checklist

Before a non-source public release:

- decide release version and changelog;
- run `make release-check`;
- build macOS and Linux artifacts;
- produce checksums;
- sign artifacts;
- attach provenance/SBOM where available;
- publish release notes;
- update installer/Homebrew tap;
- update npm install package when applicable;
- verify install from a clean machine.

## Install Channel Templates

Templates are checked in but not published:

- Homebrew formula template:
  `packaging/homebrew/carina.rb.template`;
- npm installer package template:
  `packaging/npm/package.json.template`.

Planned channels are tracked in [docs/roadmap.md](roadmap.md). A rendered
Homebrew formula or npm package must point at signed or checksummed release
archives and must pass a clean-machine smoke test before public promotion.

## Not Yet Implemented

- hosted installer;
- published Homebrew tap;
- published npm install package;
- artifact signing;
- SBOM/provenance automation;
- Windows release path.
