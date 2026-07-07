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

## Install Channel Roadmap

Planned channels are tracked in [docs/roadmap.md](roadmap.md):

- signed GitHub release archives first;
- Homebrew tap for macOS and Linuxbrew;
- npm ecosystem package as a thin platform-binary installer/launcher;
- later shell installer, Linux distro packages, Docker images, and Windows
  packages when platform support exists.

## Not Yet Implemented

- hosted installer;
- Homebrew tap;
- npm install package;
- artifact signing;
- SBOM/provenance automation;
- Windows release path.
