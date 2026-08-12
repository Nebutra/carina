# Carina TUI voice guide

Issue **#23**. Product copy is part of the product.

## Formula

```
[What happened.] [What we did about it.] [How to change the behavior.]
```

Examples:

| Bad | Good |
|-----|------|
| Auto-poking: 5 incomplete todos. | 5 incomplete todos. We poked it for you. `/poke off` to stop. |
| failed | No previous message to edit. |
| truncated | … +12 lines omitted · Ctrl-O expand |

## Rules

1. **User language first** — describe the user-visible outcome, not internal types (`ExecutionRun`, RPC method names).
2. **Past tense for actions we took** — "We cancelled the run." not "Cancellation initiated."
3. **Teach the escape hatch** — every truncation, degradation, or automatic behavior names how to undo or inspect (`Ctrl-O`, `/help`, `?`).
4. **Degrade transparently** — when color, graphics, or fonts fall back, either say so or leave a discoverable status path; never fail mysteriously.
5. **i18n boundary** — user-visible strings go through `MessageId` / `i18n.rs`. Bare `format!` for product copy is a bug (see closed #24).
6. **No internal slang** — avoid daemon, wire event, reducer, overlay stack in user-facing text.

## Templates

### Truncation
```
… {count} lines omitted · {key} expand
```

### Failure
```
{What failed}. {What is still true}. {What to try next}.
```

### Degradation
```
{Capability} is limited here ({reason}). {Fallback behavior}.
```

For font coverage, state the visible result and the recovery path. Do not use
font-rendering slang or claim that Carina detected an installed font:

```text
ASCII selected automatically for a limited terminal or legacy Windows console.
Boxes or misaligned symbols mean missing font coverage. Use /symbols or Settings to choose ASCII.
```

Automatic symbols may explain a legacy-terminal fallback, while an environment
override must say that it owns the active tier. Nerd Font copy always says that
Nerd Font Mono is required; Automatic never promises or implies Nerd detection.

### Waiting
```
{Progress noun}… {elapsed or cancel key}
```

### Confirmation
```
{Consequence}. {Scope}. Confirm or Esc to keep the current state.
```

In the Fullscreen patch workbench, use the compact consequence
`Rollback affects {count} files.` beside the affected-file actions. The footer
owns the localized Rollback/Cancel keycaps; do not repeat shortcut prose in the
body. Lifecycle labels and bounded-review continuation copy must come from
`MessageId` and must state that the complete transaction remains available for
verification and rollback.

### Plan review

Plan Review distinguishes three outcomes and always states what remains true:

```text
Approved: implementation was queued, or Build mode is active.
Revision requested: a localized draft was added to the composer; Plan mode remains active.
Review closed: Plan mode remains active; nothing was executed.
```

Comments use one-based `Lx` or `Lx-Ly` anchors. Revision drafts include a
localized request, a localized comments header, and localized line/range bullet
formats; they remain editable and are never submitted automatically. Approval
failures and stale reviews state that Plan mode remains active and teach
`/view-plan` as the recovery path. The footer and `/keymap` share one canonical
`A` approve, `S` revise, `C` comment, `M` mark range, `Q` close, and navigation
hint model.

### Collapsed success tool groups (V002)

```
{Verb} ×{N} · {path1}, {path2}, …
```

Example: `Read ×5 · src/a.rs, src/b.rs, …`

- Collapsed default is **title-only** (no member path rows).
- Expand via the disclosure glyph / tool expand key restores every member path.
- Failures, cancels, and diff-bearing tools never seal under a collapsed success group
  (expand forced; later successes do not join a failed group).

### Model / provider failures

```
{What stopped.} {Not auto-retried when side effects may have run.} {Check proxy/network or retry explicitly.}
```

Do not surface `modelrouter` / `reasoner failed` / raw `Client.Timeout` stacks in the
transcript failure cell. Technical detail belongs in audit / RoutingOutcome.

### Failure recovery

The model in the header is the next-run preference. A failure cell names the
model that actually served the failed run; it must never relabel history from
the current header selection.

Recovery actions use explicit outcome language:

```text
Retry current      Uses the session's current model and reasoning preference.
Replay original    Reuses the failed run's original governed configuration.
Details            Reveals linked run and event identities.
Copy ID            Copies the failure event or run identity.
```

Do not present Alt-only key chords as the primary recovery UI. Actions remain
visible, pointer-addressable, and keyboard-focusable. Once recovery is queued,
say that execution is running and remove both retry actions until a new terminal
outcome arrives.

### Successful edits in the dialogue stream

- Default **collapsed**: path · `+N` / `-M` · create preview (first lines, no green `+`).
- Expand restores full review window (create-only still uses line numbers without marker noise).

## Alignment checklist

- [x] Tool omission lines include expand key (`ToolLineOmitted` / `ToolCallsOmitted`)
- [x] Artifact truncation mentions expand + artifact path (`transcript::apply_tool_output`)
- [x] Collapsed Read/List groups use `×N · path…` title language; disclosure expands
- [x] `/help` and `?` expose the same command registry
- [x] Plan Review outcomes, comments, recovery, and complete key matrix use localized copy
- [ ] Remaining bare English in overlays (approval Esc hints) — prefer MessageId in a follow-up
