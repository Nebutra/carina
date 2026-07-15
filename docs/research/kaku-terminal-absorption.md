# Kaku Terminal Absorption Review

Review date: 2026-07-14

Source reviewed: `tw93/kaku` at commit
`60561ff34a2954eccab98338cdbbddd6645a9feb`. Kaku is a macOS terminal product
built on WezTerm's terminal core; Carina is an Agent Runtime whose TUI runs
inside a host terminal. That ownership boundary is the primary trade-off.

## Decision Table

| Kaku mechanism | Decision for Carina | Reason and repository resolution |
| --- | --- | --- |
| Focus tracking, unread bell state, and notification suppression until focus changes | Absorb, adapted | Agent completions and governance requests need attention when the TUI is in the background. Carina reports terminal focus, latches unread attention, emits one terminal notification per blur interval, and clears the latch on focus. This preserves signal without notification storms. |
| Native primary/alternate screen and scrollback engine | Do not absorb | This is a terminal-emulator responsibility. Carina already offers its focused viewport, `--no-alt-screen` native scrollback, and the plain transcript pager. Embedding a second terminal core would duplicate selection, reflow, mouse, and history state. |
| Alternate-screen wheel forwarding and viewport pruning rules | Conditional reference | The host terminal already translates wheel input. Carina routes received wheel events to the focused surface. Its transcript is not currently pruned; if a bounded transcript is introduced, adopt Kaku's rule that a viewport which falls outside the retained range snaps to the live tail instead of clamping to a moving stale top. |
| Copy-on-select, quick-select, and clickable file paths | Reject at the TUI layer | These are valuable host-terminal features but conflict with application mouse reporting and terminal-native selection. Carina keeps modifier-assisted native selection and exposes copy/transcript actions without claiming pointer ownership for links. |
| Pane input broadcasting | Reject | Broadcasting keystrokes can duplicate governed commands, approvals, or destructive actions across sessions. Carina fan-out belongs in explicit workflow/task APIs with policy, audit, idempotency, and per-target results. |
| AI command suggestions inserted for review rather than auto-executed | Principle already absorbed | Carina separates composition from execution and routes command/tool effects through daemon policy and approval. A second shell-oriented AI injection path would duplicate the runtime and risk bypassing governance. |
| Cached shell bootstrap and stale-cache fallback | Retain the principle, not the implementation | Kaku optimizes shell and terminal startup. Carina keeps the TUI thin and daemon state durable; any future startup cache must be versioned, stale-tolerant, and must never cache policy or approval authority. |
| macOS-native window chrome, GPU rendering, tabs, panes, and image protocols | Reject | These define Kaku as a terminal application and would turn Carina into a macOS-only shell product. Carina remains portable across supported host terminals and keeps runtime behavior available to CLI, SDK, web, and editor clients. |

## Product Boundary

Kaku is most useful to Carina as a terminal-host reference, not as a component
library. The transferable invariant is focus-aware attention: background work
may signal once, visible state remains queryable, and returning focus clears
the unread indicator. Terminal emulation, panes, selection, hyperlinks, and
GPU rendering remain host responsibilities.

Reconsider a rejected mechanism only if measured user evidence shows that the
host boundary prevents a core Agent Runtime workflow. Any reconsideration must
preserve daemon-side policy, audit, idempotency, and explicit target selection.
