# Spike: Bubble Tea v2 TUI (Go)

**DISPOSABLE CODE.** Time-boxed spike for `docs/plans/tui-stack-decision.md` §4 (Go/primary
candidate). Zero production polish. Everything here exists only to measure the gates below
against a **real** carina daemon + Rust kernel + Zig tools.

- Stack actually used: `charm.land/bubbletea/v2` **v2.0.8** (the real v2, resolved and fetched
  fine — no v1 fallback needed), `charm.land/bubbles/v2` v2.1.1 (textinput, viewport),
  `charm.land/lipgloss/v2` v2.0.5.
- Reused production plumbing directly, zero new plumbing code: `go/rpc.Client` (Dial +
  `Call` + `ReadNotification`, two-connection demux pattern), `go/kernel` wire structs
  (`Decision`, `Patch`). The daemon-spawn recipe is copied from `scripts/ci-gates.sh`.
- Only files touched outside `spikes/`: `go.mod` / `go.sum` (Charm deps added).

## Gate table

| Gate | Verdict | Evidence |
|------|---------|----------|
| G1 live-daemon | **PASS** | Real `carina-daemon` + `carina-kernel-service` + Zig tools spawned in a temp dir; TUI dialed the unix socket twice (calls + `session.events.stream`), attached to a real session; 12 `command.exec` calls from a separate process produced 36 live events (CommandStarted/Output/Exited) that streamed into a scrolling viewport transcript mid-burst. Capture: `evidence/g1-transcript-after.txt`. |
| G2 approval | **PASS** (see honesty note) | (a) Patch: `/patch` → real `workspace.patch.propose` → kernel returned a real patch-transaction (`approval_status: "pending"`, risk 2) → approval overlay rendered with the **real colored unified diff** as body (`evidence/g2-patch-overlay.txt`; ANSI colors preserved in `g2-patch-overlay-ansi.txt` — adds in 38;5;42, dels in 38;5;203, headers 38;5;39) → `y` sent `workspace.patch.apply` → `status=applied`, file verified on disk (`g2-patch-resume.txt`). (b) decision_id roundtrip: `/cmd mv readme.txt renamed.txt` → daemon returned `requires_approval` with `decision_id perm_18c08f20178bae80` → overlay → `y` sent `task.action.approve {decision_id}` → daemon executed the queued command, CommandStarted/Exited events streamed in as the visible resume, `mv` really ran on disk (`g2-cmd-overlay.txt`, `g2-cmd-resume.txt`). Deny path wired via `task.action.deny` (not captured). |
| G3 cjk | **PASS** (automated) / **PENDING-HUMAN** (IME composition) | TUI ran in tmux (110×32 PTY). Transcript preloaded with the required zh lines (`补丁干净落地。无惊无险，本该如此。`, `审计链校验通过：1,204 条记录`, full-width width-test line). Pre-composed CJK bytes sent through the PTY into the textinput (`carina 审批测试 with mixed 中英 text`); 7 backspaces deleted ` 中英 text` grapheme-by-grapheme (each CJK char = one keypress); bracketed paste of 10 CJK lines collapsed to `[Pasted 10 lines]`. Alignment check: every one of the 32 bordered rows in the capture measures **exactly 108 display columns** (east-asian-width math) — no half-width tearing, borders flush. Capture: `evidence/g3-cjk-screen.txt`. IME composition can't be automated — binary is built; 5-min manual checklist below. |
| G4 perf | **PASS** | Synthetic burst 100 events/s × 10 s (1000 events, transcript grew to 1001 lines), instrumented in-process at the renderer's output writer: **frame render p95 = 10.98 ms** (View-start → flush-end; gate < 16 ms), frame p50 5.8 ms, max 11.95 ms; View() build p95 1.33 ms; TTY write p95 0.03 ms. 1000 events coalesced into 601 frames by the v2 renderer's FPS cadence; end-to-end event→flush p95 was 16.0 ms — that number is dominated by waiting for the next ~60 fps tick, not render cost (decision doc's event-to-paint criterion is < 33 ms; also passes). Idle CPU after the burst: **0.60 % avg** over 30 × 1 s `ps -o %cpu` samples (gate < 1 %); residual load is the textinput cursor blink. Data: `evidence/g4-perf.json`. |

### G2 honesty note

The pending approvals were **driven by the TUI itself over RPC** (`workspace.patch.propose`,
`command.exec` returning `requires_approval` + queuing the command daemon-side), then resolved
with the real production RPCs (`task.action.approve`/`task.action.deny`/`workspace.patch.apply`)
against the live daemon — the decision roundtrip, the queued-command execution on approval, and
the streamed resume events are all real. What was **not** exercised live is the
`permission.request` *event* path: the daemon only publishes that envelope from the interactive
agent-loop (`awaitInteractiveApproval` in `go/daemon/approval.go`), which requires a real model
provider run — too deep for the time box. The renderer handles the `permission.request` event
type (renders `⚿ decision_id=…`), but that code path is untested against a live emission. The
payload shape was taken from `go/daemon/approval.go:58-68` (the emitting code), not from a
captured event.

## How to run

```sh
# 0. one-time: binaries (kernel: cargo build -p carina-kernel --bin carina-kernel-service)
go build -o bin/carina-daemon ./apps/carina-daemon
go build -o spikes/tui-bubbletea/spike-tui ./spikes/tui-bubbletea

# 1. real daemon + kernel + zig tools in a temp dir
./spikes/tui-bubbletea/harness/start-daemon.sh /tmp/carina-spike
#    -> prints SOCKET=/tmp/carina-spike/d.sock ; kill $(cat /tmp/carina-spike/daemon.pid) when done

# 2. the TUI (creates its own safe-edit session in the daemon's temp workspace)
./spikes/tui-bubbletea/spike-tui -socket /tmp/carina-spike/d.sock \
    -workspace /tmp/carina-spike/ws -cjk-demo
#    in-TUI: /patch (patch approval w/ colored diff) · /cmd mv a b (decision_id approval)
#            /burst (synthetic load) · ctrl+c quits · esc closes an overlay

# 3. perf gate standalone (no daemon needed)
./spikes/tui-bubbletea/spike-tui -bench -bench-rate 100 -bench-secs 10 -perf-log /tmp/perf.json

# 4. generating external events for G1: run several
#    {"method":"command.exec","params":{"session_id":…,"argv":["echo","hi"]}} against the socket
```

## Manual IME checklist (PENDING-HUMAN, ~5 min)

Run step 2 above in Terminal.app/iTerm2/WezTerm with **macOS Pinyin** active (repeat on a Linux
VM with fcitx5/Wayland and on Windows Terminal + MS Pinyin if available):

1. Focus the input box (focused on start). Type `nihao` — does the IME candidate window appear
   anchored at/near the input caret (right of `❯ >`), not at 0,0 / bottom-left / a stale cell?
2. Commit `你好` with Space/Enter-in-IME. Does the committed text insert at the caret, once
   (no doubled characters)?
3. Type mixed `ceshi测试abc` — caret lands correctly between full-width and half-width runs?
4. Left/right-arrow across the CJK — cursor moves one *character cell pair* per press, never
   splits a wide glyph?
5. Backspace each char — deletes whole graphemes, columns stay aligned?
6. Open the approval overlay (`/patch`) while text sits in the input — does the overlay take
   over cleanly, and Esc restore the input with caret intact?
- Note: in-TUI composition *preview* is not expected to work anywhere (decision doc R15);
  what matters is candidate-window placement (R13). Record pass/fail per platform in this file.
- Known upstream risk to re-check at productization: bubbletea#874 (candidate window
  misplacement, Linux/fcitx5).

## Framework friction encountered (honest notes)

1. **`tea.WithOutput` silently disables rendering unless the writer implements `term.File`.**
   Wrapping stdout in a plain `io.Writer` for frame instrumentation produced a *blank screen,
   no error, no log* — v2 only treats the output as a terminal if it has `Fd()/Read/Close`
   (`tty_unix.go: if f, ok := p.output.(term.File)`). Undocumented; cost ~30 min. The fix
   (forward `Fd`) is easy but you have to read framework source to find it.
2. **bubbles v2 `textinput` panics on width < -1** (`placeholderView`: `make([]rune, m.Width()+1)`
   → `makeslice: len out of range`) when layout math goes negative before the first
   `WindowSizeMsg`. It should clamp; you must clamp for it. Related smell: `placeholderView`
   mixes **display width** (`lipgloss.Width`) with **rune indexing** (`p[1:minWidth]`) — with a
   CJK placeholder those units differ by ~2×; it didn't corrupt anything after clamping, but
   that function is worth upstream scrutiny before shipping zh placeholders.
   (Credit: bubbletea's panic recovery restored the terminal and printed a clean stack — good.)
3. **`tea.PasteMsg.Content` arrives with `\r` line endings** (at least via tmux bracketed
   paste), not `\n` — naive newline counting sees "one line". Undocumented; normalize CRs first.
4. **v2 docs are thin.** The import path (`charm.land/...` vs `github.com/charmbracelet/...`,
   both resolve), the moved `AltScreen` (now a `View` field, not a program option), and
   `Update(Msg) (Model, Cmd)` + `View() tea.View` signature changes are mostly discoverable
   from source and `bubbles/UPGRADE_GUIDE_V2.md`, not from guides. Expect source-diving.
5. **Overlay compositing:** this spike replaces the whole frame content for the approval prompt
   (`lipgloss.Place`) instead of using v2's young Layers API — the decision doc's "overlays
   exist in v2 layers (young — exercise in spike)" question is therefore **still open**; the
   Layers/declared-cursor story needs its own look at productization time.
6. **What was pleasantly free:** daemon integration was near-zero work exactly as the decision
   doc predicted — `rpc.Dial` twice, `program.Send` from the stream goroutine, typed kernel
   structs reused; CJK width handling in viewport/textinput/lipgloss borders was correct
   out-of-the-box (108/108 columns on every row, grapheme backspace w/o custom code); renderer
   coalescing kept 100 ev/s at ~60 fps with ~1.3 ms View cost at 1000 transcript lines with a
   dumb `[]string` line cache.

## Scope shortcuts (so nobody mistakes this for more than it is)

- Static/dynamic region split (R3, native scrollback) not attempted — altscreen viewport only.
- Scoped grants (approve-for-session/project) send the same RPC as approve-once; persistence is
  P1.1 production work. Deny leaves proposed patches pending (no patch-deny RPC exists).
- Brand tokens, microcopy engine, reconnect, NO_COLOR: out of spike scope.
- Line cache is append-only `[]string` + `SetContentLines`; no per-entry invalidation.
- `evidence/*.txt` are raw `tmux capture-pane` dumps from the live runs described above.
