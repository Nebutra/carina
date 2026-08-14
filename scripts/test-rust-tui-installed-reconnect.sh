#!/usr/bin/env bash
# Installed-prefix reconnect + ScreenMode journey.
# Mirrors `make install` layout: public `carina` plus sibling `carina-ui`.
# Does not set CARINA_UI_BIN so the router must resolve the installed helper.
set -Eeuo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v tmux >/dev/null 2>&1 || {
  echo "installed-reconnect: tmux is required" >&2
  exit 127
}

for path in bin/carina target/debug/carina-ui; do
  [[ -x "$path" ]] || {
    echo "installed-reconnect: missing $path; build go + rust-ui first" >&2
    exit 1
  }
done

WORK="$(mktemp -d "${CARINA_E2E_TMPDIR:-/tmp}/carina-installed-reconnect.XXXXXX")"
HOME_DIR="$WORK/home"
WORKSPACE="$WORK/workspace"
PREFIX="$WORK/prefix"
SOCKET="$WORK/daemon.sock"
RECONNECT_CONTINUE="$WORK/reconnect-continue"
RECONNECT_CONTROL_CONTINUE="$WORK/reconnect-control-continue"
SCREEN_MODE_LIVE="$WORK/screen-mode-live"
SESSION=""
SCREEN=""
FAKE_DAEMON_PID=""

cleanup() {
  if [[ -n "$FAKE_DAEMON_PID" ]]; then
    kill "$FAKE_DAEMON_PID" >/dev/null 2>&1 || true
  fi
  if [[ -n "$SESSION" ]]; then
    TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK" >/dev/null 2>&1 || true
}
trap cleanup EXIT

mkdir -p "$HOME_DIR" "$WORKSPACE" "$PREFIX/bin"
install -m 755 bin/carina "$PREFIX/bin/carina"
install -m 755 target/debug/carina-ui "$PREFIX/bin/carina-ui"
if [[ -x bin/carina-daemon ]]; then
  install -m 755 bin/carina-daemon "$PREFIX/bin/carina-daemon"
fi

capture() {
  SCREEN="$(TMUX_TMPDIR="$WORK" tmux capture-pane -p -t "$SESSION" 2>/dev/null || true)"
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
  echo "installed-reconnect: timed out waiting for $wanted" >&2
  return 1
}

assert_installed_ui() {
  local pane
  pane="$(TMUX_TMPDIR="$WORK" tmux list-panes -t "$SESSION" -F '#{pane_pid}' 2>/dev/null || true)"
  [[ -n "$pane" ]] || {
    echo "installed-reconnect: no tmux pane pid" >&2
    return 1
  }
  if ! pgrep -P "$pane" -lf carina-ui 2>/dev/null | grep -Fq "$PREFIX/bin/carina-ui"; then
    # After ScreenMode exec the child may replace the process; accept any
    # descendant whose argv still points at the installed helper.
    if ! pgrep -lf "$PREFIX/bin/carina-ui" >/dev/null 2>&1; then
      echo "installed-reconnect: router did not launch sibling $PREFIX/bin/carina-ui" >&2
      return 1
    fi
  fi
}

python3 - "$SOCKET" "$WORKSPACE" "$RECONNECT_CONTINUE" "$RECONNECT_CONTROL_CONTINUE" "$SCREEN_MODE_LIVE" <<'PY' &
import json
import os
import socket
import sys
import threading
import time

socket_path, workspace_path, reconnect_continue, reconnect_control, screen_live = sys.argv[1:]
reconnect_stream_calls = [0]
reconnect_control_ready = threading.Event()


def send(stream, value):
    try:
        stream.write((json.dumps(value) + "\n").encode())
        stream.flush()
        return True
    except (BrokenPipeError, ConnectionResetError, OSError):
        return False


def inventory():
    return {
        "default_model": "test/model",
        "reasoner": {"backend": "model-router", "available": True},
        "providers": [{
            "id": "test", "name": "Test", "registered": True, "available": True,
            "auth_source": "credential_store",
            "models": [{"id": "test/model", "display_id": "gpt-5.5", "name": "Test Model",
                        "available": True, "reasoning": True, "default_reasoning_effort": "high"}],
        }],
        "readiness": {
            "step": "conversation", "blockers": [], "can_submit": True,
            "generation": 1, "epoch": "installed-reconnect",
        },
    }


def session_row(session_id, run_id, status):
    return {
        "session_id": session_id, "workspace_root": workspace_path, "status": "active",
        "next_model": "test/model", "next_reasoning_effort": "high",
        "latest_run_id": run_id, "execution_status": status,
    }


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
            params = request.get("params") or {}
            session_id = params.get("session_id")

            if method == "session.events.stream":
                send(stream, {"jsonrpc": "2.0", "id": request_id, "result": {"cursor": 0}})
                if session_id == "sess_reconnect":
                    reconnect_stream_calls[0] += 1
                    if reconnect_stream_calls[0] == 1:
                        reconnect_control_ready.clear()
                        send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                            "type": "ExecutionStarted", "event_id": "evt_reconnect_started",
                            "session_id": session_id, "run_id": "run_reconnect", "raw_cursor": 1,
                            "payload": {"status": "running"},
                        }})
                        return
                    if not reconnect_control_ready.is_set() or params.get("since") != 1:
                        return
                    for _ in range(150):
                        if os.path.exists(reconnect_continue):
                            break
                        time.sleep(0.1)
                    else:
                        return
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_reconnect_completed",
                        "session_id": session_id, "run_id": "run_reconnect", "raw_cursor": 2,
                        "payload": {"summary": "INSTALLED-RECONNECT-ANSWER"},
                    }})
                    continue
                if session_id == "sess_screen_mode":
                    for _ in range(300):
                        if os.path.exists(screen_live):
                            break
                        time.sleep(0.1)
                    else:
                        return
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ModelResponded", "event_id": "evt_screen_live",
                        "session_id": session_id, "run_id": "run_screen_live", "raw_cursor": 3,
                        "payload": {"text": "INSTALLED-SCREEN-LIVE"},
                    }})
                    continue
                continue

            if method == "runtime.initialize":
                if reconnect_stream_calls[0] >= 1 and not os.path.exists(reconnect_control):
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32000, "message": "runtime restarting",
                    }})
                    continue
                if os.path.exists(reconnect_control):
                    reconnect_control_ready.set()
                result = {
                    "runtime_version": "test",
                    "protocol_version": "1.3.0",
                    "projection_version": "1.0.0",
                    "capabilities": {"rpc_methods": [
                        "execution.start", "execution.retry", "model.list", "session.create",
                        "session.list", "session.resume", "session.items", "session.events.stream",
                    ]},
                }
            elif method == "model.list":
                result = inventory()
            elif method == "session.list":
                result = [
                    session_row("sess_reconnect", "run_reconnect", "running"),
                    session_row("sess_screen_mode", "run_screen", "completed"),
                ]
            elif method == "session.resume":
                if session_id == "sess_reconnect":
                    result = session_row("sess_reconnect", "run_reconnect", "running")
                else:
                    result = session_row("sess_screen_mode", "run_screen", "completed")
            elif method == "session.items":
                if session_id == "sess_reconnect":
                    result = [{
                        "type": "item.recorded", "session_id": session_id,
                        "run_id": "run_reconnect", "item_id": "user_reconnect",
                        "item": {"id": "user_reconnect", "type": "user", "status": "completed",
                                 "run_id": "run_reconnect",
                                 "details": {"prompt": "installed reconnect prompt"}},
                    }]
                else:
                    result = [
                        {"type": "item.recorded", "session_id": session_id,
                         "run_id": "run_screen", "item_id": "user_screen",
                         "item": {"id": "user_screen", "type": "user", "status": "completed",
                                  "run_id": "run_screen",
                                  "details": {"prompt": "INSTALLED-SCREEN-USER"}}},
                        {"type": "item.started", "session_id": session_id,
                         "run_id": "run_screen", "item_id": "call-screen-run",
                         "item": {"id": "call-screen-run", "type": "tool_call",
                                  "status": "requested", "run_id": "run_screen",
                                  "details": {"tool": "run", "arguments": {"executable": "rg"}}}},
                        {"type": "item.updated", "session_id": session_id,
                         "run_id": "run_screen", "item_id": "call-screen-run",
                         "item": {"id": "call-screen-run", "type": "tool_call",
                                  "status": "running", "run_id": "run_screen",
                                  "details": {"tool": "run", "command": "rg INSTALLED-SCREEN",
                                              "aggregated_output": "INSTALLED-SCREEN-TOOL"}}},
                        {"type": "item.completed", "session_id": session_id,
                         "run_id": "run_screen", "item_id": "call-screen-run",
                         "item": {"id": "call-screen-run", "type": "tool_call",
                                  "status": "completed", "run_id": "run_screen",
                                  "details": {"tool": "run"}}},
                        {"type": "item.recorded", "session_id": session_id,
                         "run_id": "run_screen", "item_id": "assistant_screen",
                         "item": {"id": "assistant_screen", "type": "assistant",
                                  "status": "completed", "run_id": "run_screen",
                                  "details": {"content": "INSTALLED-SCREEN-ANCHOR"}}},
                    ]
            elif method == "command.list":
                result = {"revision": "sha256:installed", "commands": []}
            elif method == "history.recent":
                result = {"entries": [], "count": 0, "scope": "workspace"}
            elif method == "daemon.status":
                result = {"queued_executions": 0, "active_workers": 0, "uptime_seconds": 1}
            elif method == "context.summary":
                result = {
                    "session_id": session_id or "",
                    "model_context_tokens": {
                        "available": True, "tokens": 1, "limit_tokens": 100000,
                        "remaining_tokens": 99999, "used_percent": 1, "threshold": "normal",
                    },
                }
            else:
                send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                    "code": -32601, "message": method,
                }})
                continue
            send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})


def serve():
    if os.path.exists(socket_path):
        os.unlink(socket_path)
    server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    server.bind(socket_path)
    server.listen()
    while True:
        connection, _ = server.accept()
        threading.Thread(target=handle, args=(connection,), daemon=True).start()


serve()
PY
FAKE_DAEMON_PID="$!"
for _ in $(seq 1 100); do
  [[ -S "$SOCKET" ]] && break
  sleep 0.05
done
[[ -S "$SOCKET" ]] || {
  echo "installed-reconnect: fake daemon did not start" >&2
  exit 1
}

launch_carina() {
  local session_id="$1"
  local extra=("${@:2}")
  SESSION="carina-installed-${session_id}-$$"
  TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 36 \
    "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$PREFIX/bin:/usr/bin:/bin' TERM=xterm-256color CARINA_RUNTIME_MODE=legacy '$PREFIX/bin/carina' --socket '$SOCKET' --workspace '$WORKSPACE' --session '$session_id' --locale en ${extra[*]}; sleep 300"
}

launch_carina sess_reconnect --no-alt-screen
wait_for_text "installed reconnect prompt"
assert_installed_ui
wait_for_text "Runtime unavailable"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "INSTALLED-DRAFT"
wait_for_text "INSTALLED-DRAFT"
touch "$RECONNECT_CONTROL_CONTINUE"
touch "$RECONNECT_CONTINUE"
wait_for_text "INSTALLED-RECONNECT-ANSWER"
grep -Fq "INSTALLED-DRAFT" <<<"$SCREEN" || {
  echo "installed-reconnect: draft was lost across reconnect" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

launch_carina sess_screen_mode --screen-mode fullscreen
wait_for_text "INSTALLED-SCREEN-USER"
wait_for_text "INSTALLED-SCREEN-ANCHOR"
wait_for_text "Run     rg INSTALLED-SCREEN"
assert_installed_ui
if grep -Fq "INSTALLED-SCREEN-TOOL" <<<"$SCREEN"; then
  echo "installed-reconnect: command output started expanded" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-o
for _ in $(seq 1 20); do
  capture
  grep -Fq "INSTALLED-SCREEN-TOOL" <<<"$SCREEN" && break
  sleep 0.1
done
if ! grep -Fq "INSTALLED-SCREEN-TOOL" <<<"$SCREEN"; then
  command_row="$(awk '/Run     rg INSTALLED-SCREEN/ { print NR; exit }' <<<"$SCREEN")"
  printf -v command_click '\033[<0;5;%dM\033[<0;5;%dm' "$command_row" "$command_row"
  TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$command_click"
  wait_for_text "INSTALLED-SCREEN-TOOL"
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /minimal
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "INSTALLED-SCREEN-ANCHOR"
grep -Fq "INSTALLED-SCREEN-TOOL" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "installed-reconnect: Minimal handoff lost disclosure" >&2
  exit 1
}
assert_installed_ui
touch "$SCREEN_MODE_LIVE"
live_seen=0
for _ in $(seq 1 150); do
  FULL="$(TMUX_TMPDIR="$WORK" tmux capture-pane -p -S - -t "$SESSION" 2>/dev/null || true)"
  capture
  if grep -Fq "INSTALLED-SCREEN-LIVE" <<<"$FULL$SCREEN"; then
    live_seen=1
    break
  fi
  TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" NPage
  sleep 0.1
done
[[ "$live_seen" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "installed-reconnect: live stream did not continue after ScreenMode handoff" >&2
  exit 1
}

echo "installed-reconnect: ok"
