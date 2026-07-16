# Roadmap

Carina is an alpha Agent Runtime. The repository-owned items selected from the
July 2026 productization audits are implemented in the current source tree and
have repository-owned test or benchmark coverage. That statement is scoped to
the listed work; it is not a blanket claim that an alpha product has no future
product work. The remaining gates below require external services,
credentials, hardware, tenants, or repository-administrator access. This
document does not assign committed dates.

## Repository-Owned Closure Evidence

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

### TUI interaction closure

The July TUI UX audit is closed in the repository at the following evidence
boundary. Product shell waves E–H (context pressure, sticky shell, HITL modes,
quality hygiene) are included below; see `docs/plans/tui-product-ux-closure.md`.

| Gap | Repository-owned implementation | Evidence boundary |
| --- | --- | --- |
| Submission acknowledgement ownership | The submitted draft, paste payload, journal record, and idempotency key stay frozen while ordinary typing/paste starts an independent next draft; control keys remain responsive | Transactional and race-focused model tests cover success, failure, retry, overlay ownership, and type-ahead isolation |
| Terminal input and scrolling | The shipped view requests bracketed paste, cell-motion mouse reporting, declared cursor placement, resize-safe layout, and focused mouse-wheel routing | The PTY harness covers resize, bracketed paste, wheel input, `Ctrl+J`/`Shift+Enter`, CJK rendering, interrupt, and raw-mode restoration where tmux/PTYs are available |
| Interrupt and rewind | `Esc` interrupts an active turn; double `Esc` from an idle empty composer opens a checkpoint list, requires a preview, and requires `y` plus `Enter` before restore | Model/RPC tests cover interrupt ownership, preview-before-restore, explicit confirmation, and restored paused state |
| Keybinding DX | Semantic chat, composer, editor, approval, question, history, suggestion, and pager actions share one runtime keymap; validation uses the contexts that can actually be active together, keeps printable pager keys overlay-only, protects composer text, folds terminal-equivalent keys and common modifier aliases, and rejects ambiguous chord prefixes or duplicate JSON actions | `/keymap` atomically persists project overrides and applies them immediately; `Ctrl+V` quoted-insert records literal Escape/Enter chord steps without losing cancel/save; external config supports visible, cancellable, timeout-bounded chords; managed/global/project changes hot-reload with last-good fallback on invalid edits |
| Prompt-history privacy | Durable entries carry session/workspace scope; the TUI requests workspace history instead of recalling unrelated repositories by default | Store, daemon, RPC schema, and TUI merge/search tests cover scoped and legacy records |
| Background attention | The TUI requests terminal focus events, counts important background events, emits BEL plus OSC 9/777 at most once per blur interval, and clears unread attention on focus | Model tests cover focused silence, lost-focus notification latching, status visibility, and terminal-control injection resistance |
| Terminal buffer choice | Alternate screen remains the default; `carina-tui --no-alt-screen` and `tui_alternate_screen=never` render in the normal terminal buffer | View/config/launcher tests cover the selected mode. A strict commit-once static/dynamic renderer is not claimed; the normal-buffer mode is the accepted native-scrollback escape hatch |
| Six-locale UX and microcopy | Complete en, zh-CN/zh-Hans, ja, ko, es, and fr catalogs cover interface, Ambient, Governed, Degrade, and bootstrap copy; locale precedence, explicit-value validation, safe placeholder rendering, CLDR count selection, facts/terms metadata, and catalog parity are repository contracts | CI tests traverse all locales, placeholders, register lint, count categories, fallback behavior, startup help/errors, and catalog versions. Traditional Chinese is explicitly not claimed and falls back to English only for system detection |
| Render regression signal | A production-model `View()` benchmark exercises a workspace-sized transcript and active composer | The benchmark is repeatable repository evidence, not a published latency/SLO claim until release hardware measurements are recorded |
| Sticky shell mode | Empty-composer `!` enters sticky shell (`! ` prompt, governed `command.exec`); `Esc` on empty draft returns to chat; one-shot `!cmd` remains | TUI shell-mode unit tests and README interaction notes |
| Context pressure and compact | Notices near 80%/90%; auto-compact at ≥85% only when a paused checkpoint exposes `session.checkpoint.compact` | Model tests for pressure thresholds and compact availability gating |
| Side Q&A and session fork | `/btw` answer-only on the current session; `/btw --fork` and `/side` call `session.fork` then attach (no dual-pane) | Product-wave tests for fork busy refusal and honest copy |
| Product HITL modes | Daemon modes `ask` \| `always-approve` \| `dont-ask`; `/always-approve` warns; org `disable_always_approve`; footer shows mode | Daemon resolve-approval tests, RPC mode tests, config validation; session/kernel axis `untrusted\|on_request\|never` remains separate |
| Approval naming hygiene | Product config/RPC rejects session-axis tokens (`never`, `on_request`, `untrusted`) so they cannot silently alias always-approve/ask | `normalizeApprovalMode` and `config.Validate` tests |

True IME composition placement on macOS Pinyin and fcitx5/Wayland, terminal
selection behavior under mouse reporting, and representative provider streams
remain in the external terminal matrix below because they require real desktop
input methods, terminals, hardware, and provider credentials. Automated CJK
cell width, grapheme editing, cursor coordinates, and PTY input are repository
tests; they must not be presented as substitutes for those human runs.

## External Activation

| Work | Why it is external | Completion evidence |
| --- | --- | --- |
| Apple signing and notarization | Requires Developer ID certificate, Apple notary account, protected GitHub environment, secrets, and approvers | Both Darwin assets have Accepted notary JSON, verified Team ID, signature, and Gatekeeper reports |
| npm public packages | Requires `@nebutra` package ownership, `npm-release` environment, and five trusted-publisher bindings | OIDC-only `npm publish --provenance` succeeds and immutable package integrity matches the release bundle |
| Homebrew Core `brew install carina` | Requires a Homebrew Core formula submission and upstream review; `Nebutra/tap/carina` is already the maintained channel | Core formula is merged and clean-machine install/upgrade passes on macOS and Linux |
| VS Code Marketplace / Open VSX | Requires publisher identities, marketplace tokens/OIDC support, listings, and review | Published extension digest matches the release VSIX and installs on a clean profile |
| Container registry | Requires registry namespace, credentials/OIDC, retention, signing policy, and public visibility decision | Multi-arch daemon/worker manifests, provenance, SBOM, and signature verification are public |
| Hosted Web Operator and installer URL | Requires DNS, TLS, hosting/CDN, origin allowlists, and operating ownership | Hosted assets match release digests and WSS/origin/caching checks pass |
| Real provider and terminal matrix | Requires paid provider credentials plus representative terminal/OS hardware and desktop IMEs | Recorded canaries cover macOS Pinyin and fcitx5/Wayland candidate placement, CJK input, narrow layouts, reconnect, approval/question flows, mouse selection/scrolling, alternate/normal-buffer modes, and provider streaming |
| Native-language semantic review | Requires fluent human reviewers for zh-CN, ja, ko, es, and fr, with extra scrutiny on permission, policy, secret, egress, rollback, and failure copy | Review records confirm factual parity, native tone, terminology, and absence of misleading safety claims; this strengthens release evidence but does not block the repository-owned implementation closure |
| Nebutra Cloud connector activation | Requires a versioned API contract, staging tenant, OIDC/device identity, client credentials, retention/redaction policy, and service SLOs | Contract tests pass against staging and sync/handoff is opt-in, revocable, audited, and local-authority preserving |
| GitHub governance | Requires repository administrator access | Branch protection, required checks/reviews, private vulnerability reporting, and environment protection are enabled |
| Public release promotion | Requires release/tag write permission and the external gates above | A non-draft tag release passes the immutable full-asset verification path |

## Release Bootstrap Runbook

This is the one-time administrator runbook required before the first formal
release. It resolves the Apple and npm external blockers; it does not weaken or
bypass the release workflow. Do not create or push the `v0.6.2` tag until every
item in the final checklist passes. Pushing that tag starts the public release
workflow immediately.

### Rules before starting

1. Never commit a certificate, private key, password, npm token, or a file that
   contains one. Do not paste secret values into issues, pull requests, chat,
   workflow logs, or shell commands that will be retained in history.
2. Use the exact case and spelling shown below. GitHub environment names, npm
   package names, repository owner/name, workflow filename, and Apple identity
   strings are exact-match values.
3. Do not manually publish `0.6.2` to npm. npm versions are immutable. The
   formal OIDC workflow owns `0.6.2` and cannot replace a manually published
   copy.
4. The one-time npm bootstrap uses `0.0.0-bootstrap.0` and the `bootstrap`
   dist-tag. It creates the five package settings pages without occupying
   `0.6.2` or assigning the package to the `latest` channel.
5. Delete local secret staging files after GitHub has stored the secrets. If a
   value is exposed, revoke or rotate it before continuing.

### 1. Obtain the Apple signing material

Required access: the Apple Developer Program Account Holder must create the
Developer ID certificate. The Apple ID used for notarization must have access
to the same developer team and must have two-factor authentication enabled.

#### 1.1 Create and install a Developer ID Application certificate

On the Mac that will hold the private key:

1. Open **Keychain Access**.
2. Choose **Keychain Access > Certificate Assistant > Request a Certificate
   From a Certificate Authority**.
3. Enter the Apple Developer account email, choose **Saved to disk**, and save
   the certificate signing request (`.certSigningRequest`). The private key is
   created in this Mac's login keychain. Do not lose it.
4. Follow Apple's
   [Developer ID certificate procedure](https://developer.apple.com/help/account/certificates/create-developer-id-certificates),
   then sign in to
   [Certificates, Identifiers & Profiles](https://developer.apple.com/account/resources/certificates/list).
5. Open **Certificates**, click **+**, select **Developer ID Application**, and
   continue. Do not select Apple Development, Apple Distribution, Developer ID
   Installer, or Mac App Distribution.
6. Upload the CSR, generate the certificate, and download the `.cer` file.
7. Double-click the `.cer` file to install it in the login keychain.
8. In Keychain Access, open **login > My Certificates**. The certificate must
   expand to show a private key below it. A certificate without that private
   key cannot sign a release.

Verify it in Terminal:

```bash
security find-identity -v -p codesigning
```

Success means the output includes exactly one usable line such as:

```text
1) ABCDEF... "Developer ID Application: Nebutra (...) (TEAMID1234)"
```

Copy only the complete text inside the quotes, starting with `Developer ID
Application:`. That complete string becomes
`APPLE_DEVELOPER_ID_APPLICATION_IDENTITY`. The final parenthesized value is the
ten-character Team ID; verify it on the Apple Developer membership page before
using it as `APPLE_NOTARY_TEAM_ID`.

If Terminal reports `0 valid identities found`, stop. The usual causes are a
certificate installed without its private key, the wrong certificate type, an
expired/revoked certificate, or installation into a different keychain.

#### 1.2 Export the certificate and private key as PKCS#12

1. In **Keychain Access > login > My Certificates**, select the Developer ID
   Application certificate that expands to show its private key.
2. Choose **File > Export Items** and export as
   `DeveloperIDApplication.p12`.
3. Set a new strong export password. This is
   `APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD`; it is not the Mac login
   password and not the Apple ID password.
4. Put the file in a temporary private directory, not in this repository.

Encode and validate it locally:

```bash
umask 077
mkdir -p "$HOME/.carina-release-secrets"
mv /path/to/DeveloperIDApplication.p12 "$HOME/.carina-release-secrets/"
base64 -i "$HOME/.carina-release-secrets/DeveloperIDApplication.p12" \
  | tr -d '\n' \
  > "$HOME/.carina-release-secrets/DeveloperIDApplication.p12.b64"
base64 -D \
  -i "$HOME/.carina-release-secrets/DeveloperIDApplication.p12.b64" \
  -o /tmp/carina-developer-id-check.p12
cmp "$HOME/.carina-release-secrets/DeveloperIDApplication.p12" \
  /tmp/carina-developer-id-check.p12
rm /tmp/carina-developer-id-check.p12
```

`cmp` must print nothing and exit successfully. The contents of the `.b64`
file become `APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64`.

#### 1.3 Create the notarization password and test it locally

`APPLE_NOTARY_APPLE_ID` is the Apple ID email used for the developer account.
`APPLE_NOTARY_PASSWORD` must be an app-specific password, not the normal Apple
ID password and not an App Store Connect API key.

1. Sign in to [Apple Account](https://account.apple.com/).
2. Open **Sign-In and Security > App-Specific Passwords**.
3. Select **Generate an app-specific password**, name it `Carina notarization`,
   and record the generated value in a password manager.
4. Test the three notarization values on the Mac:

```bash
xcrun notarytool store-credentials carina-release-local \
  --apple-id "APPLE_ID_EMAIL" \
  --team-id "TEN_CHARACTER_TEAM_ID" \
  --password "APP_SPECIFIC_PASSWORD"
```

This command validates the credentials with Apple. Success creates a local
Keychain profile only; the GitHub workflow still uses the three secrets below.
When the local profile is no longer needed, open Keychain Access, search for
`carina-release-local`, verify that the result is the notarytool credential,
and delete that item. Do not delete the Developer ID private key.

If validation fails, check all three values as one tuple: the Apple ID must
belong to the team identified by the Team ID, two-factor authentication must be
enabled, and the password must be an active app-specific password.

### 2. Create the protected GitHub `codesigning` environment

Required access: repository administrator for `Nebutra/carina`.

1. Read GitHub's
   [environment configuration procedure](https://docs.github.com/en/actions/how-tos/deploy/configure-and-manage-deployments/manage-environments),
   then open `https://github.com/Nebutra/carina/settings/environments`.
2. Select **New environment**, enter exactly `codesigning`, and create it.
3. Under **Deployment protection rules**, enable **Required reviewers** and
   add at least one release approver. The repository preflight deliberately
   rejects an unprotected environment. For a one-person release team, do not
   enable **Prevent self-review**, because that would make the release
   impossible to approve.
4. Under **Environment secrets**, add the following six names. Preserve leading
   and trailing characters in every value; do not add quotes.

| GitHub environment secret | Exact value |
| --- | --- |
| `APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64` | Entire one-line contents of `DeveloperIDApplication.p12.b64` |
| `APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD` | Password chosen when exporting the `.p12` |
| `APPLE_DEVELOPER_ID_APPLICATION_IDENTITY` | Complete quoted identity from `security find-identity`, without the quote characters |
| `APPLE_NOTARY_APPLE_ID` | Apple ID email used for notarization |
| `APPLE_NOTARY_TEAM_ID` | Exact ten-character Apple Developer Team ID |
| `APPLE_NOTARY_PASSWORD` | Apple app-specific password |

The web UI is the simplest option. To upload from Terminal without placing a
secret in command history, use standard input:

```bash
gh secret set --repo Nebutra/carina --env codesigning \
  APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64 \
  < "$HOME/.carina-release-secrets/DeveloperIDApplication.p12.b64"

read -r -s 'P12_PASSWORD?P12 export password: '
printf '%s' "$P12_PASSWORD" | gh secret set --repo Nebutra/carina \
  --env codesigning APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD
unset P12_PASSWORD
echo
```

Repeat the `read -r -s` plus `printf` pattern for sensitive text values. The
identity and Team ID may be piped with `printf '%s' 'exact value'`; do not add a
newline or surrounding quotes.

Verify the environment, protection rule, and secret names. GitHub never returns
secret values after storage:

```bash
gh api repos/Nebutra/carina/environments/codesigning \
  --jq '{name, protection_rules}'
gh secret list --repo Nebutra/carina --env codesigning
```

The second command must list all six names. Then remove the local exported
certificate files after ensuring the original certificate and private key are
safely backed up according to the team's credential policy:

```bash
rm -rf "$HOME/.carina-release-secrets"
```

### 3. Create the five public npm packages once

Required access: an npm account with two-factor authentication and permission
to publish public packages under the `@nebutra` organization. If the npm
organization does not exist, create `nebutra` at
[npm organization creation](https://www.npmjs.com/org/create) and add the
release administrator as an owner or package publisher.

Install a supported toolchain. npm trusted publishing requires npm 11.5.1 or
newer and Node.js 22.14.0 or newer. Then log in interactively:

```bash
node --version
npm --version
npm login --auth-type=web
npm whoami
npm org ls nebutra
```

`npm whoami` must print the intended account. `npm org ls nebutra` must show
that account with sufficient rights. An `ENEEDAUTH` error means login is still
missing; an `E403` during publish usually means the account lacks `@nebutra`
publish permission or required two-factor authentication.

Create only minimal namespace-reservation packages in a disposable directory.
Do not use the repository's release package files for this step:

```bash
bootstrap_dir="$(mktemp -d)"
for name in \
  carina \
  carina-darwin-arm64 \
  carina-darwin-x64 \
  carina-linux-arm64 \
  carina-linux-x64; do
  package_dir="$bootstrap_dir/$name"
  mkdir -p "$package_dir"
  printf '# @nebutra/%s\n\nNamespace bootstrap; use a stable release.\n' \
    "$name" > "$package_dir/README.md"
  printf '{\n  "name": "@nebutra/%s",\n  "version": "0.0.0-bootstrap.0",\n  "description": "Carina package namespace bootstrap",\n  "license": "MIT",\n  "files": ["README.md"]\n}\n' \
    "$name" > "$package_dir/package.json"
  npm publish "$package_dir" --access public --tag bootstrap
done
rm -rf "$bootstrap_dir"
```

Every `npm publish` must succeed before continuing. The explicit
`--tag bootstrap` is mandatory. Never change it to `latest`, and never change
the bootstrap version to `0.6.2`.

Verify all five packages and their dist-tags:

```bash
for package in \
  @nebutra/carina \
  @nebutra/carina-darwin-arm64 \
  @nebutra/carina-darwin-x64 \
  @nebutra/carina-linux-arm64 \
  @nebutra/carina-linux-x64; do
  npm view "$package@0.0.0-bootstrap.0" name version
  npm dist-tag ls "$package"
done
```

Each package must report version `0.0.0-bootstrap.0` and a
`bootstrap: 0.0.0-bootstrap.0` tag. It must not report
`latest: 0.0.0-bootstrap.0`. If `latest` was accidentally created, remove only
that tag; do not unpublish the package:

```bash
npm dist-tag rm @nebutra/PACKAGE_NAME latest
```

### 4. Bind all five packages to GitHub OIDC trusted publishing

The repository already has the required publisher-side implementation:
`.github/workflows/release.yml` exists, the `publish-npm` job uses environment
`npm-release`, and that job grants `id-token: write`. Do not add an npm access
token as a fallback.

First create the matching GitHub environment:

1. Open `https://github.com/Nebutra/carina/settings/environments`.
2. Create an environment named exactly `npm-release`.
3. Add a required reviewer if releases require manual approval. For a
   one-person release team, leave **Prevent self-review** disabled.
4. Do not add `NPM_TOKEN` or any long-lived npm credential. npm authenticates
   the workflow using GitHub's short-lived OIDC identity.

Read npm's
[trusted publishing requirements](https://docs.npmjs.com/trusted-publishers/),
then configure each of these five package pages separately:

- `https://www.npmjs.com/package/@nebutra/carina/access`;
- `https://www.npmjs.com/package/@nebutra/carina-darwin-arm64/access`;
- `https://www.npmjs.com/package/@nebutra/carina-darwin-x64/access`;
- `https://www.npmjs.com/package/@nebutra/carina-linux-arm64/access`;
- `https://www.npmjs.com/package/@nebutra/carina-linux-x64/access`.

On every page, find **Trusted Publisher**, select **GitHub Actions**, and enter
the same exact values:

| npm field | Value |
| --- | --- |
| Organization or user | `Nebutra` |
| Repository | `carina` |
| Workflow filename | `release.yml` |
| Environment name | `npm-release` |

Use the filename only, not `.github/workflows/release.yml`. Case matters. npm
does not prove that a typed configuration is correct when it is saved, so
visually read back every field on every package. Each package supports one
trusted publisher; replace an obsolete binding instead of creating competing
ones.

Only after all five pages show the exact binding, set the non-secret repository
confirmation variable:

```bash
gh variable set NPM_TRUSTED_PUBLISHERS_CONFIRMED \
  --repo Nebutra/carina \
  --body true
gh variable get NPM_TRUSTED_PUBLISHERS_CONFIRMED \
  --repo Nebutra/carina
```

This variable is an administrator assertion, not authentication. Setting it
early only moves a configuration error into the public release job.

### 5. Run the final preflight and publish

Run these commands from a clean `main` checkout. A temporary preflight output
directory avoids treating stale local signing files as current release
evidence:

```bash
git fetch origin main --tags
git status --short
test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"

preflight_dir="$(mktemp -d)"
CARINA_PREFLIGHT_DIST="$preflight_dir" \
  ./scripts/release-preflight.sh --check-only --online --strict
rm -rf "$preflight_dir"
```

`git status --short` must print nothing. The preflight must report no `FAIL` or
`BLOCKED` gates. In particular, `apple_credentials`, `npm_bootstrap`, and
`npm_trusted_publisher` must be `PASS`. The preflight can verify secret names,
environment protection, public package visibility, and the confirmation
variable; only the release workflow can prove the secret values and OIDC
binding work end to end.

Create and push the formal tag only after that result:

```bash
version="0.6.2"
git tag -a "v$version" -m "Carina $version"
git push origin "v$version"
gh run list --repo Nebutra/carina --workflow release.yml --limit 3
```

Open the new Release workflow run. Approve the `codesigning` deployment when
requested; approve `npm-release` when the npm job reaches it. Both native npm
packages and macOS architectures must finish before the launcher and public
GitHub Release are promoted. The workflow then updates
`Nebutra/homebrew-tap` through the already configured
`HOMEBREW_TAP_DEPLOY_KEY`.

Verify the public result:

```bash
gh release view v0.6.2 --repo Nebutra/carina \
  --json isDraft,tagName,publishedAt,url

for package in \
  @nebutra/carina \
  @nebutra/carina-darwin-arm64 \
  @nebutra/carina-darwin-x64 \
  @nebutra/carina-linux-arm64 \
  @nebutra/carina-linux-x64; do
  npm view "$package@0.6.2" name version dist.integrity
done

brew update
brew info Nebutra/tap/carina
brew install Nebutra/tap/carina
carina --version
```

Success means the GitHub Release is not a draft, all five npm packages expose
`0.6.2`, both Darwin assets contain Accepted notary evidence and signing
reports, the tap Formula reports `0.6.2`, and a clean Homebrew installation
runs `carina --version` successfully.

### Failure lookup

| Symptom | Meaning and action |
| --- | --- |
| GitHub environment API returns `404` | The exact environment does not exist, or the current GitHub account cannot administer it. Create it in repository settings and retry. |
| Preflight says `codesigning environment lacks protection rules` | Add at least one deployment protection rule, normally a required reviewer, to `codesigning`. |
| `security find-identity` reports zero identities | The Developer ID Application certificate/private-key pair is absent, expired, revoked, or in another keychain. Reinstall the matching pair; do not upload an unusable `.p12`. |
| `security import` or PKCS#12 MAC verification fails | The `.p12` password is wrong or the base64 value is damaged. Decode it locally, compare it with the original, and upload both values again. |
| `notarytool` reports invalid credentials | Recheck the Apple ID, Team ID, and app-specific password together. Do not substitute the normal Apple ID password. |
| GitHub job says `Waiting` at `codesigning` or `npm-release` | An allowed reviewer must approve that environment deployment in the Actions UI. |
| `npm whoami` returns `ENEEDAUTH` | Run `npm login --auth-type=web` with the intended npm account. |
| First npm publish returns `E404` or `E403` | Confirm the `nebutra` npm organization exists, the account is a member with publish rights, the package is public, and npm two-factor requirements are satisfied. |
| OIDC npm publish returns authentication/provenance errors | Read back the affected package's trusted publisher. It must be `Nebutra` / `carina` / `release.yml` / `npm-release`, and the workflow job must retain `id-token: write`. |
| npm reports that `0.6.2` already exists | Stop. npm versions cannot be overwritten. Compare its integrity with the frozen release bundle; do not publish a different artifact under another tag to hide the mismatch. |
| Release validation rejects the tag | The tag version differs from the product version, or the tag commit is not exactly `origin/main`. Delete an unpushed local tag and correct the checkout; do not move a public tag silently. |
| Homebrew remains on the previous version | Inspect `update-homebrew-tap` after the public Release job. The tap update occurs only after signing, npm publication, and release promotion succeed. |

### Definition of done

- [ ] A valid Developer ID Application identity with its private key is
  available and backed up under the team's credential policy.
- [ ] `notarytool store-credentials` validates the Apple ID, Team ID, and
  app-specific password.
- [ ] `codesigning` exists, has a protection rule, and contains all six exact
  environment secret names.
- [ ] All five `@nebutra/carina*` packages exist publicly at
  `0.0.0-bootstrap.0` under the `bootstrap` dist-tag; bootstrap is not `latest`.
- [ ] `npm-release` exists and all five packages bind to `Nebutra/carina`,
  `release.yml`, and `npm-release` as trusted publisher.
- [ ] Repository variable `NPM_TRUSTED_PUBLISHERS_CONFIRMED` is exactly `true`.
- [ ] Online strict release preflight has no `FAIL` or `BLOCKED` result.
- [ ] The formal tag points exactly to `origin/main` and is pushed only once.
- [ ] Apple returns `Accepted` for both Darwin release assets.
- [ ] GitHub Release, five npm packages, and the maintained Homebrew tap all
  expose the same version and verified artifact integrity.

The Homebrew tap update requires `HOMEBREW_TAP_DEPLOY_KEY`, which is already
configured. Homebrew Core acceptance remains a separate upstream process and
is not implied by tap readiness.

## Deliberate Non-Goals

- A Windows desktop daemon/CLI is not claimed; the supported Windows artifact
  is the contained remote worker.
- Carina is not a VM or a replacement for an executor's workspace/container
  isolation.
- Cloud sync is not enabled until the Nebutra service contract and tenant
  controls exist.
- A strict commit-once static/dynamic terminal renderer is not a hidden TUI
  TODO. The supported product choices are the full-screen viewport, the
  normal-buffer mode, and the plain transcript pager; revisit that renderer
  only with measured evidence that these modes are insufficient.
- GPU rendering, terminal scrollback/reflow, tabs, panes, copy-on-select,
  quick-select, and clickable terminal paths are host-terminal features rather
  than hidden Carina TUI TODOs. See the Kaku absorption review in
  `docs/research/kaku-terminal-absorption.md`.
- Release scripts and local tests are not evidence that an external registry,
  marketplace, Apple service, or hosted deployment accepted an artifact.

See [release operations](release.md), [remote workers](deployment/remote-workers.md),
and the [Nebutra Cloud boundary](nebutra-cloud-boundary.md).
