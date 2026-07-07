# OpenCode Runtime Quality Upgrade

## Decision

Absorb the OpenCode ideas that improve provider runtime quality without changing
Carina's user-facing product model in this pass:

- Provider quirks registry for known transport/header/body differences.
- Richer models.dev metadata for model variants, costs, and per-model provider
  overrides.
- Better default-model selection from catalog metadata.
- Provider-aware retry hints from HTTP status and retry headers.

Defer larger product surfaces:

- ACP compatibility: valuable for IDE/client interop, but it adds a second
  public protocol surface.
- Agent/mode registry: valuable, but it changes prompt, permission, and
  subagent semantics.
- Message-level timeline revert: valuable for UI workflows, but it needs
  snapshot/revert semantics beyond Carina's current patch rollback.

## Tradeoffs

Provider quality is the best near-term target because Carina just added
models.dev catalog loading and runtime adapters. OpenCode's provider plugin
layer shows that broad provider support depends on small, explicit quirks:
OpenRouter-style referer headers, provider base URL normalization, Azure or
Cloudflare resource expansion, model mode overrides, and retry interpretation.

The downside is another layer of provider policy. To keep it auditable, the
quirks stay data-shaped and local: no dynamic code loading, no secret logging,
and no implicit cloud credential chain. Providers that need cloud identities
remain skipped until implemented deliberately.

## Implementation

1. Extend catalog parsing to keep model cost tiers, `experimental.modes`, docs,
   and per-model provider API overrides.
2. Add a provider quirk registry used during runtime provider construction.
3. Let `provider/model-mode` entries materialize from models.dev experimental
   modes when present.
4. Replace lexicographic fallback model selection with scored selection using
   status, modalities, text suitability, reasoning/tool support, release date,
   limits, and cost.
5. Wrap HTTP status failures in a retryable error that exposes `Retry-After` and
   `Retry-After-Ms`; teach `thinkWithRetry` to honor it within a sane cap.

## Tests

Unit tests should cover quirk headers/body, env-expanded URLs, experimental mode
materialization, model scoring, and retry-delay parsing. Existing provider
adapter tests should continue proving that credentials are applied without
leaking values.
