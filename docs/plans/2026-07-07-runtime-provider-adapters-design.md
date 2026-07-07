# Runtime Provider Adapters

## Context

Carina already loads the models.dev provider catalog and stores local BYOK
credentials. That makes provider discovery broad, but runtime calls still only
use the Anthropic Messages API plus the mock fallback.

opencode gets broad runtime coverage by mapping catalog entries to provider
packages in the Vercel AI SDK. Carina's daemon is Go, so it cannot reuse that
loader directly. The useful part to absorb is the provider-family routing:
catalog metadata selects a protocol adapter, while env/user-auth precedence
continues to decide credentials.

## Scope

Ship protocol-family adapters instead of one handwritten adapter per provider:

- Anthropic native Messages API.
- OpenAI-compatible chat/completions API for OpenAI-compatible providers,
  OpenRouter, xAI, Groq, Together, Mistral, DeepInfra, Cerebras, Perplexity,
  local endpoints, and similar catalogs with OpenAI-style base URLs.
- OpenAI Responses API for OpenAI models where Responses is preferred.
- Google Gemini generateContent API for Gemini BYOK.

AWS Bedrock, Azure OpenAI, Google Vertex, and other cloud identity providers stay
out of this pass because they require region/resource/project configuration and
cloud credential chains, not just API-key transport.

## Data Flow

1. The daemon loads the provider catalog from `~/.carina/cache/models.json`, with
   the offline seed fallback.
2. For each catalog provider, Carina chooses the first supported adapter by
   provider id, npm package hint, API base URL, or explicit local env override.
3. Each adapter builds an auth chain:
   environment variables from the catalog first, then `~/.carina/auth.json`.
4. The model router registers only providers with a usable credential or local
   endpoint. Mock remains the final fallback.
5. Requests can target `provider/model` explicitly. `default` keeps existing
   fallback behavior.

## Error Handling

Provider failures stay local to the provider and the router continues fallback.
Adapters must not log secrets. HTTP errors include provider name and status, not
request headers or body. Unsupported providers are skipped during registration
instead of creating failing runtime entries.

## Tests

Unit tests cover adapter registration from catalog metadata, auth precedence,
OpenAI-compatible request/response parsing, Responses API parsing, Google Gemini
parsing, and explicit `provider/model` routing. CLI smoke continues to verify
catalog refresh and auth-store behavior.
