# Codex Phase A Absorption: Instructions and Provider Cache

Source reviewed: `openai/codex` at `cca16a1`, focused on the deferred areas
from the prior Codex source review.

## Scope

This phase absorbs two Codex mechanisms by philosophy, not by copying their
product shape:

1. Project instructions should be a layered workspace protocol, not a single
   branded file.
2. Provider/model discovery should have explicit refresh policy and cache
   metadata, not only a best-effort JSON file.

Brand boundary: public naming remains Nebutra / Carina. Compatibility with
`AGENTS.md` is treated as project-instruction interoperability, not a brand
change.

## Project Instruction Chain

Codex loads project instructions from the repository root down to the current
working directory, prefers local override files, tracks provenance, and enforces
a byte budget. Carina currently reads only a few fixed `CARINA.md` locations.

Carina should load instructions in this order:

1. User-level Nebutra memory: `~/.carina/CARINA.md`.
2. Project directories from repository root to the session workspace root.
3. In each project directory, first matching candidate wins:
   `CARINA.override.md`, `.carina/CARINA.override.md`, `CARINA.md`,
   `.carina/CARINA.md`, `AGENTS.override.md`, `AGENTS.md`.

The prompt section remains `PROJECT INSTRUCTIONS (Nebutra/Carina)`. Entries
include source labels so audit/debug output can explain why an instruction is in
scope. The existing budget remains a hard cap; later files are truncated rather
than letting a deep project doc crowd out task context.

## Provider Cache Strategy

Codex separates remote model discovery from cache policy using explicit refresh
strategies: online, offline, and online-if-uncached. Carina already has the
broader models.dev provider catalog; the missing piece is cache semantics.

Carina should add:

- `RefreshStrategy`: `online`, `offline`, `online_if_uncached`;
- cache envelope with `version`, `fetched_at`, `etag`, and `catalog`;
- ETag request/response handling with `If-None-Match` and `304` TTL renewal;
- legacy cache compatibility for existing plain catalog JSON files;
- CLI flags: `carina providers list [--refresh] [--offline]`;
- runtime behavior: default `online_if_uncached`, env-forced refresh uses
  `online`, daemon offline uses `offline`.

This keeps OpenCode's broad provider enumeration, while absorbing Codex's
cleaner discovery/cache boundary.

## Deferred From Phase A

- Nebutra Risk Review for approvals: important, but it changes the authority
  path for high-risk side effects and must land behind an explicit mode.
- Execpolicy overlay: useful as a rule explanation layer, but it touches kernel
  policy semantics.
- Turn net diff: valuable UX surface, but it should be based on Carina patch
  transactions rather than a direct port of Codex's in-memory patch tracker.
