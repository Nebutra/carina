# TUI Internationalization Architecture

Status: implementation contract, 2026-07-14

Carina's product locales are `en`, `zh`, `zh-Hant`, `ja`, `ko`, `es`, and `fr`.
The runtime key `zh` represents Simplified Chinese (`zh-Hans` / `zh-CN`).
The runtime key `zh-Hant` represents Traditional Chinese (`zh-Hant` / `zh-TW` /
`zh-HK` / `zh-MO`), **derived** from the Simplified catalogs via
OpenCC-compatible conversion (`scripts/gen_zh_hant.py`). Simplified remains
the authored source of truth.
Locale support is not a translation pass over rendered terminal strings. It is
a typed copy boundary between language-neutral runtime state and the TUI.

## Copy Layers

| Layer | Purpose | Tone rule |
| --- | --- | --- |
| Protocol codes | RPC methods, event types, statuses, ids, paths, commands, policy names | Never translated or used as prose |
| Interface | Labels, navigation, help, empty states, progress ownership, key hints | Neutral, concise, predictable |
| Governed | Approval, policy, audit, destructive action, rollback, secrets, egress | Exact full sentences; no humor, metaphor, emoji, or exclamation |
| Degrade | Failure, interruption, timeout, partial completion, unavailable dependencies | State what happened, what was preserved, and the next remedy |
| Ambient | Low-risk waiting, successful housekeeping, idle state | Dry and brief; locale-native restraint rather than translated jokes |

Brand character comes from the register switch, not from making every control
playful. When risk rises, Carina becomes quieter and more precise.

## Locale Resolution

The client resolves locale once at startup using this order:

1. explicit `--locale`;
2. `CARINA_LOCALE`;
3. layered `tui_locale` configuration;
4. `LC_ALL`, `LC_MESSAGES`, then `LANG`;
5. `en`.

BCP 47-style language tags and POSIX locale forms normalize to the supported
runtime keys. Regional tags such as `es-MX`, `fr-CA`, `ja-JP`, and
`ko-KR` use their supported base catalog. Bare `zh`, `zh-CN`, `zh-SG`, and
tags carrying `zh-Hans` use the `zh` catalog. Traditional Chinese tags
(`zh-Hant`, `zh-TW`, `zh-HK`, and `zh-MO`) use the `zh-Hant` catalog.
Unsupported system locales fall back to English; unsupported explicit
`--locale`, `CARINA_LOCALE`, or `tui_locale` values fail fast. Missing entries
must never render an empty string or a message id.

## Catalog Contract

- Call sites use stable message ids and named placeholders. They do not build
  sentences by concatenating translated fragments.
- Every id has complete coverage in all supported locales (including
  derived `zh-Hant`). CI rejects missing locales, missing placeholders,
  undeclared placeholders, terminal controls, and register-specific brand
  violations.
- Count-sensitive messages use locale-aware variants. The implementation
  follows CLDR categories rather than assuming every language has an English
  singular/plural split.
- Locale authors may change sentence order around named placeholders. Paths,
  hashes, ids, commands, and policy names remain verbatim.
- English is the runtime fallback, not a license for partial checked-in
  catalogs. Fallback exists for corrupt or forward-versioned state; repository
  catalogs are held to 100 percent coverage.

## Terminal Contract

- Layout uses terminal cell width, never byte or rune count. CJK, combining
  marks, and grapheme clusters share the same clipping boundary as English.
- Modal controls survive narrow terminals: title and final action row take
  priority over descriptive copy.
- Tests render every supported locale at wide, narrow, and one-row sizes and
  assert that no line exceeds the terminal width or total terminal height.
- Catalog data is stripped of ANSI and control characters before rendering;
  translated content cannot emit terminal control sequences.

## Authoring Governance

Translations are rewrites in the same register, not literal mappings of an
English joke. Governed and Degrade changes require the same review standard as
policy-facing code. Ambient copy may be neutral when a native, trustworthy line
is not available; forced humor is a quality failure.

The terminology glossary keeps `Agent Runtime`, agent, policy, audit,
checkpoint, rollback, workspace, session, and task consistent within each
locale. Public copy never revives the internal `Agent OS` positioning.

## Non-Goals

- Translating protocol fields, CLI commands, file paths, ids, or source output.
- Inferring locale from model output or workspace contents.
- Shipping right-to-left locales in this milestone. The no-concatenation and
  named-placeholder rules preserve an extension path, but bidi interaction
  needs its own terminal matrix before it can be claimed.
