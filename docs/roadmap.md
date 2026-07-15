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
boundary:

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

## Release Credentials

Secret values must never be committed. Apple automation expects these secrets
in a protected `codesigning` GitHub environment:

- `APPLE_DEVELOPER_ID_APPLICATION_P12_BASE64`;
- `APPLE_DEVELOPER_ID_APPLICATION_P12_PASSWORD`;
- `APPLE_DEVELOPER_ID_APPLICATION_IDENTITY`;
- `APPLE_NOTARY_APPLE_ID`;
- `APPLE_NOTARY_TEAM_ID`;
- `APPLE_NOTARY_PASSWORD`.

The npm bootstrap requires public ownership of `@nebutra/carina` and its four
native packages, a protected `npm-release` environment, and trusted-publisher
bindings to `Nebutra/carina` / `release.yml`. Set
`NPM_TRUSTED_PUBLISHERS_CONFIRMED=true` only after all five bindings are
verified. Do not add long-lived token fallbacks.

The Homebrew tap update requires `HOMEBREW_TAP_DEPLOY_KEY`. Homebrew Core
acceptance is a separate upstream process and is not implied by tap readiness.

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
