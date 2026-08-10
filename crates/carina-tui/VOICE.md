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

### Successful edits in the dialogue stream

- Default **collapsed**: path · `+N` / `-M` · create preview (first lines, no green `+`).
- Expand restores full review window (create-only still uses line numbers without marker noise).

## Alignment checklist

- [x] Tool omission lines include expand key (`ToolLineOmitted` / `ToolCallsOmitted`)
- [x] Artifact truncation mentions expand + artifact path (`transcript::apply_tool_output`)
- [x] Collapsed Read/List groups use `×N · path…` title language; disclosure expands
- [x] `/help` and `?` expose the same command registry
- [ ] Remaining bare English in overlays (approval Esc hints) — prefer MessageId in a follow-up
