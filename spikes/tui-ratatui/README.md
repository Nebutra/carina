# SPIKE: ratatui (Rust) TUI — tui-stack-decision §4.3

**Disposable code.** Source lives at `crates/spike-tui-ratatui/` (workspace member added to
root `Cargo.toml` — the only production-file edit this spike makes). No production polish;
shortcuts are called out honestly below. Evidence captures: `spikes/tui-ratatui/evidence/`.

Run date: 2026-07-09, macOS (Darwin 25.5), 100×24–32 tmux panes, ratatui **0.30.2** +
crossterm 0.29 (via ratatui default backend), rustc 1.96.1.

## Gate table

| Gate | Verdict | Evidence |
|------|---------|----------|
| G1 live-daemon | **PASS** | Real `bin/carina-daemon` + Rust kernel spawned isolated (`run-daemon.sh`, ci-gates pattern). Two unix-socket connections: calls + `session.events.stream` subscription. 24 real events (CommandStarted/Output/Exited from 8 driver `command.exec` calls incl. zh output) streamed and rendered in a follow-scrolling transcript. `evidence/g1-screen-mid.txt` (mid-stream), `g1-screen-end.txt` (scrolled), `g1-perf.json` |
| G2 approval | **PASS** (see honesty note) | Patch flow: `workspace.patch.propose` returned a real `PatchTransaction` whose **daemon-produced unified diff** is the approval prompt body, colored (green/red/cyan/bold spans — ANSI codes visible in `g2-screen-prompt.txt`, layout in `g2-screen-prompt-plain.txt`). Allow (real `a` keypress via tmux PTY) → `workspace.patch.apply` → `status=applied`. Then a **real pending daemon-side approval**: `command.exec touch …` → `requires_approval` + `decision_id` (daemon `pendingCmds`), prompt shown (`g2-screen-cmdprompt.txt`), allow → `task.action.approve {decision_id}` round trip → command executed, exit=0, its events streamed live = visible resume (`g2-screen-resume.txt`); `spike-approved.txt` appeared on disk. Deny path also exercised: `task.action.deny` → `"denied by operator: spike deny"` (`g2-screen-deny.txt`, `g2-deny-evidence.jsonl`). |
| G3 cjk | **PASS** (rendering + committed input) / **PENDING-HUMAN** (IME composition) | Transcript with required lines `补丁干净落地。无惊无险，本该如此。` and `审计链校验通过：1,204 条记录` plus half/full-width boundary lines; pre-composed CJK bytes typed through the tmux PTY into the input box (`tmux send-keys -l`). Column alignment verified programmatically: every bordered line in all 8 captures is exactly 100 display columns (east-asian-width math) — no tearing (`g3-screen-*.txt`). Backspace ×5 deleted ` text` char-by-char, not byte-wise. Hardware cursor pinned to the caret via `Frame::set_cursor_position`: after `你好 world` cursor at x=11 (1+2+2+1+5); arrowing left across `好`/`你` jumps 2 columns each (`g3-cursor-evidence.txt`) — the R13 IME-anchor architecture works. True IME composition can't be automated: checklist below. |
| G4 perf | **PASS** | Synthetic burst 1000 events at measured **100.0–100.1/s over ~10.0s**: ~185 frames drawn (event coalescing), **p95 frame render 3.8–8.3 ms across three runs** (p50 1.8–4.0, worst single frame 32.5 ms once at startup; gate is p95 < 16 ms) instrumented in-process around `terminal.draw`. Idle CPU after burst, 30×1s `ps -o %cpu` samples: **mean 0.0%, max 0.0%** every run (< 1% gate; `ps` resolution 0.1%). `g4-perf.json`, `g4-idle-cpu-samples.txt`, `g4-screen-postburst.txt` |

### G2 honesty note

The plan's ideal script (`permission.request` event from a paused agent task, resolved via
`task.approval.resolve`) requires the daemon's **agent loop**, which needs a live model
provider — too deep for the time box. What the spike does instead is fully real, no captured
payloads needed:

- the **colored-diff prompt body** is a real `PatchTransaction.diff` produced by the daemon/kernel;
- the **pending approval object** is real daemon state (`pendingCmds` keyed by `decision_id`,
  created by `command.exec` returning `requires_approval` under safe-edit);
- the **decision round trip** is the real `task.action.approve` / `task.action.deny` RPC, and
  the approved command's execution events arrive over the live event stream (the "resume").

Not exercised: `task.approval.resolve` + `permission.request` (agent-loop interactive path),
and `workspace.patch.apply` does not itself verify a prior capability approval (the kernel
records the approval at apply time with the given approver) — flagged for P1.1 productization.

## How to run

```sh
cargo build -p spike-tui-ratatui          # needs crates.io
# everything, automated (tmux required):
spikes/tui-ratatui/run-gates.sh
# or by hand:
spikes/tui-ratatui/run-daemon.sh          # isolated daemon+kernel under .rt/
./target/debug/spike-tui-ratatui --scenario live     --sock spikes/tui-ratatui/.rt/d.sock --workspace $PWD/spikes/tui-ratatui/.rt/ws
./target/debug/spike-tui-ratatui --scenario approval --sock spikes/tui-ratatui/.rt/d.sock --workspace $PWD/spikes/tui-ratatui/.rt/ws   # a=allow d=deny
./target/debug/spike-tui-ratatui --scenario cjk      # offline; Esc quits
./target/debug/spike-tui-ratatui --scenario burst --perf-out /tmp/perf.json
```

Flags: `--exit-after-secs N`, `--auto-ms N` (headless auto-approve fallback; the gate runs
used real keypresses through the PTY), `--evidence-out FILE` (JSONL of every RPC decision).

## 5-minute manual IME checklist — PENDING-HUMAN

Run `./target/debug/spike-tui-ratatui --scenario cjk` in Terminal.app/iTerm2 (macOS Pinyin),
then in a Linux VM under fcitx5 (and ideally Windows Terminal + MS Pinyin):

1. Focus the input box; switch to Pinyin. Type `shenpi` — **candidate window must appear at
   the caret cell inside the bordered input box** (bottom of screen), not at 0,0 / stale spot.
2. Commit `审批`; text inserts at caret, cursor lands after it, border stays intact.
3. Type mixed `test测试test`; arrow through it — cursor moves 1 col over ASCII, 2 over CJK.
4. Backspace through the committed CJK — deletes whole characters, never half a glyph.
5. Move caret mid-string (◀◀), compose again — candidate window follows the caret cell.
6. Resize the terminal during composition — no panic, borders re-wrap, caret stays correct.

Expected to pass on macOS given the cursor evidence above (terminal places the candidate
window at the hardware cursor, which the spike pins to the caret); fcitx5 is the platform
where Bubble Tea's #874 bites, so it's the decisive run.

## G9-rs: plumbing LoC the gates forced (feeds the decision)

| Piece | LoC (total / code) | Notes |
|---|---|---|
| `src/rpc.rs` — first Rust JSON-RPC client | **132 / 106** | unix dial, NDJSON framing, id correlation, interleaved-notification skip, second-connection stream subscribe + forward. Mirrors `go/rpc/client.go` (149 LoC). |
| Typed wire models | **0 written** | Spike cheats with `serde_json::Value` + string indexing everywhere. Fine for a spike; a product client would hand-write the ~500–1000 LoC of Session/Event/Decision/Patch/Task structs the decision doc prices in, plus keep them from drifting against the Go structs forever. |
| Diff colorizer | 28 | manual spans; `ansi-to-tui` not needed because `PatchTransaction.diff` is plain unified diff |
| TUI itself (`main.rs`) | 764 | scenarios, transcript, approval overlay, input, perf instrumentation |

Bottom line: **~106 LoC of real client bought all four gates** because the daemon's NDJSON
protocol is trivially clean — but only by dodging the typed layer; the §2.2 estimate
(300–500 client + 500–1000 types) stands for production, with `Value`-indexing bugs
(e.g. `r["result"]["exit_code"]` silently `null` on deny) as the preview of why.

## Framework friction notes (honest)

- **ratatui 0.30 was almost frictionless.** Compiled first try against the written-from-docs
  API. `ratatui::init()/restore()`, `Layout::vertical`, `Block::bordered`,
  `Frame::set_cursor_position(Position)` — all as documented. The 0.30 crate split
  (ratatui-core/-widgets/-crossterm) is invisible from the facade crate.
- **CJK width handling needed zero code for rendering.** Wide glyphs never tore a border in
  any capture; the only width math written was 3 lines for the input caret column
  (`unicode-width`), and it agreed exactly with the terminal.
- **Crossterm re-export quirk**: `Event::Paste`/bracketed-paste is feature-gated behind
  ratatui's crossterm dep; enabling it means depending on crossterm directly and matching
  ratatui's version. Spike skipped paste-collapse (R14) rather than fight it — pasted text
  arrives as rapid Char events, which worked for the CJK PTY test.
- **Immediate mode makes the perf story easy but manual.** Event coalescing (drain channel,
  draw once) and only-draw-when-dirty had to be hand-written (~15 lines); nothing in the
  framework does it for you. That is exactly the "you own the event loop" tax — trivially
  payable at spike scale, needs discipline at product scale (per-entry render caching, R18,
  was not needed at 1k lines but would be at 10k).
- **No async needed.** Three OS threads + `mpsc` covered calls/stream/driver; the crossterm
  `poll(50ms)` loop can't be woken by the channel (poll watches stdin only), so worst-case
  event-to-paint latency is one poll tick — fine here, but a product would want the
  crossterm event-stream + a select'able waker instead.
- **The real cost is not the framework, it's the second language.** Everything RPC was
  rewritten by hand and is already semantically thinner than `go/rpc` (no scope negotiation,
  no TCP/gateway, no reconnect); every payload access is stringly-typed. The friction lives
  there, not in ratatui.
- **Environment gotchas** (not framework): the daemon readiness loop in `run-daemon.sh` can
  race debug-kernel startup (harmless); tmux session names collide with the parallel Go spike
  (`spike-rs` used here); `tmux capture-pane` strips trailing spaces, so the alignment check
  only measures bordered lines.
