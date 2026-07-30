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

## Alignment checklist

- [x] Tool omission lines include expand key (`ToolLineOmitted` / `ToolCallsOmitted`)
- [x] Artifact truncation mentions expand + artifact path (`transcript::apply_tool_output`)
- [x] `/help` and `?` expose the same command registry
- [ ] Remaining bare English in overlays (approval Esc hints) — prefer MessageId in a follow-up
