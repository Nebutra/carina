#!/usr/bin/env bash
# SPIKE gate runner (G1/G2/G3/G4) — drives the ratatui spike inside tmux
# against a real isolated daemon and captures evidence/ files.
# Prereqs: tmux; cargo build -p spike-tui-ratatui; bin/carina-daemon;
#          target/debug/carina-kernel-service; zig/zig-out/bin tools.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
RT="$ROOT/spikes/tui-ratatui/.rt"
EV="$ROOT/spikes/tui-ratatui/evidence"
BIN="$ROOT/target/debug/spike-tui-ratatui"
S=spike-rs
mkdir -p "$EV"
cargo build -p spike-tui-ratatui
"$ROOT/spikes/tui-ratatui/run-daemon.sh"
[ -S "$RT/d.sock" ] || { echo "no daemon socket"; exit 1; }
trap 'tmux kill-session -t $S 2>/dev/null || true; kill "$(cat "$RT/daemon.pid")" 2>/dev/null || true' EXIT

fresh() { tmux kill-session -t $S 2>/dev/null || true; tmux new-session -d -s $S -x 100 -y "$1"; sleep 0.5; }

echo "== G1 live =="
fresh 30
tmux send-keys -t $S "cd $ROOT && $BIN --scenario live --sock $RT/d.sock --workspace $RT/ws --evidence-out $EV/g1-evidence.jsonl --perf-out $EV/g1-perf.json --exit-after-secs 15" Enter
sleep 4;  tmux capture-pane -t $S -p > "$EV/g1-screen-mid.txt"
sleep 7;  tmux capture-pane -t $S -p > "$EV/g1-screen-end.txt"
sleep 5

echo "== G2 approval (allow path) =="
printf 'hello from carina\nsecond line kept intact\n' > "$RT/ws/hello.txt"; rm -f "$RT/ws/spike-approved.txt"
fresh 32
tmux send-keys -t $S "cd $ROOT && $BIN --scenario approval --sock $RT/d.sock --workspace $RT/ws --evidence-out $EV/g2-evidence.jsonl --perf-out $EV/g2-perf.json --exit-after-secs 60" Enter
sleep 3; tmux capture-pane -t $S -e -p > "$EV/g2-screen-prompt.txt"; tmux capture-pane -t $S -p > "$EV/g2-screen-prompt-plain.txt"
tmux send-keys -t $S a; sleep 2; tmux capture-pane -t $S -p > "$EV/g2-screen-cmdprompt.txt"
tmux send-keys -t $S a; sleep 3; tmux capture-pane -t $S -p > "$EV/g2-screen-resume.txt"
tmux send-keys -t $S q; sleep 1
[ -f "$RT/ws/spike-approved.txt" ] && echo "G2: approved command really ran (spike-approved.txt exists)"

echo "== G3 cjk =="
fresh 24
tmux send-keys -t $S "cd $ROOT && $BIN --scenario cjk --perf-out $EV/g3-perf.json" Enter
sleep 2; tmux send-keys -t $S -l 'carina 审批测试 with mixed 中英 text'; sleep 1
tmux capture-pane -t $S -p > "$EV/g3-screen-typed.txt"
tmux send-keys -t $S BSpace BSpace BSpace BSpace BSpace; sleep 1
tmux capture-pane -t $S -p > "$EV/g3-screen-backspace.txt"
tmux send-keys -t $S Enter; sleep 1; tmux send-keys -t $S -l '你好 world'; sleep 1
tmux capture-pane -t $S -p > "$EV/g3-screen-final.txt"
tmux display -p -t $S 'cursor after 你好 world: x=#{cursor_x} (expect 11)'
tmux send-keys -t $S Escape; sleep 1

echo "== G4 burst + idle =="
fresh 30
tmux send-keys -t $S "cd $ROOT && $BIN --scenario burst --perf-out $EV/g4-perf.json" Enter
sleep 12; tmux capture-pane -t $S -p > "$EV/g4-screen-postburst.txt"
PID=$(tmux capture-pane -t $S -p | grep -o 'pid=[0-9]*' | tail -1 | cut -d= -f2)
echo "sampling idle cpu of pid $PID for 30s..."
for _ in $(seq 1 30); do ps -o %cpu= -p "$PID"; sleep 1; done > "$EV/g4-idle-cpu-samples.txt"
python3 -c "v=[float(x) for x in open('$EV/g4-idle-cpu-samples.txt').read().split()]; print(f'idle cpu: samples={len(v)} mean={sum(v)/len(v):.3f}% max={max(v):.3f}%')" | tee -a "$EV/g4-idle-cpu-samples.txt"
tmux send-keys -t $S q; sleep 1

echo "== alignment check =="
python3 - <<'PY'
import unicodedata, glob, sys
def w(s):
    return sum(2 if unicodedata.east_asian_width(c) in ("W","F") else (0 if unicodedata.combining(c) else 1) for c in s)
bad = 0
for name in sorted(glob.glob("spikes/tui-ratatui/evidence/g*-screen-*.txt")):
    if name.endswith("g2-screen-prompt.txt"):  # ANSI-colored capture, skip width math
        continue
    widths = {w(l.rstrip("\n")) for l in open(name, encoding="utf-8") if any(ch in l for ch in "│┌└")}
    ok = len(widths) == 1
    bad += 0 if ok else 1
    print(f"{name}: bordered-line widths {sorted(widths)} {'ALIGNED' if ok else 'MISALIGNED'}")
sys.exit(1 if bad else 0)
PY
cat "$EV/g4-perf.json"
echo "ALL GATE SCRIPTS DONE — see spikes/tui-ratatui/evidence/"
