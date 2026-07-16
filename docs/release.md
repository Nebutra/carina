# Release Process

Carina is currently an alpha with automated macOS and Linux GitHub releases
and an official Homebrew tap. This document describes the local release gate and the
tag-driven public release path. Future channels are tracked in
[docs/roadmap.md](roadmap.md).

## Current Source-First Release Gate

Before tagging or publishing anything from this repository, run:

```bash
make release-check
```

This is the same technical gate used by the tag workflow. It is strict: a
failed gate or an unclean/out-of-sync release source returns non-zero. It
validates:

- pinned Go 1.25, Rust 1.96.1, Node 24, Python 3.12, and Zig 0.15.1 toolchains;
- locally installed `actionlint` 1.7.12 for the offline workflow gate (strict
  online mode may fetch that exact Go module when the binary is absent);
- product/package/workflow version agreement and workflow lint;
- Go app builds, vet, application tests, and runtime race tests;
- Rust workspace and release kernel builds/tests;
- Go, Python, and TypeScript SDK conformance;
- VS Code, web, npm launcher, native acceptance, and benchmark gates;
- current-platform archive manifest/checksums and packaged-daemon conformance;
- exact native archive, deb/rpm, Windows worker, VSIX/Web/installer, and
  five-package npm assembly contracts, including frozen bundle and offline
  global-install verification;
- Homebrew Formula rendering/install where the host supports it;
- signing/notarization automation dry-run behavior.

Carina compiles its six Zig tools directly with the pinned compiler instead of
using Zig's host build runner. On macOS the tools target macOS 13.0 or later;
this keeps local builds working on newer macOS releases even when Zig 0.15.1's
host runner cannot link against the newest SDK.

The local Homebrew install/upgrade gate is reported as `BLOCKED` when the
machine's Command Line Tools are older than Homebrew's current minimum. That is
a host prerequisite, not a product test failure; release CI still runs the
same temporary-tap test on both supported macOS architectures.

For a developer-readable report that records missing Apple/npm prerequisites
without stopping after `BLOCKED`, run:

```bash
make release-preflight
```

For final release readiness, including read-only GitHub/npm prerequisite
checks, run:

```bash
make release-ready
```

The normal developer preflight is offline. `make release-ready` opts into
read-only GitHub/npm queries; neither mode invokes a publication API. Accepted
notary JSON is a post-build/pre-publish evidence gate, so a source-tree
preflight reports it as `SKIP`, while the tag workflow requires it before
creating the GitHub Release.

Every mode writes `dist/release-preflight.json` plus per-gate logs. Statuses
mean:

- `PASS`: the gate executed and its evidence matched;
- `FAIL`: a technical check failed; every mode exits 1;
- `BLOCKED`: required release state or external configuration is absent;
  developer preflight continues, while strict mode exits 2;
- `SKIP`: the gate is inapplicable on this host or was explicitly excluded,
  never a substitute for a required credential.

These commands never push, tag, publish, sign with dummy credentials, or call
Apple/npm publication endpoints.

Manual equivalent:

```bash
make all
cargo build --release -p carina-kernel --bin carina-kernel-service
CARINA_KERNEL_BIN="$PWD/target/release/carina-kernel-service" go test -race ./go/...
CARINA_KERNEL_BIN="$PWD/target/release/carina-kernel-service" go test ./apps/...
cargo test --workspace
go test -race ./sdk/go
(cd sdk/typescript && npm ci && npm test)
(cd integrations/vscode && npm ci && npm test)
./scripts/test-platform-packaging.sh
```

## Local Release Package

Build a current-platform release candidate with:

```bash
make release-package
```

To build the product version declared by `go/product` explicitly:

```bash
VERSION=0.6.4 make release-package
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
`integrations/headroom.lock`. `package-release.sh` downloads the target's
SHA-256-pinned wheel or source distribution, installs the hash-locked build
dependencies from `integrations/headroom-requirements.lock`, builds a standalone
executable, and verifies both its CLI and managed MCP tools before packaging it.
The daemon never downloads Headroom at startup and nothing is installed into the
user's global Python, npm, pipx, or uv environment.

`VERSION_CHECK.txt` records CLI, daemon, Rust workspace, TypeScript SDK, and
Python SDK versions, plus the bundled Headroom pin and source artifact. Version
mismatches are warnings in the package manifest, not hidden state.

Use existing artifacts without rebuilding:

```bash
SKIP_BUILD=1 VERSION=0.6.4 ./scripts/package-release.sh
```

If Zig is unavailable but `zig/zig-out/bin/carina-*` artifacts already exist,
reuse them explicitly:

```bash
SKIP_ZIG=1 VERSION=0.6.4 make release-package
```

`SKIP_BUILD=1` and `SKIP_ZIG=1` are recorded as warnings in `MANIFEST.json` and
`VERSION_CHECK.txt`.

`SKIP_HEADROOM=1` remains an explicit developer-only escape hatch for offline
local package experiments and is recorded in the manifest. Tagged releases do
not use it. In `context_engine=auto`, Carina selects the bundled Headroom engine
after its health check and safely falls back to noop if startup later fails.

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

- `headroom` in release packages only, pinned by `integrations/headroom.lock`
- Zig tools from `zig/zig-out/bin`
- Rust `carina-kernel-service` under `target/release`

These are local build outputs, not public release artifacts. `make install`
copies the Go binaries, the release `carina-kernel-service`, and the Zig tools
flat into `$(PREFIX)/bin` (default `~/.local/bin`), mirroring the release
package layout minus the pinned Headroom bundle.

## Self-update contract

`carina update` is daemon-free and follows installation ownership. Homebrew,
npm, and pnpm installations delegate mutations to their package manager.
Standalone installations use the published GitHub Release platform archive
and its adjacent `.sha256`; before touching the install directory, the updater
also verifies `MANIFEST.json`, `checksums.txt`, product version, target OS/arch,
the complete runtime binary set, and the absence of developer-only `SKIP_*`
warnings. Tar path traversal, links, devices, duplicate entries, unexpected
origins, oversized payloads, and non-regular destinations fail closed.

Sibling runtime files are staged in the destination directory, fsynced, and
activated with per-file rename plus transaction-wide rollback; `carina` itself
is replaced last. The updater does not stop a daemon or interrupt an active
task. Operators restart the daemon after task completion. `--check` performs no
asset download, and a development build newer than the latest public release
is never downgraded unless an exact older version and `--force` are supplied.

## Versioning

Every Go runtime binary shares the single product version declared in
`go/product/version.go`; the CLI and daemon source their version constants
from it. Independent semver remains for:

- Rust workspace version: `Cargo.toml`
- SDK package versions under each SDK directory

`VERSION_CHECK.txt` records the full version matrix, and
`scripts/test-version-matrix.sh` validates it during `make release-check` and
tag releases.

## Automated Tag Release

Pushing a tag matching `v<major>.<minor>.<patch>` runs
`.github/workflows/release.yml`. The workflow:

- pins Zig `0.15.1`; local release checks may select the same isolated binary
  through `CARINA_ZIG_BIN` or install a checksum-verified copy with
  `scripts/install-zig-tool.sh`;
- builds native macOS and Linux archives for `arm64` and `amd64`, deb/rpm
  packages, and contained Windows worker ZIPs;
- packages the VS Code extension, static Web Operator, and checksum-enforcing
  installer as independently verified release assets;
- starts the daemon from each packaged archive and runs the Go, Python, and
  TypeScript read-only conformance contract against its Unix socket;
- freezes the four native npm packages plus launcher as one checksum-verified
  bundle on a draft GitHub Release before the first registry write. Full and
  partial reruns reuse those exact tarballs even when Apple timestamped signing
  would make a rebuilt archive byte-different;
- publishes the four native npm packages before the launcher using npm trusted
  publishing and provenance. Existing registry packages must match the frozen
  tarball integrity exactly;
- keeps self-asserted provenance out of npm tarballs: package checksums and
  deterministic SBOMs are generated with pinned Syft 1.46.0, while provenance is created only by
  `npm publish --provenance` through the registry's OIDC flow;

- requires the tag version to match the CLI version and the tag commit to equal
  `origin/main` exactly;
- verifies the `npm-release` environment, all five package bootstrap entries,
  and the Homebrew deploy key before public artifacts are promoted;
- builds on native Apple Silicon and Intel GitHub-hosted runners;
- installs each archive through a temporary Homebrew tap and runs `brew test`;
- publishes archives, per-archive checksums, and `SHA256SUMS`;
- creates GitHub build provenance attestations;
- serializes all tag versions through one Homebrew tap update group, refreshes
  `main`, refuses Formula downgrades, then pushes `Formula/carina.rb` through a
  repository-scoped SSH deploy key.

Once the draft becomes public, all 32 release assets are immutable. A full
workflow rerun downloads and verifies the existing native archives, signing
evidence, OS packages, operator clients, installer, checksum manifest, and
frozen npm bundle instead of overwriting them.
The Homebrew Formula always derives its checksums from that public Release, so
a retry cannot point the tap at newly rebuilt bytes.

The release is rejected before publication if either architecture fails to
build or install.

### Apple signing and notarization

Tag releases are fail-closed on Apple release credentials. Before either
architecture starts building, the workflow requires all of these protected
`codesigning` environment secrets:

- `APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64`: base64-encoded PKCS#12 export of
  the Developer ID Application certificate and private key;
- `APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD`: password used for that PKCS#12
  export;
- `APPLE_DEVELOPER_ID_APPLICATION_IDENTITY`: the complete `Developer ID
  Application: ...` identity shown by `security find-identity`;
- `APPLE_NOTARY_APPLE_ID`: Apple ID used for the notary service;
- `APPLE_NOTARY_TEAM_ID`: ten-character Apple Developer team ID;
- `APPLE_NOTARY_PASSWORD`: app-specific password for the notary Apple ID.

The macOS build matrix is bound to the protected `codesigning` GitHub
environment. That environment must exist and should require reviewers before
these secrets are exposed to either architecture job.

After configuring the five npm trusted-publisher bindings, set the non-secret
repository variable `NPM_TRUSTED_PUBLISHERS_CONFIRMED=true`. The tag workflow
checks this attestation before expensive build/signing work; the registry's
OIDC publication and provenance remain the authoritative runtime proof.

For example, encode the certificate locally without committing it:

```bash
base64 -i DeveloperIDApplication.p12 | pbcopy
```

`scripts/sign-and-notarize-release.sh` imports the certificate into a temporary
keychain, signs every Mach-O file in the existing architecture archive with a
secure timestamp and hardened runtime, refreshes the package manifest and
internal checksums, and submits a zip of the signed package with `notarytool`.
It only replaces the contents and checksum of the existing
`carina_<version>_darwin_<arch>.tar.gz`; release filenames and Homebrew URLs do
not change.

After Apple returns `Accepted`, the script runs strict `codesign` verification,
`codesign --check-notarization`, and `spctl --assess --type execute` for every
signed binary. It then publishes these audit companions beside each archive:

- `<archive>.notary.json`: the complete machine-readable `notarytool` result;
- `<archive>.signing.txt`: per-binary signature, notarization, and Gatekeeper
  assessment output.

Carina currently ships standalone command-line Mach-O files in a tar archive,
not an app bundle, pkg, or dmg. Apple does not support stapling a ticket to
these raw executables or to the tar file. Gatekeeper verification therefore
uses Apple's online notarization ticket, and the release workflow treats a
failed `codesign --check-notarization` or `spctl` assessment as fatal.

The automation and its missing-secret paths can be checked without Apple
credentials:

```bash
./scripts/test-sign-and-notarize-release.sh
```

A real notarization cannot be validated from a source checkout without the
certificate, notary credentials, and Apple service. Successful shell tests are
not evidence that Apple accepted a release; the published notary JSON and
signing report are that evidence.

## Homebrew Channel

Install the published Formula with:

```bash
brew install Nebutra/tap/carina
```

Upgrade with `brew update && brew upgrade carina`. The Formula source template
is `packaging/homebrew/carina.rb.template`; `scripts/render-homebrew-formula.sh`
injects versioned release URLs and both architecture checksums.

## External Activation Still Required

The repository ships the checksum-enforcing installer, Linuxbrew Formula,
deb/rpm packages, Windows worker ZIP, VSIX, Web Operator archive, container
Dockerfiles, SBOM generation, provenance, and verification contracts. Public
activation still requires the credentials, publisher identities, registries,
upstream review, hosting, and tag permissions listed in the Roadmap.
