# Roadmap

Carina is currently an alpha with source builds and a public macOS Homebrew
channel. This roadmap records intended product direction, not committed dates.

## Install And Release Channels

Today:

- source build from Git with `make all`;
- CI-equivalent technical release gate with `make release-check`;
- structured local readiness report with `make release-preflight`, and strict
  online external readiness enforcement with `make release-ready`;
- current-platform release candidate archives with `make release-package`;
- local binaries under `bin/`;
- local archives, checksums, and manifests under `dist/`;
- checksummed Apple Silicon and Intel macOS releases;
- official `Nebutra/homebrew-tap` Formula with install/upgrade smoke tests;
- verified, writable `Carina release workflow` deploy key for
  `Nebutra/homebrew-tap`;
- Linux `amd64` and `arm64` archive jobs with checksums, provenance, and
  packaged-daemon SDK conformance;
- npm launcher and native platform package assembly with OIDC trusted
  publishing and provenance support;
- operator session review, channel crash reconciliation, artifact inspection,
  and usage/cost commands included in the release CLI smoke surface;
- GitHub build provenance for release archives;
- fail-closed Developer ID signing and Apple notarization automation for future
  tag releases, with per-release notary JSON and Gatekeeper reports; the first
  credentialed Apple-accepted run remains externally verifiable release work.

Planned install channels:

1. **Credentialed Apple release**: provision the Developer ID and notarization
   credentials below, then validate the fail-closed signing/notarization path
   on a public tag for both macOS architectures.
2. **First npm trusted release**: establish the `@nebutra` packages and npm
   trusted-publisher bindings, then verify the OIDC-only publication path on a
   public tag. The launcher remains a thin platform-binary installer rather
   than a second Node.js runtime.
3. **Linuxbrew**: extend the Nebutra-maintained Formula after Linux archives
   pass clean-machine installation tests. Bundled Headroom remains gated on a
   reproducible standalone artifact for every supported architecture.
4. **Later channels**: shell installer, Linux distro packages, Docker images
   for daemon/worker roles, and Windows packages after Windows support exists.

### Release Credential Readiness

Audited 2026-07-12. Secret values must never be committed to this repository.

| Channel | Readiness | Remaining external work |
| --- | --- | --- |
| Homebrew tap | Ready | The repository secret `HOMEBREW_TAP_DEPLOY_KEY` exists, and the writable deploy key is verified on `Nebutra/homebrew-tap`. |
| Apple signing and notarization | Blocked | Create the protected GitHub `codesigning` environment, add all six required secrets, and approve the first two-architecture signing deployment. |
| npm trusted publishing | Blocked | The launcher and native packages are not yet present in npm, the local machine is not authenticated to npm, and the GitHub `npm-release` environment has not been created. |

Apple release provisioning requires these GitHub repository secrets:

- `APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64`;
- `APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD`;
- `APPLE_DEVELOPER_ID_APPLICATION_IDENTITY`;
- `APPLE_NOTARY_APPLE_ID`;
- `APPLE_NOTARY_TEAM_ID`;
- `APPLE_NOTARY_PASSWORD`.

Store or expose these secrets only through the protected `codesigning`
environment used by the native macOS build matrix. Configure required
reviewers before the first tag so signing credentials are not available to an
unapproved job.

The certificate must be a Developer ID Application certificate with its
private key exported as PKCS#12. `APPLE_NOTARY_PASSWORD` must be an app-specific
password for the notarization Apple ID. The first credentialed run must verify
the signature, Team ID, notarization result, and Gatekeeper acceptance rather
than treating secret presence as completion.

The npm bootstrap sequence is:

1. Authenticate an npm account with publish permission for the `@nebutra`
   scope and establish `@nebutra/carina` plus its four native platform packages.
2. Create the `npm-release` environment in `Nebutra/carina`.
3. Bind every package's npm trusted publisher to organization `Nebutra`,
   repository `carina`, workflow `release.yml`, and environment `npm-release`.
4. Set repository variable `NPM_TRUSTED_PUBLISHERS_CONFIRMED=true` only after
   verifying all five bindings.
5. Perform the first tag publication with GitHub OIDC and
   `npm publish --provenance`; do not add a long-lived `NPM_TOKEN` fallback.

Release-channel acceptance criteria:

- `make release-check` passes on a clean release machine;
- `make release-ready` reports zero `FAIL` and zero `BLOCKED` gates; its JSON
  report distinguishes missing external credentials from skipped
  platform-specific checks;
- artifacts are signed or have published checksums before public promotion;
- Homebrew install, npm global install, and source install each have a smoke
  test;
- bundled Headroom is pinned by `integrations/headroom.lock` and verified by
  checksum during release packaging;
- installed CLI help uses only `carina` naming;
- uninstall instructions remove binaries, service files, and local state
  locations clearly.

The technical pipeline is implemented, but implementation is not publication.
Until the credentialed tag workflow produces Accepted Apple notary JSON for
both Darwin archives and completes npm OIDC publication, those channels remain
blocked in the table above.

## Product Polish

Near-term polish work:

- keep README and quickstart centered on user workflows rather than internal
  language split;
- validate the typed TUI, structured question, reconnect, and narrow/CJK paths
  against real provider sessions on supported terminal profiles;
- harden and distribute the existing web operator dashboard and VS Code
  extension (packaging, marketplace publication, docs); the runtime promise
  remains terminal-first;
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
