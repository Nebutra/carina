# TUI Layout UX Closure

Date: 2026-07-16

## Evidence

- [Grok Build](https://github.com/xai-org/grok-build): full-screen scrollback,
  compact mode, a composable status bar, prompt widget, focused permission
  surfaces, and PTY resize/scroll tests.
- [OpenAI Codex](https://github.com/openai/codex): a bottom pane that owns the
  composer and transient views, pure footer rendering, and explicit
  width-based footer fallbacks backed by snapshots.
- `claude-code-notes/07-UI与交互`: virtual message lists, a single prompt
  facade, declared cursor ownership, paste summaries, and permission UI that
  replaces normal input while an action is blocked.

## GAP

Carina already has authoritative session state, model selection, governed
permission overlays, folding, streaming markdown, and a responsive root
layout. The remaining layout problem is hierarchy: transcript and composer
use equally strong frames, a single task consumes a two-line dashboard, the
footer truncates one long sentence, and audit WAL events compete with the
conversation.

## Decision And Trade-offs

1. Use one unframed conversation document and one framed composer. This gains
   two rows and improves copyability; the composer border retains a clear input
   focus boundary.
2. Keep a one-line status surface, but choose complete width-specific variants
   instead of truncating a full line. Model and activity are mandatory;
   profile, sandbox, context, and shortcuts progressively yield.
3. Collapse one root task to a one-line rail. Hierarchical/multi-task work keeps
   the counted tree; `/tasks` remains the complete management surface.
4. Hide request/WAL telemetry from the primary transcript. Canonical replay
   and audit retain it; committed effects, failures, approvals, and recovery
   actions remain visible.
5. Do not add floating toasts or a second sidebar. They complicate terminal
   redraw, selection, narrow layouts, and screen-reader-friendly linear order.

## Acceptance

- Exactly one persistent rounded frame in the normal view.
- Single-task state uses one row.
- At 110 columns, mode/model/profile/sandbox/context and model entry are visible.
- At 48 columns, model and activity remain visible without overlap.
- Composer remains present and all rows fit at 16-110 columns.
- Request/WAL events do not enter the primary transcript; outcomes and
  actionable failures do.
- Unit, full Go, vet, and real PTY resize checks pass.
