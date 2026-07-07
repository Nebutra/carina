# BYOK Provider Catalog Design

Date: 2026-07-07

## Context

Carina already has a small ordered auth chain: daemon environment BYOK keys win,
and Nebutra OAuth is a fallback. That is correct but too narrow. OpenCode's
stronger pattern is a provider catalog plus a user-level credential store:
providers and models are discoverable, credentials can be managed without
editing shell env, and diagnostics can report credential source without leaking
values.

## Design

Absorb the provider enumeration layer without copying OpenCode's AI SDK runtime.

1. Add a provider catalog package that can read a bundled seed and refresh from
   `https://models.dev/api.json`.
2. Cache refreshed catalog data under `~/.carina/cache/models.json` with 0600
   permissions and a short freshness window.
3. Keep auth values out of logs. Store user credentials in
   `~/.carina/auth.json` with 0600 permissions.
4. Add CLI management commands:
   - `carina auth login <provider> [api_key|-]`
   - `carina auth list`
   - `carina auth logout <provider>`
   - `carina providers list [--refresh]`
5. Preserve Carina's precedence:
   daemon env BYOK > user auth store > file/static admin config > Nebutra OAuth.
6. Do not make repo-local config a trusted place for plaintext provider keys.

## Non-goals

- Do not implement every provider protocol in this wave.
- Do not add project-scoped plaintext credential files.
- Do not weaken existing secret redaction or egress credential boundaries.

## Follow-up

Wire selected providers into the model router in small protocol batches:
Anthropic, OpenAI, OpenAI-compatible, OpenRouter, then cloud providers that need
provider-specific auth flows.
