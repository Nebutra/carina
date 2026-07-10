# Roadmap

Carina is currently an alpha with source builds and a public macOS Homebrew
channel. This roadmap records intended product direction, not committed dates.

## Install And Release Channels

Today:

- source build from Git with `make all`;
- local release gate with `make release-check`;
- current-platform release candidate archives with `make release-package`;
- local binaries under `bin/`;
- local archives, checksums, and manifests under `dist/`;
- checksummed Apple Silicon and Intel macOS releases;
- official `Nebutra/homebrew-tap` Formula with install/upgrade smoke tests;
- GitHub build provenance for release archives;
- no Apple code signing or notarization yet.

Planned install channels:

1. **Signed/notarized releases**: add Apple signing/notarization, Linux
   archives, SBOMs, and expanded provenance verification.
2. **Linuxbrew**: extend the Nebutra-maintained Formula after Linux archives
   pass clean-machine installation tests. Bundled Headroom remains gated on a
   reproducible standalone artifact for every supported architecture.
3. **npm ecosystem package**: an `@nebutra/carina` package for JavaScript and
   TypeScript users. It should be a thin installer/launcher for platform
   binaries, not a second Node.js runtime. TypeScript SDK packages should stay
   separate from the CLI install package.
4. **Later channels**: shell installer, Linux distro packages, Docker images
   for daemon/worker roles, and Windows packages after Windows support exists.

Release-channel acceptance criteria:

- `make release-check` passes on a clean release machine;
- artifacts are signed or have published checksums before public promotion;
- Homebrew install, npm global install, and source install each have a smoke
  test;
- bundled Headroom is pinned by `integrations/headroom.lock` and verified by
  checksum during release packaging;
- installed CLI help uses only `carina` naming;
- uninstall instructions remove binaries, service files, and local state
  locations clearly.

## Product Polish

Near-term polish work:

- keep README and quickstart centered on user workflows rather than internal
  language split;
- improve TUI/dashboard status views;
- document production deployment profiles for remote workers;
- improve SDK parity across TypeScript, Python, and Go;
- publish security and contributor processes before broad external adoption.

## Nebutra Cloud Identity And Sync

Carina should not grow a Codex-style app server inside the local runtime. The
multi-endpoint product layer belongs to Nebutra Cloud (`nebutra.com`).

Planned sequence:

1. Keep `nebutra_sync_mode=off` as the only source-first alpha behavior.
2. Add a Nebutra-authenticated endpoint/device registry.
3. Add metadata sync for endpoint/session indexes and status summaries.
4. Add explicit audit-bundle sync with redaction and retention controls.
5. Add remote handoff through existing daemon RPC, approval, and audit paths.

See [Nebutra Cloud boundary](nebutra-cloud-boundary.md).
