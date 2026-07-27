#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v tmux >/dev/null 2>&1 || {
  echo "rust-tui-journey: tmux is required" >&2
  exit 127
}

for path in \
  bin/carina \
  bin/carina-daemon \
  target/debug/carina-ui \
  target/release/carina-kernel-service; do
  [[ -x "$path" ]] || {
    echo "rust-tui-journey: missing executable $path" >&2
    exit 1
  }
done

WORK="$(mktemp -d "${CARINA_E2E_TMPDIR:-/tmp}/carina-rust-tui-e2e.XXXXXX")"
HOME_DIR="$WORK/home"
WORKSPACE="$WORK/workspace"
STAGE="$WORK/stage"
EXIT_FILE="$WORK/ui-exit"
SESSION="carina-rust-tui-$$"
SCREEN=""
FAKE_DAEMON_PID=""

cleanup() {
  if [[ -n "$FAKE_DAEMON_PID" ]]; then
    kill "$FAKE_DAEMON_PID" >/dev/null 2>&1 || true
  fi
  if [[ -x "$STAGE/carina" && -d "$HOME_DIR" && -d "$WORKSPACE" ]]; then
    env -i HOME="$HOME_DIR" PATH="$STAGE:/usr/bin:/bin" \
      "$STAGE/carina" runtime stop --workspace "$WORKSPACE" --force \
      >/dev/null 2>&1 || true
  fi
  TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
  for _ in $(seq 1 20); do
    rm -rf "$WORK" >/dev/null 2>&1 || true
    [[ ! -e "$WORK" ]] && break
    sleep 0.05
  done
}
trap cleanup EXIT

mkdir -p "$HOME_DIR" "$WORKSPACE" "$STAGE"
install -m 755 bin/carina bin/carina-daemon "$STAGE"
install -m 755 target/debug/carina-ui target/release/carina-kernel-service "$STAGE"
for name in carina-scan carina-grep carina-diff carina-run carina-pty carina-patch-native; do
  [[ -x "zig/zig-out/bin/$name" ]] || {
    echo "rust-tui-journey: missing Zig tool $name" >&2
    exit 1
  }
  install -m 755 "zig/zig-out/bin/$name" "$STAGE"
done

capture() {
  SCREEN="$(TMUX_TMPDIR="$WORK" tmux capture-pane -p -t "$SESSION" -S - 2>/dev/null || true)"
}

wait_for_text() {
  local wanted="$1"
  for _ in $(seq 1 150); do
    capture
    if grep -Fq "$wanted" <<<"$SCREEN"; then
      return 0
    fi
    sleep 0.1
  done
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: timed out waiting for $wanted" >&2
  return 1
}

wait_without_text() {
  local unwanted="$1"
  for _ in $(seq 1 150); do
    capture
    if ! grep -Fq "$unwanted" <<<"$SCREEN"; then
      return 0
    fi
    sleep 0.1
  done
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: timed out waiting for $unwanted to disappear" >&2
  return 1
}

TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina' --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$EXIT_FILE'; sleep 300"

wait_for_text "Choose language"
grep -Fq "Provider" <<<"$SCREEN"
grep -Fq "Model" <<<"$SCREEN"
grep -Fq "Conversation" <<<"$SCREEN"
if grep -Fq "Enter submit" <<<"$SCREEN"; then
  echo "rust-tui-journey: composer appeared before prerequisites" >&2
  exit 1
fi

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Connect a provider"
grep -Fq "Provider identity comes before model selection and conversation." <<<"$SCREEN"
if grep -Fq "Choose model" <<<"$SCREEN" || grep -Fq "Enter submit" <<<"$SCREEN"; then
  echo "rust-tui-journey: model/composer leaked before provider readiness" >&2
  exit 1
fi

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
for _ in $(seq 1 50); do
  [[ -f "$EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -f "$EXIT_FILE" ]] || {
  echo "rust-tui-journey: UI did not exit" >&2
  exit 1
}
[[ "$(cat "$EXIT_FILE")" == "6" ]] || {
  echo "rust-tui-journey: provider cancel must exit degraded (6), got $(cat "$EXIT_FILE")" >&2
  exit 1
}

grep -Eq '"tui_locale"[[:space:]]*:[[:space:]]*"en"' "$HOME_DIR/.carina/config.json"
descriptor_count="$(find "$HOME_DIR/.carina/runtimes/v1" -name descriptor.json -type f | wc -l | tr -d ' ')"
[[ "$descriptor_count" == "1" ]] || {
  echo "rust-tui-journey: expected one workspace runtime, got $descriptor_count" >&2
  exit 1
}
[[ ! -e "$HOME_DIR/.carina/daemon.sock" ]] || {
  echo "rust-tui-journey: legacy shared daemon socket was created" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

GOV_SOCKET="$WORK/governance.sock"
GOV_EXIT_FILE="$WORK/governance-ui-exit"
python3 - "$GOV_SOCKET" <<'PY' &
import json
import os
import socket
import sys
import threading
import time

socket_path = sys.argv[1]
resolved = threading.Event()
checkpoint_restored = threading.Event()
checkpoint_resumed = threading.Event()
paused_resumed = threading.Event()


def send(stream, value):
    stream.write((json.dumps(value) + "\n").encode())
    stream.flush()


def handle(connection):
    with connection:
        stream = connection.makefile("rwb")
        while True:
            line = stream.readline()
            if not line:
                return
            request = json.loads(line)
            request_id = request.get("id")
            method = request.get("method")
            if method == "session.events.stream":
                session_id = request.get("params", {}).get("session_id")
                send(stream, {"jsonrpc": "2.0", "id": request_id, "result": {"cursor": 0}})
                if session_id == "sess_checkpoint":
                    if not checkpoint_resumed.wait(10):
                        return
                    time.sleep(0.5)
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "TaskCreated", "event_id": "evt_checkpoint_completed",
                        "session_id": "sess_checkpoint", "task_id": "task_cp", "raw_cursor": 1,
                        "payload": {"status": "completed", "summary": "Resumed checkpoint task complete"}
                    }})
                    continue
                if session_id == "sess_paused":
                    if not paused_resumed.wait(10):
                        return
                    time.sleep(0.5)
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "TaskCreated", "event_id": "evt_paused_completed",
                        "session_id": "sess_paused", "task_id": "task_paused", "raw_cursor": 1,
                        "payload": {"status": "completed", "summary": "Returning paused task complete"}
                    }})
                    continue
                if session_id == "sess_history":
                    continue
                send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                    "type": "permission.request", "event_id": "evt_request",
                    "session_id": "sess_1", "task_id": "task_1", "raw_cursor": 1,
                    "decision_id": "perm_1", "capability": "CommandExec",
                    "resource": "cargo test", "reason": "workspace policy",
                    "label": "Run verification"
                }})
                if not resolved.wait(10):
                    return
                time.sleep(0.5)
                send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                    "type": "TaskCreated", "event_id": "evt_resolution",
                    "session_id": "sess_1", "task_id": "task_1", "raw_cursor": 2,
                    "payload": {"status": "approval_resolved", "decision_id": "perm_1", "granted": True}
                }})
                send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                    "type": "TaskCreated", "event_id": "evt_completed",
                    "session_id": "sess_1", "task_id": "task_1", "raw_cursor": 3,
                    "payload": {"status": "completed", "summary": "Verification complete"}
                }})
                continue
            if method == "runtime.initialize":
                result = {"runtime_version": "test", "protocol_version": "1.3.0", "projection_version": "1.0.0"}
            elif method == "model.list":
                result = {"default_model": "test/model", "providers": [{
                    "id": "test", "name": "Test", "registered": True, "available": True,
                    "models": [{"id": "test/model", "name": "Test Model", "available": True}]
                }]}
            elif method == "session.list":
                result = [
                    {"session_id": "sess_checkpoint", "workspace_root": "/tmp", "status": "active", "next_model": "test/model", "latest_task_id": "task_cp", "task_status": "running" if checkpoint_resumed.is_set() else ("paused" if checkpoint_restored.is_set() else "completed")},
                    {"session_id": "sess_paused", "workspace_root": "/tmp", "status": "active", "next_model": "test/model", "latest_task_id": "task_paused", "task_status": "running" if paused_resumed.is_set() else "paused"},
                    {"session_id": "sess_history", "workspace_root": "/tmp", "status": "active", "next_model": "test/model", "latest_task_id": "task_hist_2", "task_status": "completed"}
                ]
            elif method == "session.resume":
                session_id = request.get("params", {}).get("session_id", "sess_1")
                result = {"session_id": session_id, "workspace_root": "/tmp", "status": "active", "next_model": "test/model"}
            elif method == "session.items":
                session_id = request.get("params", {}).get("session_id")
                if session_id == "sess_history":
                    result = [
                        {"type": "item.recorded", "session_id": session_id, "turn_id": "turn_1", "task_id": "task_hist_1", "item_id": "user_1", "item": {"id": "user_1", "type": "user", "status": "completed", "task_id": "task_hist_1", "details": {"prompt": "first prompt"}}},
                        {"type": "item.recorded", "session_id": session_id, "turn_id": "turn_2", "task_id": "task_hist_2", "item_id": "user_2", "item": {"id": "user_2", "type": "user", "status": "completed", "task_id": "task_hist_2", "details": {"prompt": "second prompt"}}}
                    ]
                else:
                    result = []
            elif method == "session.fork":
                send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32603, "message": "synthetic fork failure"}})
                continue
            elif method == "session.checkpoint.list":
                result = [
                    {"checkpoint_id": "task_cp:1", "parent_checkpoint_id": "", "created_at": "2026-07-27T10:00:00Z", "sequence": "00000000000000000001", "task_id": "task_cp", "session_id": "sess_checkpoint", "turn": 1, "summary": "before setup", "applied_patches": []},
                    {"checkpoint_id": "task_cp:2", "parent_checkpoint_id": "task_cp:1", "created_at": "2026-07-27T11:00:00Z", "sequence": "00000000000000000002", "task_id": "task_cp", "session_id": "sess_checkpoint", "turn": 2, "summary": "before refactor", "applied_patches": ["patch_1"]}
                ]
            elif method == "session.checkpoint.preview":
                result = {"checkpoint": {"checkpoint_id": "task_cp:2", "parent_checkpoint_id": "task_cp:1", "created_at": "2026-07-27T11:00:00Z", "sequence": "00000000000000000002", "task_id": "task_cp", "session_id": "sess_checkpoint", "turn": 2, "summary": "before refactor", "applied_patches": ["patch_1"]}, "conversation_turns": 2, "summary": "before refactor", "rollback_patches": ["patch_2"], "will_resume": "paused"}
            elif method == "session.checkpoint.restore":
                result = {"restored": True, "checkpoint_id": "task_cp:2", "task_id": "task_cp", "turn": 2, "rolled_back": ["patch_2"], "status": "paused", "idempotent": False, "reconciliation_required": False, "journal_cleanup_pending": False}
                checkpoint_restored.set()
            elif method == "task.resume":
                task_id = request.get("params", {}).get("task_id")
                if task_id == "task_paused":
                    result = {"task_id": "task_paused", "session_id": "sess_paused", "status": "running"}
                    paused_resumed.set()
                else:
                    result = {"task_id": "task_cp", "session_id": "sess_checkpoint", "status": "running"}
                    checkpoint_resumed.set()
                send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                continue
            elif method == "task.approval.resolve":
                result = {"decision_id": "perm_1", "resolved": True, "scope": "once"}
                send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                resolved.set()
                continue
            else:
                send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32601, "message": method}})
                continue
            send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})


if os.path.exists(socket_path):
    os.unlink(socket_path)
server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
server.bind(socket_path)
server.listen()
while True:
    connection, _ = server.accept()
    threading.Thread(target=handle, args=(connection,), daemon=True).start()
PY
FAKE_DAEMON_PID="$!"
for _ in $(seq 1 100); do
  [[ -S "$GOV_SOCKET" ]] && break
  sleep 0.05
done
[[ -S "$GOV_SOCKET" ]] || {
  echo "rust-tui-journey: fake governance daemon did not start" >&2
  exit 1
}

SESSION="carina-rust-tui-governance-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_1 --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$GOV_EXIT_FILE'; sleep 300"

wait_for_text "Approval required"
grep -Fq "Run verification" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "waiting for durable confirmation"
grep -Fq "Approval required" <<<"$SCREEN"
wait_for_text "Approval perm_1 durably resolved"
wait_without_text "Approval required"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c
for _ in $(seq 1 50); do
  [[ -f "$GOV_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -f "$GOV_EXIT_FILE" ]] || {
  echo "rust-tui-journey: governance UI did not exit" >&2
  exit 1
}
[[ "$(cat "$GOV_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: governance UI exit = $(cat "$GOV_EXIT_FILE")" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
CHECKPOINT_EXIT_FILE="$WORK/checkpoint-ui-exit"
SESSION="carina-rust-tui-checkpoint-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_checkpoint --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$CHECKPOINT_EXIT_FILE'; sleep 300"

wait_for_text "Enter submit"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" /checkpoints Enter
wait_for_text "Checkpoint recovery"
grep -Fq "task_cp:2" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Review restore impact"
grep -Fq "patch_2" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Confirm destructive restore"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Checkpoint restored"
grep -Fq "Task status: paused" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Resume"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" /resume Enter
wait_for_text "Resumed task task_cp"
wait_for_text "completed"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c
for _ in $(seq 1 50); do
  [[ -f "$CHECKPOINT_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -f "$CHECKPOINT_EXIT_FILE" ]] || {
  echo "rust-tui-journey: checkpoint UI did not exit" >&2
  exit 1
}
[[ "$(cat "$CHECKPOINT_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: checkpoint UI exit = $(cat "$CHECKPOINT_EXIT_FILE")" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
PAUSED_EXIT_FILE="$WORK/paused-ui-exit"
SESSION="carina-rust-tui-paused-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_paused --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$PAUSED_EXIT_FILE'; sleep 300"

wait_for_text "Task task_paused is paused"
grep -Fq "Resume" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" /resume Enter
wait_for_text "Resumed task task_paused"
wait_for_text "completed"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c
for _ in $(seq 1 50); do
  [[ -f "$PAUSED_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -f "$PAUSED_EXIT_FILE" ]] || {
  echo "rust-tui-journey: returning paused UI did not exit" >&2
  exit 1
}
[[ "$(cat "$PAUSED_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: returning paused UI exit = $(cat "$PAUSED_EXIT_FILE")" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
HISTORY_EXIT_FILE="$WORK/history-ui-exit"
SESSION="carina-rust-tui-history-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_history --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$HISTORY_EXIT_FILE'; sleep 300"

wait_for_text "second prompt"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" draft
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Press Esc again to edit an earlier prompt"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Choose a prompt in the conversation"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "draft"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Press Esc again to edit an earlier prompt"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Choose a prompt in the conversation"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Could not branch from this prompt"
grep -Fq "sess_history" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "draft"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c
for _ in $(seq 1 50); do
  [[ -f "$HISTORY_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -f "$HISTORY_EXIT_FILE" ]] || {
  echo "rust-tui-journey: history edit UI did not exit" >&2
  exit 1
}
[[ "$(cat "$HISTORY_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: history edit UI exit = $(cat "$HISTORY_EXIT_FILE")" >&2
  exit 1
}

echo "rust-tui-journey: ok"
