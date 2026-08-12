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

STALE_RUST_SOURCE="$(
  find \
    crates/carina-tui \
    crates/xai-ratatui-inline \
    crates/xai-ratatui-textarea \
    Cargo.toml \
    Cargo.lock \
    -newer target/debug/carina-ui \
    -print \
    -quit
)"
[[ -z "$STALE_RUST_SOURCE" ]] || {
  echo "rust-tui-journey: target/debug/carina-ui is older than $STALE_RUST_SOURCE; rebuild it before testing" >&2
  exit 1
}

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
mkdir -p "$HOME_DIR/.carina/cache"
install -m 600 scripts/testdata/provider-cache.json "$HOME_DIR/.carina/cache/models.json"
python3 - "$HOME_DIR/.carina/cache/models.json" <<'PY'
import json
import sys

path = sys.argv[1]
with open(path, encoding="utf-8") as stream:
    cache = json.load(stream)
catalog = cache["catalog"]
catalog["google"]["name"] = "Google"
early = [
    ("302ai", "302.AI"),
    ("abacus", "Abacus"),
    ("abliteration-ai", "abliteration.ai"),
    ("aiand", "ai&"),
    ("ai-router", "AI-ROUTER"),
    ("aki-io", "AKI.IO"),
    ("alibaba", "Alibaba"),
    ("alibaba-cn", "Alibaba (China)"),
    ("alibaba-coding-plan", "Alibaba Coding Plan"),
    ("alibaba-coding-plan-cn", "Alibaba Coding Plan (China)"),
    ("alibaba-token-plan", "Alibaba Token Plan"),
    ("alibaba-token-plan-cn", "Alibaba Token Plan (China)"),
    ("ambient", "Ambient"),
    ("anyapi", "AnyAPI"),
    ("atomic-chat", "Atomic Chat"),
    ("auriko", "Auriko"),
    ("bailing", "Bailing"),
    ("baseten", "Baseten"),
    ("berget", "Berget.AI"),
    ("blueclaw", "Blue Claw"),
    ("cerebras", "Cerebras"),
    ("chutes", "Chutes"),
    ("clarifai", "Clarifai"),
]
for provider_id, name in early:
    catalog[provider_id] = {
        "id": provider_id,
        "name": name,
        "api": f"https://{provider_id}.example/v1",
        "env": [provider_id.upper().replace("-", "_") + "_API_KEY"],
        "npm": "@ai-sdk/openai-compatible",
    }
index = 1
while len(catalog) < 159:
    provider_id = f"zz-fixture-{index:03d}"
    catalog[provider_id] = {
        "id": provider_id,
        "name": f"ZZ Fixture {index:03d}",
        "api": f"https://fixture-{index:03d}.example/v1",
        "env": [f"ZZ_FIXTURE_{index:03d}_API_KEY"],
        "npm": "@ai-sdk/openai-compatible",
    }
    index += 1
with open(path, "w", encoding="utf-8") as stream:
    json.dump(cache, stream, separators=(",", ":"))
PY
WORKSPACE_REAL="$(cd "$WORKSPACE" && pwd -P)"
install -m 755 bin/carina bin/carina-daemon "$STAGE"
install -m 755 target/debug/carina-ui target/release/carina-kernel-service "$STAGE"
install -m 755 scripts/testdata/carina-import-failure.sh "$STAGE/carina-import-failure"
install -m 755 scripts/testdata/carina-runtime-stop-blocked.sh "$STAGE/carina-runtime-stop-blocked"
install -m 755 scripts/testdata/carina-provider-success.sh "$STAGE/carina-provider-success"
for name in carina-scan carina-grep carina-diff carina-run carina-pty carina-patch-native; do
  [[ -x "zig/zig-out/bin/$name" ]] || {
    echo "rust-tui-journey: missing Zig tool $name" >&2
    exit 1
  }
  install -m 755 "zig/zig-out/bin/$name" "$STAGE"
done

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

check_snapshot() {
  local path="$1"
  local content="$2"
  local label="$3"
  if [[ "${CARINA_UPDATE_SNAPSHOTS:-0}" == "1" ]]; then
    printf '%s\n' "$content" > "$path"
    return 0
  fi
  if ! diff -u "$path" <(printf '%s\n' "$content"); then
    printf '%s\n' "$content" >&2
    echo "rust-tui-journey: $label snapshot changed" >&2
    return 1
  fi
}

TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 160 -y 44 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina' --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$EXIT_FILE'; sleep 300"

wait_for_text "Choose language"
if grep -Fq "SETUP" <<<"$SCREEN" || grep -Fq "1  Language" <<<"$SCREEN" || grep -Fq "╭ Message" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: composer appeared before prerequisites" >&2
  exit 1
fi

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Connect a provider"
grep -Fq "Search all 159 providers by name or ID" <<<"$SCREEN"
grep -Fq "Connection" <<<"$SCREEN"
if grep -Fq "SETUP" <<<"$SCREEN" || grep -Fq "Choose model" <<<"$SCREEN" || grep -Fq "╭ Message" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: model/composer leaked before provider readiness" >&2
  exit 1
fi
provider_screen="$(sed \
  -e "s|$WORKSPACE_REAL|<workspace>|g" \
  -e "s|$WORKSPACE|<workspace>|g" \
  -e 's/[[:space:]]*$//' <<<"$SCREEN" | awk '
    NF == 0 { blanks += 1; next }
    blanks > 0 { print "<blank:" blanks ">"; blanks = 0 }
    { print }
    END { if (blanks > 0) print "<blank:" blanks ">" }
  ')"
check_snapshot scripts/testdata/rust-tui-provider-wide.snap "$provider_screen" "wide provider"

# Match the 244x71 geometry used by the manual product review. A full-screen
# picker must remain composed instead of turning into a raw inventory dump.
TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 244 -y 71
sleep 0.2
SCREEN="$(TMUX_TMPDIR="$WORK" tmux capture-pane -p -t "$SESSION" 2>/dev/null || true)"
provider_ultrawide_screen="$(sed \
  -e "s|$WORKSPACE_REAL|<workspace>|g" \
  -e "s|$WORKSPACE|<workspace>|g" \
  -e 's/[[:space:]]*$//' <<<"$SCREEN" | awk '
    NF == 0 { blanks += 1; next }
    blanks > 0 { print "<blank:" blanks ">"; blanks = 0 }
    { print }
    END { if (blanks > 0) print "<blank:" blanks ">" }
  ')"
check_snapshot scripts/testdata/rust-tui-provider-ultrawide.snap "$provider_ultrawide_screen" "ultrawide provider"
TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 160 -y 44
wait_for_text "Search all 159 providers by name or ID"

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /open
wait_for_text "/  open"
if grep -Fq "Anthropic" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: provider search did not filter the picker" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Anthropic"

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
for _ in $(seq 1 50); do
  [[ -s "$EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$EXIT_FILE" ]] || {
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
EMPTY_MODELS_SOCKET="$WORK/empty-models.sock"
CCSWITCH_SOCKET="$WORK/ccswitch.sock"
MODEL_JOURNEY_SOCKET="$WORK/model-journey.sock"
MODEL_PROVIDER_READY_FILE="$WORK/model-provider-ready"
PLAN_SOCKET="$WORK/plan-review.sock"
REVOCATION_SOCKET="$WORK/provider-revocation.sock"
LOCALE_CAPTURE="$WORK/run-locale"
PLAN_CAPTURE="$WORK/plan-rpc.jsonl"
HISTORY_FORK_CAPTURE="$WORK/history-fork-rpc.jsonl"
REVOCATION_SUBMIT_CAPTURE="$WORK/revocation-submit-rpc.jsonl"
MEDIA_SUBMIT_CAPTURE="$WORK/media-submit.json"
STREAM_CONTINUE="$WORK/stream-continue"
RECONNECT_CONTINUE="$WORK/reconnect-continue"
RECONNECT_CONTROL_CONTINUE="$WORK/reconnect-control-continue"
RECONNECT_CAPTURE="$WORK/reconnect-stream-rpc.jsonl"
RECONNECT_SUBMIT_CAPTURE="$WORK/reconnect-submit.json"
UNKNOWN_SUBMIT_CAPTURE="$WORK/unknown-submit.jsonl"
MEDIA_IMAGE="$WORK/media-sample.png"

assert_plan_review_local_only() {
  local checkpoint="$1"
  python3 - "$PLAN_CAPTURE" "$checkpoint" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    requests = [json.loads(line) for line in stream if line.strip()]
methods = [request.get("method") for request in requests]
if "session.approve_plan" in methods:
    raise SystemExit(f"{sys.argv[2]} unexpectedly approved the plan")
if methods.count("execution.start") != 1:
    raise SystemExit(
        f"{sys.argv[2]} unexpectedly changed execution count: {methods!r}"
    )
PY
}

python3 - "$MEDIA_IMAGE" <<'PY'
import base64
import pathlib
import sys
pathlib.Path(sys.argv[1]).write_bytes(base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
))
PY
GOV_EXIT_FILE="$WORK/governance-ui-exit"
python3 - "$GOV_SOCKET" "$EMPTY_MODELS_SOCKET" "$CCSWITCH_SOCKET" "$MODEL_JOURNEY_SOCKET" "$PLAN_SOCKET" "$REVOCATION_SOCKET" "$WORKSPACE" "$LOCALE_CAPTURE" "$PLAN_CAPTURE" "$HISTORY_FORK_CAPTURE" "$REVOCATION_SUBMIT_CAPTURE" "$MEDIA_SUBMIT_CAPTURE" "$STREAM_CONTINUE" "$RECONNECT_CONTINUE" "$RECONNECT_CONTROL_CONTINUE" "$RECONNECT_CAPTURE" "$RECONNECT_SUBMIT_CAPTURE" "$UNKNOWN_SUBMIT_CAPTURE" "$MODEL_PROVIDER_READY_FILE" <<'PY' &
import base64
import json
import os
import socket
import sys
import threading
import time

socket_path = sys.argv[1]
empty_models_socket_path = sys.argv[2]
ccswitch_socket_path = sys.argv[3]
model_journey_socket_path = sys.argv[4]
plan_socket_path = sys.argv[5]
revocation_socket_path = sys.argv[6]
workspace_path = sys.argv[7]
locale_capture_path = sys.argv[8]
plan_capture_path = sys.argv[9]
history_fork_capture_path = sys.argv[10]
revocation_submit_capture_path = sys.argv[11]
media_submit_capture_path = sys.argv[12]
stream_continue_path = sys.argv[13]
reconnect_continue_path = sys.argv[14]
reconnect_control_continue_path = sys.argv[15]
reconnect_capture_path = sys.argv[16]
reconnect_submit_capture_path = sys.argv[17]
unknown_submit_capture_path = sys.argv[18]
model_provider_ready_path = sys.argv[19]
resolved = threading.Event()
answered = threading.Event()
checkpoint_restored = threading.Event()
checkpoint_resumed = threading.Event()
paused_resumed = threading.Event()
plan_mode_enabled = threading.Event()
plan_submitted = threading.Event()
plan_approved = threading.Event()
model_list_calls = {}
model_list_lock = threading.Lock()
model_run_submitted = threading.Event()
model_session_created = threading.Event()
media_run_submitted = threading.Event()
media_upload_attempts = [0]
media_available = threading.Event()
stream_completed = threading.Event()
history_fork_attempts = {}
archived_sessions = set()
reconnect_stream_calls = [0]
reconnect_control_ready = threading.Event()
unknown_submission_attempts = [0]
unknown_submission_reconciled = threading.Event()
restart_governance_answered = threading.Event()


def session_execution_snapshot(session_id):
    if session_id == "sess_checkpoint":
        status = "running" if checkpoint_resumed.is_set() else (
            "paused" if checkpoint_restored.is_set() else "completed"
        )
        return {"latest_run_id": "run_cp", "execution_status": status}
    if session_id == "sess_paused":
        status = "running" if paused_resumed.is_set() else "paused"
        return {"latest_run_id": "run_paused", "execution_status": status}
    if session_id == "sess_history":
        return {"latest_run_id": "run_hist_2", "execution_status": "completed"}
    if session_id == "sess_reconnect":
        return {"latest_run_id": "run_reconnect", "execution_status": "running"}
    if session_id == "sess_governance_restart":
        return {
            "latest_run_id": "run_governance_restart",
            "execution_status": "waiting_approval",
        }
    return {"latest_run_id": "", "execution_status": "ready"}


def send(stream, value):
    try:
        stream.write((json.dumps(value) + "\n").encode())
        stream.flush()
        return True
    except (BrokenPipeError, ConnectionResetError, OSError):
        return False


def send_execution_started(stream, session_id, run_id, event_id, raw_cursor, agent=""):
    params = {
        "type": "ExecutionStarted",
        "event_id": event_id,
        "session_id": session_id,
        "run_id": run_id,
        "raw_cursor": raw_cursor,
        "payload": {},
    }
    if agent:
        params["agent"] = agent
    return send(stream, {"jsonrpc": "2.0", "method": "event", "params": params})


def capture_plan_request(request):
    with open(plan_capture_path, "a", encoding="utf-8") as stream:
        stream.write(json.dumps(request, separators=(",", ":")) + "\n")


def handle(connection, mode="normal"):
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
                if session_id == "sess_reconnect":
                    reconnect_stream_calls[0] += 1
                    with open(reconnect_capture_path, "a", encoding="utf-8") as stream_out:
                        stream_out.write(json.dumps(request.get("params", {}), separators=(",", ":")) + "\n")
                    if reconnect_stream_calls[0] == 1:
                        reconnect_control_ready.clear()
                        send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                            "type": "ExecutionStarted", "event_id": "evt_reconnect_started",
                            "session_id": session_id, "run_id": "run_reconnect", "raw_cursor": 1,
                            "payload": {"status": "running"}
                        }})
                        return
                    if not reconnect_control_ready.is_set():
                        return
                    if request.get("params", {}).get("since") != 1:
                        return
                    for _ in range(150):
                        if os.path.exists(reconnect_continue_path):
                            break
                        time.sleep(0.1)
                    else:
                        return
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_reconnect_completed",
                        "session_id": session_id, "run_id": "run_reconnect", "raw_cursor": 2,
                        "payload": {"summary": "RECONNECT-RECOVERED-ANSWER"}
                    }})
                    for _ in range(150):
                        if os.path.exists(reconnect_submit_capture_path):
                            break
                        time.sleep(0.1)
                    else:
                        return
                    send_execution_started(
                        stream, session_id, "run_locale", "evt_reconnect_draft_started", 3
                    )
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_reconnect_draft_completed",
                        "session_id": session_id, "run_id": "run_locale", "raw_cursor": 4,
                        "payload": {"summary": "RECONNECT-DRAFT-COMPLETED"}
                    }})
                    continue
                if session_id == "sess_unknown_submit":
                    if not unknown_submission_reconciled.wait(10):
                        return
                    send_execution_started(
                        stream, session_id, "run_unknown_submit", "evt_unknown_submit_started", 1
                    )
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_unknown_submit_completed",
                        "session_id": session_id, "run_id": "run_unknown_submit", "raw_cursor": 2,
                        "payload": {"summary": "UNKNOWN-SUBMISSION-RECONCILED"}
                    }})
                    continue
                if session_id == "sess_governance_restart":
                    if not restart_governance_answered.wait(10):
                        return
                    send_execution_started(
                        stream,
                        session_id,
                        "run_governance_restart",
                        "evt_governance_restart_resumed",
                        1,
                    )
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted",
                        "event_id": "evt_governance_restart_completed",
                        "session_id": session_id,
                        "run_id": "run_governance_restart",
                        "raw_cursor": 2,
                        "payload": {"summary": "Recovered verification completed"}
                    }})
                    continue
                if session_id == "sess_checkpoint":
                    if not checkpoint_resumed.wait(10):
                        return
                    time.sleep(0.5)
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_checkpoint_completed",
                        "session_id": "sess_checkpoint", "run_id": "run_cp", "raw_cursor": 1,
                        "payload": {"summary": "Checkpoint execution completed"}
                    }})
                    continue
                if session_id == "sess_paused":
                    if not paused_resumed.wait(10):
                        return
                    # Keep a deterministic active window so the PTY can prove the
                    # live cell before the terminal event replaces it.
                    time.sleep(2.0)
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_paused_completed",
                        "session_id": "sess_paused", "run_id": "run_paused", "raw_cursor": 1,
                        "payload": {"summary": "Returning paused execution complete"}
                    }})
                    continue
                if session_id in {"sess_history", "sess_history_branch"}:
                    continue
                if session_id == "sess_media_target":
                    continue
                if session_id == "sess_returning":
                    if mode == "normal":
                        if not media_run_submitted.wait(30):
                            return
                        send_execution_started(
                            stream, session_id, "run_media", "evt_media_started", 1
                        )
                        send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                            "type": "ExecutionCompleted", "event_id": "evt_media_completed",
                            "session_id": session_id, "run_id": "run_media", "raw_cursor": 2,
                            "payload": {"summary": "Image received"}
                        }})
                    continue
                if session_id == "sess_tools":
                    for cursor, payload in enumerate([
                        {"call_id": "call-todo-1", "tool": "TodoWrite",
                         "arguments": {"todos": [
                             {"content": "Inspect renderer", "status": "in_progress"},
                             {"content": "Run tests", "status": "pending"}
                         ]}},
                        {"call_id": "call-todo-1", "tool": "TodoWrite", "status": "completed"},
                        {"call_id": "call-todo-2", "tool": "update_plan",
                         "arguments": {"plan": [
                             {"step": "Inspect renderer", "status": "completed"},
                             {"step": "Run tests", "status": "in_progress"}
                         ]}},
                        {"call_id": "call-todo-2", "tool": "update_plan", "status": "completed"},
                    ], start=1):
                        kind = "ToolCallCompleted" if payload.get("status") == "completed" else "ToolCallRequested"
                        send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                            "type": kind, "event_id": f"evt_todo_{cursor}",
                            "session_id": session_id, "run_id": "run_tools",
                            "raw_cursor": cursor, "payload": payload
                        }})
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ToolCallRequested", "event_id": "evt_mcp_requested",
                        "session_id": session_id, "run_id": "run_tools", "raw_cursor": 5,
                        "payload": {"call_id": "call-mcp", "tool": "mcp",
                                    "arguments": {"mcp_tool": "docs.search"}}
                    }})
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ToolCallCompleted", "event_id": "evt_mcp_completed",
                        "session_id": session_id, "run_id": "run_tools", "raw_cursor": 6,
                        "payload": {"call_id": "call-mcp", "tool": "mcp",
                                    "status": "completed", "artifact_ids": ["artifact-mcp"]}
                    }})
                    continue
                if session_id == "sess_live_patch":
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ToolCallRequested", "event_id": "evt_live_patch_requested",
                        "session_id": session_id, "run_id": "run_live_patch", "raw_cursor": 10,
                        "payload": {"call_id": "call-live-patch", "tool": "patch",
                                    "status": "pending", "arguments": {"path": "src/live.rs"}}
                    }})
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "PatchProposed", "event_id": "evt_live_patch_proposed",
                        "session_id": session_id, "run_id": "run_live_patch", "raw_cursor": 11,
                        "payload": {
                            "patch_id": "patch_live_unique",
                            "affected_files": ["src/live.rs"],
                            "reason": "live lifecycle proof",
                            "diff": "--- a/src/live.rs\n+++ b/src/live.rs\n@@ -0,0 +1 @@\n+EDIT-DIFF-LIVE-UNIQUE\n"
                        }
                    }})
                    time.sleep(1.0)
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ToolCallStarted", "event_id": "evt_live_patch_started",
                        "session_id": session_id, "run_id": "run_live_patch", "raw_cursor": 12,
                        "payload": {"call_id": "call-live-patch", "tool": "patch", "status": "running"}
                    }})
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "PatchApplied", "event_id": "evt_live_patch_applied",
                        "session_id": session_id, "run_id": "run_live_patch", "raw_cursor": 13,
                        "payload": {"patch_id": "patch_live_unique"}
                    }})
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ToolCallCompleted", "event_id": "evt_live_patch_completed",
                        "session_id": session_id, "run_id": "run_live_patch", "raw_cursor": 14,
                        "payload": {"call_id": "call-live-patch", "tool": "patch",
                                    "status": "completed", "artifact_ids": ["artifact-live-patch"]}
                    }})
                    continue
                if session_id == "sess_transcript":
                    continue
                if session_id == "sess_streaming":
                    if stream_completed.is_set():
                        continue
                    send_execution_started(
                        stream, session_id, "run_stream", "evt_stream_started", 1
                    )
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "assistant.message.reset", "session_id": session_id,
                        "run_id": "run_stream", "payload": {"generation": 1, "sequence": 1, "phase": "final_answer"}
                    }})
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "assistant.message.delta", "session_id": session_id,
                        "run_id": "run_stream", "payload": {
                            "generation": 1, "sequence": 2, "phase": "final_answer",
                            "delta": "## STREAM-FIRST-PREFIX\n\nA **partial"
                        }
                    }})
                    time.sleep(1.0)
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "assistant.message.reset", "session_id": session_id,
                        "run_id": "run_stream", "payload": {"generation": 2, "sequence": 1, "phase": "final_answer"}
                    }})
                    replacement = "## STREAM-REPLACEMENT-TOP\n\n" + "\n".join(
                        f"streaming retained row {index:02d}" for index in range(1, 36)
                    ) + "\nSTREAM-REPLACEMENT-TAIL"
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "assistant.message.delta", "session_id": session_id,
                        "run_id": "run_stream", "payload": {
                            "generation": 2, "sequence": 2, "phase": "final_answer",
                            "delta": replacement
                        }
                    }})
                    deadline = time.time() + 10
                    while not os.path.exists(stream_continue_path) and time.time() < deadline:
                        time.sleep(0.05)
                    if not os.path.exists(stream_continue_path):
                        return
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "assistant.message.delta", "session_id": session_id,
                        "run_id": "run_stream", "payload": {
                            "generation": 2, "sequence": 3, "phase": "final_answer", "delta": "\nSTREAM-NEW-TAIL"
                        }
                    }})
                    time.sleep(0.5)
                    final_summary = replacement + "\nSTREAM-FINAL-ONCE"
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "assistant.message.completed", "session_id": session_id,
                        "run_id": "run_stream", "payload": {
                            "generation": 2, "sequence": 4, "phase": "final_answer", "content": final_summary
                        }
                    }})
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_stream_completed",
                        "session_id": session_id, "run_id": "run_stream", "raw_cursor": 2,
                        "payload": {"summary": final_summary}
                    }})
                    stream_completed.set()
                    continue
                if session_id == "sess_model_journey":
                    if not model_run_submitted.wait(10):
                        return
                    send_execution_started(
                        stream, session_id, "run_locale", "evt_model_journey_started", 1
                    )
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_model_journey_completed",
                        "session_id": session_id, "run_id": "run_locale", "raw_cursor": 2,
                        "payload": {"summary": "已按简体中文完成"}
                    }})
                    continue
                if session_id == "sess_plan":
                    if not plan_submitted.wait(10):
                        return
                    send_execution_started(
                        stream, session_id, "run_plan", "evt_plan_started", 1, "plan"
                    )
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_plan_completed",
                        "session_id": session_id, "run_id": "run_plan", "raw_cursor": 2,
                        "agent": "plan",
                        "payload": {"summary": "Implement provider discovery with typed readiness and recovery.", "result_kind": "plan"}
                    }})
                    if not plan_approved.wait(10):
                        return
                    # Leave enough time for the UI to project the canonical
                    # approval `task` before lifecycle events supersede it.
                    time.sleep(1.0)
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionStarted", "event_id": "evt_build_running",
                        "session_id": session_id, "run_id": "run_build", "raw_cursor": 3,
                        "agent": "build",
                        "payload": {}
                    }})
                    time.sleep(0.5)
                    send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                        "type": "ExecutionCompleted", "event_id": "evt_build_completed",
                        "session_id": session_id, "run_id": "run_build", "raw_cursor": 4,
                        "agent": "build",
                        "payload": {"summary": "Approved plan implemented"}
                    }})
                    continue
                send_execution_started(stream, "sess_1", "run_1", "evt_started", 1)
                send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                    "type": "permission.request", "event_id": "evt_request",
                    "session_id": "sess_1", "run_id": "run_1", "raw_cursor": 2,
                    "decision_id": "perm_1", "capability": "CommandExec",
                    "resource": "cargo test", "reason": "workspace policy",
                    "label": "Run verification"
                }})
                if not resolved.wait(10):
                    return
                send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                    "type": "ExecutionProgressed", "event_id": "evt_resolution",
                    "session_id": "sess_1", "run_id": "run_1", "raw_cursor": 3,
                    "payload": {"status": "approval_resolved", "decision_id": "perm_1", "granted": True}
                }})
                send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                    "type": "user.question", "event_id": "evt_question",
                    "session_id": "sess_1", "run_id": "run_1", "raw_cursor": 4,
                    "question_id": "q_1", "prompt": "How thorough should the verification be?",
                    "options": [
                        {"label": "Focused", "value": "focused", "description": "Run affected checks"},
                        {"label": "Full", "value": "full", "description": "Run the complete suite"}
                    ]
                }})
                if not answered.wait(10):
                    return
                send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                    "type": "ExecutionProgressed", "event_id": "evt_question_resolution",
                    "session_id": "sess_1", "run_id": "run_1", "raw_cursor": 5,
                    "payload": {"status": "user_question_resolved", "question_id": "q_1", "value": "full"}
                }})
                send(stream, {"jsonrpc": "2.0", "method": "event", "params": {
                    "type": "ExecutionCompleted", "event_id": "evt_completed",
                    "session_id": "sess_1", "run_id": "run_1", "raw_cursor": 6,
                    "payload": {"summary": "Verification complete"}
                }})
                continue
            if method == "runtime.initialize":
                if reconnect_stream_calls[0] >= 1:
                    if not os.path.exists(reconnect_control_continue_path):
                        send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                            "code": -32000, "message": "runtime restarting"
                        }})
                        continue
                    reconnect_control_ready.set()
                result = {
                    "runtime_version": "test",
                    "protocol_version": "1.3.0",
                    "projection_version": "1.0.0",
                    "capabilities": {"rpc_methods": [
                        "execution.start", "execution.retry", "model.list", "session.create",
                        "session.events.stream", "session.list"
                    ]}
                }
            elif method == "model.list":
                params = request.get("params", {})
                if mode == "ccswitch":
                    result = {"default_model": "", "providers": [{
						"id": "ccswitch-codex-internal-id", "name": "Relay profile",
                        "registered": True, "available": False,
                        "source_kind": "cc-switch", "source_label": "CC Switch",
						"source_app": "codex", "source_route": "managed_proxy",
						"source_auth_mode": "bearer_token", "source_action": "use_active_route",
                        "source_current": True, "source_importable": True,
                        "models": [{"id": "ccswitch-codex-internal-id/gpt-test", "display_id": "gpt-test", "name": "GPT Test", "available": False, "reasoning": True, "default_reasoning_effort": "high"}]
                    }]}
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                    continue
                if mode == "model-journey":
                    execution_ready = os.path.exists(model_provider_ready_path)
                    with model_list_lock:
                        model_list_calls[mode] = model_list_calls.get(mode, 0) + 1
                        readiness_generation = model_list_calls[mode]
                    can_submit = (
                        execution_ready
                        and params.get("session_id") == "sess_model_journey"
                        and params.get("model_id") == "test/model"
                        and params.get("locale") == "zh"
                    )
                    result = {"default_model": "test/model", "reasoner": {"backend": "model-router", "available": execution_ready}, "providers": [{
                        "id": "test", "name": "Test", "registered": execution_ready, "available": execution_ready,
                        "auth_source": "credential_store" if execution_ready else "",
                        "models": [{"id": "test/model", "display_id": "gpt-5.5", "name": "Test Model", "available": True, "reasoning": True, "reasoning_efforts": ["low", "medium", "high"], "default_reasoning_effort": "high"}]
                    }], "readiness": {
                        "step": "conversation" if can_submit else ("session" if execution_ready else "provider"),
                        "blockers": [] if can_submit else (["session_required"] if execution_ready else ["provider_unavailable"]),
                        "route_kind": "credential_source" if execution_ready else "",
                        "model_id": params.get("model_id", "test/model"),
                        "locale": params.get("locale", ""),
                        "can_submit": can_submit,
                        "epoch": "runtime-model-journey",
                        "generation": readiness_generation,
                    }}
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                    continue
                if mode == "revocation":
                    with model_list_lock:
                        model_list_calls[mode] = model_list_calls.get(mode, 0) + 1
                        execution_ready = model_list_calls[mode] != 3
                    result = {"default_model": "test/model", "reasoner": {
                        "backend": "model-router", "available": execution_ready}, "providers": [{
                        "id": "test", "name": "Test", "registered": True,
                        "available": execution_ready,
                        "models": [{"id": "test/model", "display_id": "gpt-5.5",
                                    "name": "Test Model", "available": True,
                                    "reasoning": True, "default_reasoning_effort": "high"}]
                    }], "readiness": {
                        "step": "conversation" if execution_ready else "provider",
                        "blockers": [] if execution_ready else ["provider_unavailable"],
                        "route_kind": "credential_source" if execution_ready else "",
                        "model_id": params.get("model_id", "test/model"),
                        "locale": params.get("locale", "en"),
                        "can_submit": execution_ready,
                        "epoch": "runtime-revocation",
                        "generation": model_list_calls[mode],
                    }}
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                    continue
                models = [] if mode == "empty" else [
                    {"id": "test/model", "display_id": "gpt-5.5", "name": "Test Model", "available": True, "reasoning": True, "reasoning_efforts": ["low", "medium", "high"], "default_reasoning_effort": "high", "image_input": True}
                ]
                if mode == "normal" and media_available.is_set():
                    models.append({"id": "test/text-only", "display_id": "text-only", "name": "Text-only Model", "available": True, "reasoning": True, "default_reasoning_effort": "high", "image_input": False})
                result = {"default_model": "test/model", "reasoner": {"backend": "model-router", "available": True}, "providers": [{
                    "id": "test", "name": "Test", "registered": True, "available": True,
                    "models": models
                }]}
            elif method == "command.list":
                result = {"revision": "sha256:journey", "commands": [{
                    "id": "prompt:project:dynamic-review",
                    "kind": "prompt_template",
                    "name": "dynamic-review",
                    "description": "Review a dynamic target",
                    "source": "project",
                    "hints": ["target"]
                }]}
            elif method == "session.list":
                if mode == "model-journey":
                    result = ([{
                        "session_id": "sess_model_journey", "workspace_root": workspace_path,
                        "status": "active", "next_model": "test/model",
                        "next_reasoning_effort": "high", "latest_run_id": "",
                        "execution_status": "ready", "created_at": "2026-07-29T10:00:00Z"
                    }] if model_session_created.is_set() else [])
                elif mode == "plan":
                    result = [{
                        "session_id": "sess_plan", "workspace_root": workspace_path,
                        "status": "active", "next_model": "test/model",
                        "next_reasoning_effort": "high", "plan_mode": False,
                        "latest_run_id": "", "execution_status": "ready",
                        "created_at": "2026-07-27T14:00:00Z"
                    }]
                else:
                    result = [
                    {"session_id": "sess_stale", "workspace_root": workspace_path, "status": "closed" if "sess_stale" in archived_sessions else "active", "next_model": "test/model", "next_reasoning_effort": "high", "latest_run_id": "", "execution_status": "ready", "created_at": "2026-07-27T13:00:00Z"},
                    {"session_id": "sess_returning", "name": "Primary draft" if media_available.is_set() else "", "workspace_root": workspace_path, "status": "active", "next_model": "test/model", "next_reasoning_effort": "high", "latest_run_id": "", "execution_status": "ready", "created_at": "2026-07-27T12:00:00Z"},
                    {"session_id": "sess_checkpoint", "workspace_root": workspace_path, "status": "active", "next_model": "test/model", **session_execution_snapshot("sess_checkpoint")},
                    {"session_id": "sess_paused", "workspace_root": workspace_path, "status": "active", "next_model": "test/model", **session_execution_snapshot("sess_paused")},
                    {"session_id": "sess_history", "workspace_root": workspace_path, "status": "active", "next_model": "test/model", **session_execution_snapshot("sess_history")},
                    {"session_id": "sess_reconnect", "workspace_root": workspace_path, "status": "active", "next_model": "test/model", **session_execution_snapshot("sess_reconnect")}
                    ,{"session_id": "sess_unknown_submit", "workspace_root": workspace_path, "status": "active", "next_model": "test/model", "latest_run_id": "", "execution_status": "ready"}
                    ,{"session_id": "sess_governance_restart", "workspace_root": workspace_path, "status": "active", "next_model": "test/model", **session_execution_snapshot("sess_governance_restart")}
                ]
                    if media_available.is_set():
                        result.append({"session_id": "sess_media_target", "name": "Media target", "workspace_root": workspace_path, "status": "active", "next_model": "test/model", "next_reasoning_effort": "high", "latest_run_id": "", "execution_status": "ready", "created_at": "2026-07-27T11:00:00Z"})
            elif method == "session.create":
                if mode == "model-journey":
                    model_session_created.set()
                result = {"session_id": "sess_model_journey", "workspace_root": workspace_path, "status": "active", "next_model": ""}
            elif method == "session.resume":
                session_id = request.get("params", {}).get("session_id", "sess_1")
                if session_id in ("sess_stale", "sess_missing"):
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32602, "message": "conversation unavailable"}})
                    continue
                result = {
                    "session_id": session_id,
                    "workspace_root": workspace_path,
                    "status": "active", "next_model": "test/model",
                    "next_reasoning_effort": "high",
                    "plan_mode": plan_mode_enabled.is_set() if session_id == "sess_plan" else False,
                    **session_execution_snapshot(session_id),
                }
            elif method == "session.rename":
                params = request.get("params", {})
                if params.get("session_id") != "sess_stale" or params.get("name") != "Release cleanup":
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32602, "message": "unexpected rename target"}})
                    continue
                result = {
                    "session_id": "sess_stale", "name": "Release cleanup",
                    "workspace_root": workspace_path, "status": "active",
                    "next_model": "test/model", "next_reasoning_effort": "high",
                    "latest_run_id": "", "execution_status": "ready"
                }
            elif method == "session.archive":
                session_id = request.get("params", {}).get("session_id")
                if session_id != "sess_stale":
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32602, "message": "unexpected archive target"}})
                    continue
                archived_sessions.add(session_id)
                result = {
                    "session_id": session_id, "name": "Release cleanup",
                    "workspace_root": workspace_path, "status": "closed",
                    "next_model": "test/model", "execution_status": "ready"
                }
            elif method == "session.unarchive":
                session_id = request.get("params", {}).get("session_id")
                if session_id != "sess_stale" or session_id not in archived_sessions:
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32602, "message": "unexpected unarchive target"}})
                    continue
                archived_sessions.remove(session_id)
                result = {
                    "session_id": session_id, "name": "Release cleanup",
                    "workspace_root": workspace_path, "status": "active",
                    "next_model": "test/model", "execution_status": "ready"
                }
            elif method == "session.model.set":
                if mode == "model-journey":
                    model_session_created.set()
                result = {
                    "session_id": request.get("params", {}).get("session_id", ""),
                    "next_model": request.get("params", {}).get("model", ""),
                    "next_reasoning_effort": "",
                }
            elif method == "session.items":
                session_id = request.get("params", {}).get("session_id")
                if session_id == "sess_1":
                    result = [{
                        "type": "item.completed", "session_id": session_id,
                        "turn_id": "run_old", "run_id": "run_old",
                        "item_id": "q_old", "item": {
                            "id": "q_old", "type": "question", "status": "resolved",
                            "run_id": "run_old", "details": {
                                "status": "user_question_resolved",
                                "question_id": "q_old", "value": "北京",
                                "timed_out": False}}
                    }]
                elif session_id == "sess_history":
                    result = [
                        {"type": "item.recorded", "session_id": session_id, "turn_id": "turn_1", "run_id": "run_hist_1", "item_id": "user_1", "item": {"id": "user_1", "type": "user", "status": "completed", "run_id": "run_hist_1", "details": {"prompt": "first prompt"}}},
                        {"type": "item.recorded", "session_id": session_id, "turn_id": "turn_2", "run_id": "run_hist_2", "item_id": "user_2", "item": {"id": "user_2", "type": "user", "status": "completed", "run_id": "run_hist_2", "details": {"prompt": "second prompt"}}}
                    ]
                elif session_id == "sess_reconnect":
                    result = [{
                        "type": "item.recorded", "session_id": session_id,
                        "turn_id": "turn_reconnect", "run_id": "run_reconnect",
                        "item_id": "user_reconnect", "item": {
                            "id": "user_reconnect", "type": "user", "status": "completed",
                            "run_id": "run_reconnect", "details": {"prompt": "reconnect source prompt"}}
                    }]
                elif session_id == "sess_governance_restart":
                    result = [
                        {
                            "type": "item.started", "session_id": session_id,
                            "turn_id": "turn_governance_restart",
                            "run_id": "run_governance_restart",
                            "item_id": "perm_restart", "item": {
                                "id": "perm_restart", "type": "approval",
                                "status": "requested", "run_id": "run_governance_restart",
                                "details": {
                                    "decision_id": "perm_restart",
                                    "capability": "CommandExec",
                                    "resource": "cargo test --workspace",
                                    "reason": "workspace policy",
                                    "label": "Resume verification after restart"
                                }
                            }
                        },
                        {
                            "type": "item.started", "session_id": session_id,
                            "turn_id": "turn_governance_restart",
                            "run_id": "run_governance_restart",
                            "item_id": "q_restart", "item": {
                                "id": "q_restart", "type": "question",
                                "status": "requested", "run_id": "run_governance_restart",
                                "details": {
                                    "question_id": "q_restart",
                                    "prompt": "Which recovered verification scope?",
                                    "options": [
                                        {"label": "Focused", "value": "focused", "description": "Affected checks"},
                                        {"label": "Full", "value": "full", "description": "Complete suite"}
                                    ]
                                }
                            }
                        }
                    ]
                elif session_id == "sess_transcript":
                    result = [{
                        "type": "item.recorded", "session_id": session_id,
                        "turn_id": "turn_transcript", "run_id": "run_transcript",
                        "item_id": "assistant_transcript",
                        "item": {
                            "id": "assistant_transcript", "type": "assistant",
                            "status": "completed", "run_id": "run_transcript",
                            "details": {"content": "TRANSCRIPT-FIRST-LINE\n" + "\n".join(
                                f"complete assistant row {index:02d}" for index in range(1, 31)
                            ) + "\nTRANSCRIPT-FINAL-LINE"}
                        }
                    }]
                elif session_id == "sess_streaming":
                    if stream_completed.is_set():
                        durable_summary = "## STREAM-REPLACEMENT-TOP\n\n" + "\n".join(
                            f"streaming retained row {index:02d}" for index in range(1, 36)
                        ) + "\nSTREAM-REPLACEMENT-TAIL\nSTREAM-FINAL-ONCE"
                        result = [{
                            "type": "turn.completed", "session_id": session_id,
                            "turn_id": "run_stream", "run_id": "run_stream",
                            "item_id": "", "source_event_id": "evt_stream_completed",
                            "details": {"summary": durable_summary}
                        }]
                    else:
                        result = []
                elif session_id == "sess_tools":
                    result = [
                        {"type": "runtime.stage_changed", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-read", "details": {
                            "call_id": "call-read", "tool": "read",
                            "stage": "tool.requested", "status": "running"}},
                        {"type": "item.started", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-read", "item": {
                            "id": "call-read", "type": "tool_call", "status": "requested",
                            "run_id": "run_tools", "details": {
                                "tool": "read", "arguments": {"path": "src/snake.cpp"}}}},
                        {"type": "item.updated", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-read", "item": {
                            "id": "call-read", "type": "tool_call", "status": "running",
                            "run_id": "run_tools", "details": {"tool": "read"}}},
                        {"type": "item.completed", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-read", "item": {
                            "id": "call-read", "type": "tool_call", "status": "completed",
                            "run_id": "run_tools", "details": {
                                "tool": "read", "output": {"bytes": 128, "redacted": True}}}},
                        {"type": "item.completed", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "model-action", "item": {
                            "id": "model-action", "type": "agent_message", "status": "completed",
                            "run_id": "run_tools", "details": {
                                "text": "{\"tool\":\"read\",\"path\":\"src/snake.cpp\"}"}}},
                        {"type": "item.started", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-command", "item": {
                            "id": "call-command", "type": "tool_call", "status": "requested",
                            "run_id": "run_tools", "details": {
                                "tool": "run", "arguments": {"executable": "cmake", "argc": 2}}}},
                        {"type": "item.updated", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-command", "item": {
                            "id": "call-command", "type": "tool_call", "status": "running",
                            "run_id": "run_tools", "details": {
                                "tool": "run", "command": "cmake --build build",
                                "aggregated_output": "COMMAND-OUTPUT-UNIQUE"}}},
                        {"type": "item.completed", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-command", "item": {
                            "id": "call-command", "type": "tool_call", "status": "completed",
                            "run_id": "run_tools", "details": {"tool": "run"}}},
                        {"type": "item.started", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-edit", "item": {
                            "id": "call-edit", "type": "tool_call", "status": "requested",
                            "run_id": "run_tools", "details": {
                                "tool": "patch", "arguments": {"path": "src/snake.cpp"}}}},
                        {"type": "item.updated", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "patch-edit", "item": {
                            "id": "patch-edit", "type": "file_change", "status": "proposed",
                            "run_id": "run_tools", "details": {
                                "patch_id": "patch-edit", "affected_files": ["src/snake.cpp"],
                                "diff": "--- a/src/snake.cpp\n+++ b/src/snake.cpp\n@@ -1 +1 @@\n-old\n+EDIT-DIFF-UNIQUE"}}},
                        {"type": "item.completed", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-edit", "item": {
                            "id": "call-edit", "type": "tool_call", "status": "completed",
                            "run_id": "run_tools", "details": {"tool": "patch"}}},
                        {"type": "item.started", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-edit-failed", "item": {
                            "id": "call-edit-failed", "type": "tool_call", "status": "requested",
                            "run_id": "run_tools", "details": {
                                "tool": "patch", "arguments": {"path": "src/broken.cpp"}}}},
                        {"type": "item.completed", "session_id": session_id,
                         "turn_id": "run_tools", "run_id": "run_tools",
                         "item_id": "call-edit-failed", "item": {
                            "id": "call-edit-failed", "type": "tool_call", "status": "failed",
                            "run_id": "run_tools", "details": {
                                "tool": "patch", "error": {"message": "EDIT-FAILURE-UNIQUE"}}}}
                    ]
                elif session_id == "sess_live_patch":
                    result = []
                elif session_id == "sess_returning":
                    result = [
                        {"type": "item.recorded", "session_id": session_id, "item_id": "session_start", "item": {"id": "session_start", "type": "session.started", "status": "recorded", "run_id": "", "details": {}}}
                    ]
                else:
                    result = []
            elif method == "history.recent":
                params = request.get("params", {})
                if params.get("scope") != "workspace" or not params.get("session_id"):
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32602, "message": "workspace history scope required"}})
                    continue
                if mode == "revocation":
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32603, "message": "synthetic history storage failure"}})
                    continue
                result = {
                    "entries": ["PERSISTED-HISTORY-OLDER", "PERSISTED-HISTORY-LATEST"],
                    "count": 2,
                    "scope": "workspace"
                }
            elif method == "workspace.tree":
                params = request.get("params", {})
                if params.get("session_id") != "sess_returning":
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32602, "message": "workspace tree must use the active session"}})
                    continue
                result = [
                    {"path": "src/app/render.rs", "size": 1200, "binary": False,
                     "large": False, "language": "rust", "mtime": 1},
                    {"path": "src/runtime.rs", "size": 800, "binary": False,
                     "large": False, "language": "rust", "mtime": 1},
                    {"path": "docs/rendering.md", "size": 400, "binary": False,
                     "large": False, "language": "markdown", "mtime": 1}
                ]
            elif method == "workspace.file.get":
                params = request.get("params", {})
                if params != {"session_id": "sess_returning", "path": "src/app/render.rs"}:
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32602, "message": "file preview must use active session and relative path"}})
                    continue
                result = {
                    "content": "FILE-PREVIEW-ALPHA\nFILE-PREVIEW-BETA\nFILE-PREVIEW-GAMMA\n",
                    "hash": "0123456789abcdef"
                }
            elif method == "session.fork":
                params = request.get("params", {})
                client_fork_id = params.get("client_fork_id", "")
                with open(history_fork_capture_path, "a", encoding="utf-8") as stream_out:
                    stream_out.write(json.dumps(params, separators=(",", ":")) + "\n")
                history_fork_attempts[client_fork_id] = history_fork_attempts.get(client_fork_id, 0) + 1
                if history_fork_attempts[client_fork_id] == 1:
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32603, "message": "synthetic fork response failure"}})
                    continue
                result = {"session_id": "sess_history_branch", "workspace_root": workspace_path, "status": "active", "next_model": "test/model", "next_reasoning_effort": "high"}
            elif method == "artifact.read":
                params = request.get("params", {})
                artifact_scope = (
                    params.get("session_id"), params.get("run_id"),
                    params.get("call_id"), params.get("artifact_id"))
                if artifact_scope == ("sess_tools", "run_tools", "call-mcp", "artifact-mcp"):
                    content = b"ARTIFACT-OUTPUT-UNIQUE"
                elif artifact_scope == ("sess_live_patch", "run_live_patch", "call-live-patch", "artifact-live-patch"):
                    content = b"PATCH-RECEIPT-LIVE-UNIQUE"
                else:
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32602, "message": "artifact scope mismatch"}})
                    continue
                result = {
                    "metadata": {"media_type": "text/plain"},
                    "offset": 0, "next_offset": len(content), "eof": True,
                    "content_base64": base64.b64encode(content).decode("ascii")
                }
            elif method == "artifact.upload":
                params = request.get("params", {})
                content = base64.b64decode(params.get("content_base64", ""))
                if params.get("session_id") not in ("sess_returning", "sess_media_target") or not content.startswith(b"\x89PNG"):
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32602, "message": "invalid media upload"}})
                    continue
                media_upload_attempts[0] += 1
                with open(media_submit_capture_path + ".uploads", "a", encoding="utf-8") as stream_out:
                    stream_out.write(json.dumps(params, separators=(",", ":")) + "\n")
                if media_upload_attempts[0] == 2:
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {
                        "code": -32603, "message": "synthetic media transport failure"}})
                    continue
                media_available.set()
                result = {
                    "artifact_id": params.get("sha256"),
                    "media_type": "image/png",
                    "bytes": params.get("total_bytes"),
                    "origin": params.get("origin", "")
                }
            elif method == "execution.start":
                params = request.get("params", {})
                if params.get("session_id") == "sess_unknown_submit":
                    unknown_submission_attempts[0] += 1
                    with open(unknown_submit_capture_path, "a", encoding="utf-8") as stream_out:
                        stream_out.write(json.dumps(params, separators=(",", ":")) + "\n")
                    if unknown_submission_attempts[0] == 1:
                        return
                    result = {
                        "run_id": "run_unknown_submit", "session_id": "sess_unknown_submit",
                        "status": "queued", "user_prompt": params.get("prompt", "")
                    }
                    unknown_submission_reconciled.set()
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                    continue
                if params.get("session_id") == "sess_reconnect":
                    with open(reconnect_submit_capture_path, "w", encoding="utf-8") as stream_out:
                        json.dump(params, stream_out, separators=(",", ":"))
                if params.get("session_id") == "sess_returning" and params.get("input_media_refs"):
                    with open(media_submit_capture_path, "w", encoding="utf-8") as stream_out:
                        json.dump(params, stream_out, separators=(",", ":"))
                    media_run_submitted.set()
                if mode == "revocation":
                    with open(revocation_submit_capture_path, "a", encoding="utf-8") as stream_out:
                        stream_out.write(json.dumps(params, separators=(",", ":")) + "\n")
                if mode == "plan":
                    capture_plan_request(request)
                    if not plan_mode_enabled.is_set():
                        send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32602, "message": "plan mode was not enabled before submit"}})
                        continue
                    if params.get("session_id") != "sess_plan" or params.get("agent") != "plan":
                        send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32602, "message": "plan submit must target sess_plan with agent=plan"}})
                        continue
                    result = {
                        "run_id": "run_plan", "session_id": "sess_plan",
                        "status": "queued", "user_prompt": params.get("prompt", "")
                    }
                    plan_submitted.set()
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                    continue
                with open(locale_capture_path, "w", encoding="utf-8") as stream_out:
                    stream_out.write(params.get("locale", ""))
                result = {
                    "run_id": "run_media" if params.get("session_id") == "sess_returning" else "run_locale", "session_id": params.get("session_id", ""),
                    "status": "queued", "user_prompt": params.get("prompt", "")
                }
                model_run_submitted.set()
            elif method == "session.plan_mode" and mode == "plan":
                capture_plan_request(request)
                params = request.get("params", {})
                if params.get("session_id") != "sess_plan" or params.get("on") is not True:
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32602, "message": "expected plan mode enable for sess_plan"}})
                    continue
                plan_mode_enabled.set()
                result = {"session_id": "sess_plan", "plan_mode": True}
            elif method == "session.approve_plan" and mode == "plan":
                capture_plan_request(request)
                params = request.get("params", {})
                if params.get("session_id") != "sess_plan" or not plan_submitted.is_set():
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32602, "message": "plan approval arrived before a completed plan"}})
                    continue
                if params.get("run_id") != "run_plan":
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32602, "message": "plan approval did not preserve the reviewed run identity"}})
                    continue
                result = {
                    "session_id": "sess_plan", "plan_mode": False, "approved": True,
                    "task": {
                        "run_id": "run_build", "session_id": "sess_plan", "agent": "build",
                        "status": "queued", "user_prompt": "Implement this approved plan"
                    }
                }
                send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                plan_mode_enabled.clear()
                plan_approved.set()
                continue
            elif method == "session.checkpoint.list":
                result = [
                    {"checkpoint_id": "run_cp:1", "parent_checkpoint_id": "", "created_at": "2026-07-27T10:00:00Z", "sequence": "00000000000000000001", "run_id": "run_cp", "session_id": "sess_checkpoint", "turn": 1, "summary": "before setup", "applied_patches": []},
                    {"checkpoint_id": "run_cp:2", "parent_checkpoint_id": "run_cp:1", "created_at": "2026-07-27T11:00:00Z", "sequence": "00000000000000000002", "run_id": "run_cp", "session_id": "sess_checkpoint", "turn": 2, "summary": "before refactor", "applied_patches": ["patch_1"]}
                ]
            elif method == "daemon.status":
                result = {"queued_executions": 2, "active_workers": 1, "uptime_seconds": 42, "context_engine": {"effective_engine": "native", "phase": "ready"}}
            elif method == "agent.view":
                result = {"needs_input": [], "working": [{"task_id": "internal-agent-task", "title": "Index workspace", "status": "running"}], "completed": []}
            elif method == "usage.cost":
                result = {"totals": {"input_tokens": 120, "output_tokens": 30, "cache_read_tokens": 50, "cost_usd": 0.0123}}
            elif method == "context.summary":
                result = {"session_id": request.get("params", {}).get("session_id", ""), "model_context_tokens": {"available": True, "estimated": False, "tokens": 12000, "limit_tokens": 100000, "remaining_tokens": 88000, "used_percent": 12, "threshold": "normal", "measurement": "latest completed provider request", "breakdown": {"input_tokens": 12000, "output_tokens": 1400}}}
            elif method == "session.review":
                result = {"state": "ready", "changes": [{"id": "change-1"}], "checks": [{"id": "check-1"}], "diagnostics": [], "artifact_ids": ["internal-artifact-id"], "rollback": {"available": True, "patch_ids": ["internal-patch-id"]}}
            elif method == "workspace.diff":
                result = {"files": [], "truncated": False, "total_bytes": 0}
            elif method == "session.checkpoint.preview":
                result = {"checkpoint": {"checkpoint_id": "run_cp:2", "parent_checkpoint_id": "run_cp:1", "created_at": "2026-07-27T11:00:00Z", "sequence": "00000000000000000002", "run_id": "run_cp", "session_id": "sess_checkpoint", "turn": 2, "summary": "before refactor", "applied_patches": ["patch_1"]}, "conversation_turns": 2, "summary": "before refactor", "rollback_patches": ["patch_2"], "will_resume": "paused"}
            elif method == "session.checkpoint.restore":
                result = {"restored": True, "checkpoint_id": "run_cp:2", "run_id": "run_cp", "turn": 2, "rolled_back": ["patch_2"], "status": "paused", "idempotent": False, "reconciliation_required": False, "journal_cleanup_pending": False}
                checkpoint_restored.set()
            elif method == "execution.resume":
                run_id = request.get("params", {}).get("run_id")
                if run_id == "run_paused":
                    result = {"run_id": "run_paused", "session_id": "sess_paused", "status": "running"}
                    paused_resumed.set()
                else:
                    result = {"run_id": "run_cp", "session_id": "sess_checkpoint", "status": "running"}
                    checkpoint_resumed.set()
                send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                continue
            elif method == "governance.approval.resolve":
                decision_id = request.get("params", {}).get("decision_id", "")
                result = {"decision_id": decision_id, "resolved": True, "scope": "once"}
                send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                resolved.set()
                continue
            elif method == "question.answer":
                value = request.get("params", {}).get("value", "")
                if value != "full":
                    send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32602, "message": "unexpected answer"}})
                    continue
                question_id = request.get("params", {}).get("question_id", "")
                result = {"question_id": question_id, "accepted": True, "value": value}
                send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})
                if question_id == "q_restart":
                    restart_governance_answered.set()
                answered.set()
                continue
            else:
                send(stream, {"jsonrpc": "2.0", "id": request_id, "error": {"code": -32601, "message": method}})
                continue
            send(stream, {"jsonrpc": "2.0", "id": request_id, "result": result})


def serve(path, mode):
    if os.path.exists(path):
        os.unlink(path)
    server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    server.bind(path)
    server.listen()
    while True:
        connection, _ = server.accept()
        threading.Thread(target=handle, args=(connection, mode), daemon=True).start()


threading.Thread(target=serve, args=(socket_path, "normal"), daemon=True).start()
threading.Thread(target=serve, args=(empty_models_socket_path, "empty"), daemon=True).start()
threading.Thread(target=serve, args=(model_journey_socket_path, "model-journey"), daemon=True).start()
threading.Thread(target=serve, args=(plan_socket_path, "plan"), daemon=True).start()
threading.Thread(target=serve, args=(revocation_socket_path, "revocation"), daemon=True).start()
serve(ccswitch_socket_path, "ccswitch")
PY
FAKE_DAEMON_PID="$!"
for _ in $(seq 1 100); do
  [[ -S "$GOV_SOCKET" && -S "$EMPTY_MODELS_SOCKET" && -S "$CCSWITCH_SOCKET" && -S "$MODEL_JOURNEY_SOCKET" && -S "$PLAN_SOCKET" && -S "$REVOCATION_SOCKET" ]] && break
  sleep 0.05
done
[[ -S "$GOV_SOCKET" && -S "$EMPTY_MODELS_SOCKET" && -S "$CCSWITCH_SOCKET" && -S "$MODEL_JOURNEY_SOCKET" && -S "$PLAN_SOCKET" && -S "$REVOCATION_SOCKET" ]] || {
  echo "rust-tui-journey: fake journey daemons did not start" >&2
  exit 1
}

CCSWITCH_EXIT_FILE="$WORK/ccswitch-ui-exit"
CCSWITCH_ATTEMPTS_FILE="$WORK/ccswitch-import-attempts"
SESSION="carina-rust-tui-ccswitch-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color CCSWITCH_ATTEMPTS_FILE='$CCSWITCH_ATTEMPTS_FILE' '$STAGE/carina-ui' --socket '$CCSWITCH_SOCKET' --workspace '$WORKSPACE' --locale en --carina-bin '$STAGE/carina-import-failure' --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$CCSWITCH_EXIT_FILE'; sleep 300"

wait_for_text "Active route"
grep -Fq "Codex  ·  active via proxy" <<<"$SCREEN"
grep -Fq "Provider   Relay profile  ·  CC Switch / Codex" <<<"$SCREEN"
if grep -Fq "ccswitch-codex-internal-id" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: CC Switch source/runtime id leaked" >&2
  exit 1
fi
ccswitch_screen="$(sed \
  -e "s|$WORKSPACE_REAL|<workspace>|g" \
  -e "s|$WORKSPACE|<workspace>|g" \
  -e 's/[[:space:]]*$//' <<<"$SCREEN" | awk '
    NF == 0 { blanks += 1; next }
    blanks > 0 { print "<blank:" blanks ">"; blanks = 0 }
    { print }
    END { if (blanks > 0) print "<blank:" blanks ">" }
  ')"
check_snapshot scripts/testdata/rust-tui-ccswitch.snap "$ccswitch_screen" "CC Switch provider"
TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 80 -y 24
wait_for_text "Connection"
grep -Fq "Connection" <<<"$SCREEN"
grep -Fq "Relay profile" <<<"$SCREEN"
ccswitch_narrow_screen="$(sed \
  -e "s|$WORKSPACE_REAL|<workspace>|g" \
  -e "s|$WORKSPACE|<workspace>|g" \
  -e 's/[[:space:]]*$//' <<<"$SCREEN" | awk '
    NF == 0 { blanks += 1; next }
    blanks > 0 { print "<blank:" blanks ">"; blanks = 0 }
    { print }
    END { if (blanks > 0) print "<blank:" blanks ">" }
  ')"
check_snapshot scripts/testdata/rust-tui-ccswitch-narrow.snap "$ccswitch_narrow_screen" "narrow CC Switch provider"
TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 120 -y 40
wait_for_text "Active route"
wait_for_text "PROVIDER DETAILS"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Ready to use"
for consequence in "Uses the active Codex proxy" "proxy token" "CC Switch is" "unchanged."; do
  if ! grep -Fq "$consequence" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: CC Switch confirmation consequence is not visible: $consequence" >&2
    exit 1
  fi
done
grep -Fq "Confirm route" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Validating provider"
grep -Fq "within 20" <<<"$SCREEN"
wait_for_text "Import failed"
grep -Fq "endpoint rejects this client type" <<<"$SCREEN"
grep -Fq "Retry route" <<<"$SCREEN"
if head -n 6 <<<"$SCREEN" | grep -Fq "Import failed"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: provider failure replaced the product header" >&2
  exit 1
fi
if grep -Fq "Ready to use" <<<"$SCREEN" || grep -Fq "ccswitch-codex-internal-id" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: failed CC Switch import rendered a contradictory or unsafe state" >&2
  exit 1
fi
[[ "$(cat "$CCSWITCH_ATTEMPTS_FILE")" == "1" ]] || {
  echo "rust-tui-journey: CC Switch import did not execute exactly once" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
for _ in $(seq 1 100); do
  [[ -f "$CCSWITCH_ATTEMPTS_FILE" && "$(cat "$CCSWITCH_ATTEMPTS_FILE")" == "2" ]] && break
  sleep 0.05
done
[[ "$(cat "$CCSWITCH_ATTEMPTS_FILE")" == "2" ]] || {
  echo "rust-tui-journey: Enter did not explicitly retry the failed CC Switch import" >&2
  exit 1
}
wait_for_text "Import failed"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Active route"
if grep -Fq "Ready to use" <<<"$SCREEN"; then
  echo "rust-tui-journey: CC Switch import confirmation did not cancel" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
for _ in $(seq 1 50); do
  [[ -s "$CCSWITCH_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$CCSWITCH_EXIT_FILE" && "$(cat "$CCSWITCH_EXIT_FILE")" == "6" ]] || {
  echo "rust-tui-journey: CC Switch provider cancel did not exit degraded" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

MODEL_JOURNEY_EXIT_FILE="$WORK/model-journey-ui-exit"
SESSION="carina-rust-tui-model-journey-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color CARINA_PROVIDER_READY_FILE='$MODEL_PROVIDER_READY_FILE' '$STAGE/carina-ui' --socket '$MODEL_JOURNEY_SOCKET' --workspace '$WORKSPACE' --locale zh --carina-bin '$STAGE/carina-provider-success' --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$MODEL_JOURNEY_EXIT_FILE'; sleep 300"

wait_for_text "选择语言"
grep -Fq "简体中文" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "连接服务商"
grep -Fq "未注册" <<<"$SCREEN"
if grep -Fq "● 就绪" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: provider claimed Ready while the execution backend was unavailable" >&2
  exit 1
fi

# Locale is durable, provider readiness is not. Restart must return to the
# first unresolved provider prerequisite without replaying Language or leaking Model.
# Idle quit is deliberately double-confirmed; keep both presses inside the
# product's bounded confirmation window.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do [[ -s "$MODEL_JOURNEY_EXIT_FILE" ]] && break; sleep 0.1; done
[[ -s "$MODEL_JOURNEY_EXIT_FILE" && "$(cat "$MODEL_JOURNEY_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: locale-boundary restart did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
rm -f "$MODEL_JOURNEY_EXIT_FILE"
SESSION="carina-rust-tui-model-provider-restart-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color CARINA_PROVIDER_READY_FILE='$MODEL_PROVIDER_READY_FILE' '$STAGE/carina-ui' --socket '$MODEL_JOURNEY_SOCKET' --workspace '$WORKSPACE' --locale zh-Hans --carina-bin '$STAGE/carina-provider-success' --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$MODEL_JOURNEY_EXIT_FILE'; sleep 300"
wait_for_text "连接服务商"
for leaked in "▌ 选择语言" "▌ 选择模型" "描述你想完成的改动。"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: locale-boundary restart leaked $leaked" >&2
    exit 1
  fi
done

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "验证 test"
grep -Fq "凭证会在保存前验证" <<<"$SCREEN"
if grep -Fq "╭ 消息" <<<"$SCREEN"; then
  echo "rust-tui-journey: credential prerequisite leaked the conversation composer" >&2
  exit 1
fi

# Entering credential input does not make the provider complete. Restart must
# recompute Provider as the first unresolved durable prerequisite and must not
# retain or expose a partial secret.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "partial-secret"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do [[ -s "$MODEL_JOURNEY_EXIT_FILE" ]] && break; sleep 0.1; done
[[ -s "$MODEL_JOURNEY_EXIT_FILE" && "$(cat "$MODEL_JOURNEY_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: credential-boundary restart did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
rm -f "$MODEL_JOURNEY_EXIT_FILE"
SESSION="carina-rust-tui-credential-restart-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color CARINA_PROVIDER_READY_FILE='$MODEL_PROVIDER_READY_FILE' '$STAGE/carina-ui' --socket '$MODEL_JOURNEY_SOCKET' --workspace '$WORKSPACE' --locale zh-Hans --carina-bin '$STAGE/carina-provider-success' --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$MODEL_JOURNEY_EXIT_FILE'; sleep 300"
wait_for_text "连接服务商"
if grep -Fq "partial-secret" <<<"$SCREEN" || grep -Fq "▌ 选择模型" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: credential-boundary restart leaked secret or skipped provider" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "验证 test"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "valid-test-key"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "▌ 选择模型"
[[ -s "$MODEL_PROVIDER_READY_FILE" ]] || {
  echo "rust-tui-journey: successful credential validation was not durable" >&2
  exit 1
}
if grep -Fq "连接服务商" <<<"$SCREEN"; then
  echo "rust-tui-journey: provider screen remained after execution readiness became available" >&2
  exit 1
fi

# Credential/provider readiness is durable, but no workspace session exists
# yet. Restart must remain on explicit Model confirmation instead of silently
# creating a default-model conversation.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do [[ -s "$MODEL_JOURNEY_EXIT_FILE" ]] && break; sleep 0.1; done
[[ -s "$MODEL_JOURNEY_EXIT_FILE" && "$(cat "$MODEL_JOURNEY_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: provider-boundary restart did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
rm -f "$MODEL_JOURNEY_EXIT_FILE"
SESSION="carina-rust-tui-model-confirm-restart-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color CARINA_PROVIDER_READY_FILE='$MODEL_PROVIDER_READY_FILE' '$STAGE/carina-ui' --socket '$MODEL_JOURNEY_SOCKET' --workspace '$WORKSPACE' --locale zh-Hans --carina-bin '$STAGE/carina-provider-success' --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$MODEL_JOURNEY_EXIT_FILE'; sleep 300"
wait_for_text "▌ 选择模型"
for leaked in "▌ 选择语言" "▌ 连接服务商" "描述你想完成的改动。"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: provider-boundary restart leaked $leaked" >&2
    exit 1
  fi
done

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "描述你想完成的改动。"
for leaked in "连接服务商" "Connect a provider" "选择模型" "Choose model" "Open a conversation"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: model confirmation returned to setup state $leaked" >&2
    exit 1
  fi
done

# Model confirmation is represented by the daemon-owned workspace session.
# Restart now resumes Conversation and may not replay any setup surface.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do [[ -s "$MODEL_JOURNEY_EXIT_FILE" ]] && break; sleep 0.1; done
[[ -s "$MODEL_JOURNEY_EXIT_FILE" && "$(cat "$MODEL_JOURNEY_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: model-boundary restart did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
rm -f "$MODEL_JOURNEY_EXIT_FILE"
SESSION="carina-rust-tui-conversation-restart-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color CARINA_PROVIDER_READY_FILE='$MODEL_PROVIDER_READY_FILE' '$STAGE/carina-ui' --socket '$MODEL_JOURNEY_SOCKET' --workspace '$WORKSPACE' --locale zh-Hans --carina-bin '$STAGE/carina-provider-success' --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$MODEL_JOURNEY_EXIT_FILE'; sleep 300"
wait_for_text "描述你想完成的改动。"
for leaked in "▌ 选择语言" "▌ 连接服务商" "▌ 选择模型"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: model-boundary restart replayed $leaked" >&2
    exit 1
  fi
done

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l hi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
for _ in $(seq 1 100); do
  [[ -f "$LOCALE_CAPTURE" ]] && break
  sleep 0.05
done
[[ -f "$LOCALE_CAPTURE" && "$(cat "$LOCALE_CAPTURE")" == "zh" ]] || {
  echo "rust-tui-journey: selected Simplified Chinese did not reach execution.start" >&2
  exit 1
}
wait_for_text "已按简体中文完成"
for leaked in "run_locale" "Task queued" "active task" "任务已排队"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: foreground conversation leaked scheduler vocabulary or ID: $leaked" >&2
    exit 1
  fi
done
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$MODEL_JOURNEY_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$MODEL_JOURNEY_EXIT_FILE" && "$(cat "$MODEL_JOURNEY_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: provider-model-conversation journey did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

RETURNING_EXIT_FILE="$WORK/returning-ui-exit"
SESSION="carina-rust-tui-returning-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --locale en --screen-mode fullscreen; code=\$?; printf '%s' \"\$code\" > '$RETURNING_EXIT_FILE'; sleep 300"

wait_for_text "Describe the change you want to make."
grep -Fq "gpt-5.5  ·  high reasoning  ·  Test  ·  Direct API  ·  workspace" <<<"$SCREEN"
grep -Fq "Carina  ·  workspace conversation" <<<"$SCREEN"
for leaked in "Choose model" "Open a conversation" "sess_stale" "sess_returning" "Started" "recorded" "runtime test" "protocol 1.3.0"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: returning conversation leaked $leaked" >&2
    exit 1
  fi
done
normalized_screen="$(sed \
  -e "s|$WORKSPACE_REAL|<workspace>|g" \
  -e "s|$WORKSPACE|<workspace>|g" \
  -e 's/[[:space:]]*$//' <<<"$SCREEN" | awk '
    NF == 0 { blanks += 1; next }
    blanks > 0 { print "<blank:" blanks ">"; blanks = 0 }
    { print }
    END { if (blanks > 0) print "<blank:" blanks ">" }
  ')"
check_snapshot scripts/testdata/rust-tui-returning.snap "$normalized_screen" "returning conversation"

# @file completion consumes daemon-owned workspace.tree inventory. It renders
# an interactive popup, inserts an atomic relative-path chip, and acceptance is
# one undo step back to the original query.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "Review @src/app/rend"
wait_for_text "Workspace files"
wait_for_text "src/app/render.rs"

# ':' opens a retained line viewer without mutating the draft. A visual range
# becomes one atomic @file:N-M element; the element can be reopened in place,
# Escape preserves it, and one undo restores the original completion query.
TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 70 -y 20
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l ":"
wait_for_text "FILE-PREVIEW-ALPHA"
wait_for_text "V range  / search  Enter  Esc"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Down v Down Enter
wait_without_text "FILE-PREVIEW-ALPHA"
wait_for_text "src/app/render.rs:2-3"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Left C-l
wait_for_text "FILE-PREVIEW-BETA"
wait_for_text "Lines 2-3"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_without_text "FILE-PREVIEW-BETA"
wait_for_text "src/app/render.rs:2-3"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-z
wait_for_text "Workspace files"
wait_for_text "@src/app/rend"
TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 120 -y 40
# Resize uses a trailing debounce. Do not derive pointer coordinates from the
# previous frame while the new layout and hit map are still pending.
sleep 0.2

capture
file_row="$(awk '/src\/app\/render.rs/ { print NR; exit }' <<<"$SCREEN")"
file_col="$(python3 -c 'import sys; line=next(line for line in sys.stdin if "src/app/render.rs" in line); print(line.index("src/app/render.rs") + 2)' <<<"$SCREEN")"
printf -v file_click '\033[<0;%d;%dM\033[<0;%d;%dm' "$file_col" "$file_row" "$file_col" "$file_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$file_click"
wait_without_text "Workspace files"
wait_for_text "src/app/render.rs"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-z
wait_for_text "Workspace files"
wait_for_text "@src/app/rend"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Tab
wait_without_text "Workspace files"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" BSpace BSpace
wait_without_text "src/app/render.rs"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Up
wait_for_text "Prompt history"
wait_for_text "Up/Down browse  Enter edit  Esc restore draft"
wait_for_text "PERSISTED-HISTORY-LATEST"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Up
wait_for_text "PERSISTED-HISTORY-OLDER"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Down Down
wait_without_text "Prompt history"
wait_without_text "PERSISTED-HISTORY-LATEST"

# Browse history is a retained interactive component, not a keyboard-only log.
# One click selects an older row and a second click accepts it for editing.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Up
wait_for_text "Prompt history"
capture
history_row="$(awk '/PERSISTED-HISTORY-OLDER/ { print NR; exit }' <<<"$SCREEN")"
history_col="$(python3 -c 'import sys; line=next(line for line in sys.stdin if "PERSISTED-HISTORY-OLDER" in line); print(line.index("PERSISTED-HISTORY-OLDER") + 2)' <<<"$SCREEN")"
printf -v history_click '\033[<0;%d;%dM\033[<0;%d;%dm' "$history_col" "$history_row" "$history_col" "$history_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$history_click"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$history_click"
wait_without_text "Prompt history"
wait_for_text "PERSISTED-HISTORY-OLDER"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "draft stays editable"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Up
wait_for_text "draft stays editable"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "search draft survives cancel"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-r
wait_for_text "Prompt history"
wait_for_text "PERSISTED-HISTORY-LATEST"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "OLDER"
wait_for_text "Search  OLDER"
wait_for_text "PERSISTED-HISTORY-OLDER"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_without_text "Prompt history"
wait_for_text "search draft survives cancel"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u

TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 80 -y 24
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-r
wait_for_text "Prompt history"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "LATEST"
wait_for_text "Search  LATEST"
wait_for_text "PERSISTED-HISTORY-LATEST"
grep -Fq "Message" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_without_text "Prompt history"
wait_for_text "PERSISTED-HISTORY-LATEST"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u
TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 120 -y 40

# A pasted image path becomes one atomic composer element. Deleting the chip
# must also delete its attachment state; re-pasting and submitting sends only
# the typed MediaRef, never local bytes or the source path.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l $'\033[200~'"$MEDIA_IMAGE"$'\033[201~'
wait_for_text "media-sample.png"
wait_for_text "image"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l x
wait_for_text "media-sample.png  x"
wait_without_text "Format      image/png"
wait_for_text "media-sample.png  x"
pane_height="$(TMUX_TMPDIR="$WORK" tmux display-message -p -t "$SESSION" '#{pane_height}')"
for target_row in $(seq "$((pane_height - 7))" "$pane_height"); do
  for target_col in $(seq 2 36); do
    printf -v media_pointer '\033[<0;%d;%dM' "$target_col" "$target_row"
    TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$media_pointer"
    sleep 0.1
    capture
    grep -Fq "Format      image/png" <<<"$SCREEN" && break 2
  done
done
grep -Fq "Format      image/png" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: real SGR pointer action did not open the image preview" >&2
  exit 1
}
for _ in $(seq 1 50); do
  TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l $'\033[<0;60;20M'
  sleep 0.1
  capture
  ! grep -Fq "Format      image/png" <<<"$SCREEN" && break
done
if grep -Fq "Format      image/png" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: real SGR outside click did not close the image preview" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" BSpace BSpace BSpace
wait_without_text "media-sample.png"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l $'\033[200~'"$MEDIA_IMAGE"$'\033[201~'
wait_for_text "media-sample.png"
wait_for_text "failed  media-sample.png"
wait_for_text "Retry"
if grep -Fq "synthetic media transport failure" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: media component leaked a backend transport error" >&2
  exit 1
fi
[[ ! -e "$MEDIA_SUBMIT_CAPTURE" ]] || {
  echo "rust-tui-journey: failed media upload reached execution.start before retry" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Image attached"
wait_for_text "│ image  media-sample.png"

wait_for_text "Sessions"
sessions_col="$(python3 -c 'import sys; line=next(line for line in sys.stdin if "Sessions" in line); print(line.index("Sessions") + 2)' <<<"$SCREEN")"
printf -v sessions_move '\033[<35;%d;1M' "$sessions_col"
printf -v sessions_click '\033[<0;%d;1M' "$sessions_col"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$sessions_move"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$sessions_click"
wait_for_text "Conversations"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "/Media target"
wait_for_text "Media target"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Image attached"
wait_for_text "media-sample.png"
TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 70 -y 20
wait_for_text "media-sample.png"
TMUX_TMPDIR="$WORK" tmux resize-window -t "$SESSION" -x 120 -y 40
wait_for_text "media-sample.png"
wait_for_text "Sessions"
sessions_col="$(python3 -c 'import sys; line=next(line for line in sys.stdin if "Sessions" in line); print(line.index("Sessions") + 2)' <<<"$SCREEN")"
printf -v sessions_move '\033[<35;%d;1M' "$sessions_col"
printf -v sessions_click '\033[<0;%d;1M' "$sessions_col"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$sessions_move"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$sessions_click"
wait_for_text "Conversations"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "/"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" BSpace BSpace BSpace BSpace BSpace BSpace BSpace BSpace BSpace BSpace BSpace BSpace
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "Primary draft"
wait_for_text "Primary draft"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_without_text "Conversations"
wait_for_text "Image attached"
wait_for_text "media-sample.png"

# A ready attachment remains a conversation-owned draft when the user selects
# a text-only model. Submission must be blocked against the refreshed inventory
# without deleting the chip or sending execution.start; switching back to the
# vision model then submits the same retained draft.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /model
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Choose model"
wait_for_text "text-only"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Down Enter
wait_for_text "text-only"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "Explain this image"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "The current model does not support images"
wait_for_text "media-sample.png"
[[ ! -e "$MEDIA_SUBMIT_CAPTURE" ]] || {
  echo "rust-tui-journey: text-only model received a media execution" >&2
  exit 1
}
capture
model_col="$(python3 -c 'import sys; line=sys.stdin.readline(); print(line.index("Model") + 2)' <<<"$SCREEN")"
printf -v model_click '\033[<0;%d;1M' "$model_col"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$model_click"
wait_for_text "Choose model"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Up Enter
wait_for_text "gpt-5.5"
wait_for_text "Explain this image"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Image received"
python3 - "$MEDIA_SUBMIT_CAPTURE" "$MEDIA_IMAGE" "$MEDIA_SUBMIT_CAPTURE.uploads" <<'PY'
import json
import pathlib
import sys
capture = pathlib.Path(sys.argv[1])
if not capture.is_file():
    raise SystemExit("media execution.start was not captured")
params = json.loads(capture.read_text(encoding="utf-8"))
refs = params.get("input_media_refs")
if not isinstance(refs, list) or len(refs) != 1:
    raise SystemExit(f"expected one media ref, got {refs!r}")
if params.get("prompt") != "Explain this image":
    raise SystemExit(f"media chip leaked into prompt: {params.get('prompt')!r}")
encoded = json.dumps(params, separators=(",", ":"))
if sys.argv[2] in encoded or "content_base64" in encoded:
    raise SystemExit("execution.start leaked the local path or image bytes")
uploads = [json.loads(line) for line in pathlib.Path(sys.argv[3]).read_text(encoding="utf-8").splitlines()]
if len(uploads) != 5:
    raise SystemExit(f"expected initial, failed, retry, and two session rebind uploads, got {len(uploads)}")
failed, retried = uploads[1:3]
if failed.get("upload_id") == retried.get("upload_id"):
    raise SystemExit("media retry reused a failed transport transaction")
if failed.get("sha256") != retried.get("sha256") or failed.get("origin") != retried.get("origin"):
    raise SystemExit("media retry changed content identity")
if [upload.get("session_id") for upload in uploads[3:]] != ["sess_media_target", "sess_returning"]:
    raise SystemExit("media attachment was not rebound to each target session")
if len({upload.get("sha256") for upload in uploads}) != 1:
    raise SystemExit("session rebind changed media content identity")
PY

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "draft survives failed resume"
wait_for_text "Sessions"
sessions_col="$(python3 -c 'import sys; line=next(line for line in sys.stdin if "Sessions" in line); print(line.index("Sessions") + 2)' <<<"$SCREEN")"
printf -v sessions_click '\033[<0;%d;1M\033[<0;%d;1m' "$sessions_col" "$sessions_col"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$sessions_click"
wait_for_text "Search conversations"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Could not open the conversation"
if [[ -f "$RETURNING_EXIT_FILE" ]] || grep -Fq "sess_stale" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: failed resume exited or leaked the stale session id" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "draft survives failed resume"
if ! grep -Fq "workspace conversation" <<<"$SCREEN" && ! grep -Fq "Image received" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: failed resume replaced the source conversation" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /settings
wait_for_text "/settings"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Language  ·  en"
grep -Fq "Provider  ·  Test" <<<"$SCREEN"
for leaked in "run_" "Task queued" "active task" "Task failed"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: settings leaked foreground execution internals: $leaked" >&2
    exit 1
  fi
done
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Down Enter
wait_for_text "Connect a provider"
grep -Fq "Connection" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Choose model"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Image received"
if grep -Fq "Connect a provider" <<<"$SCREEN" || grep -Fq "Choose model" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: settings provider/model selection did not return to conversation" >&2
  exit 1
fi
if [[ -f "$RETURNING_EXIT_FILE" ]]; then
  echo "rust-tui-journey: provider settings selection exited the active conversation" >&2
  exit 1
fi

# Pointer parity runs after the media and settings journeys so every retained
# component is proven independently. Click a source row and the explicit
# action; one undo restores the original @ query.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "Pointer @src/app/rend"
wait_for_text "Workspace files"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l ":"
wait_for_text "FILE-PREVIEW-BETA"
capture
preview_row="$(awk '/FILE-PREVIEW-BETA/ { print NR; exit }' <<<"$SCREEN")"
preview_col="$(python3 -c 'import sys; line=next(line for line in sys.stdin if "FILE-PREVIEW-BETA" in line); print(line.index("FILE-PREVIEW-BETA") + 2)' <<<"$SCREEN")"
printf -v preview_click '\033[<0;%d;%dM\033[<0;%d;%dm' "$preview_col" "$preview_row" "$preview_col" "$preview_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$preview_click"
wait_for_text "Lines 2"
capture
attach_row="$(awk '/Attach range/ { print NR; exit }' <<<"$SCREEN")"
attach_col="$(python3 -c 'import sys; line=next(line for line in sys.stdin if "Attach range" in line); print(line.index("Attach range") + 2)' <<<"$SCREEN")"
printf -v attach_click '\033[<0;%d;%dM\033[<0;%d;%dm' "$attach_col" "$attach_row" "$attach_col" "$attach_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$attach_click"
wait_without_text "FILE-PREVIEW-BETA"
wait_for_text "src/app/render.rs:2"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-z
wait_for_text "Workspace files"
wait_for_text "@src/app/rend"

# Symbols is a Settings detail page with live, reversible preview. The fourth
# candidate changes the current frame to ASCII without restarting; Escape
# restores the original Unicode tier and must leave config bytes untouched.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_without_text "Workspace files"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u
GLYPH_CONFIG="$HOME_DIR/.carina/config.json"
GLYPH_CONFIG_BEFORE_CANCEL="$WORK/glyph-config-before-cancel.json"
cp "$GLYPH_CONFIG" "$GLYPH_CONFIG_BEFORE_CANCEL"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /symbols
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Settings / Symbols"
wait_for_text "✓ Done"
grep -Fq "● Working" <<<"$SCREEN"
grep -Fq "✗ failed" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" 4
wait_for_text "+ Done"
grep -Fq "* Working" <<<"$SCREEN"
grep -Fq "x failed" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Symbols  ·  Automatic (Unicode)"
cmp -s "$GLYPH_CONFIG_BEFORE_CANCEL" "$GLYPH_CONFIG" || {
  echo "rust-tui-journey: cancelling the Symbols preview changed config" >&2
  exit 1
}

# Reopen the detail and explicitly apply ASCII. Selection only previews; Enter
# is the commit boundary and preserves every unrelated config key.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /symbols
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Settings / Symbols"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" 4
wait_for_text "+ Done"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Symbols  |  ASCII"
python3 - "$GLYPH_CONFIG" "$GLYPH_CONFIG_BEFORE_CANCEL" <<'PY'
import json
import pathlib
import sys

config = json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
before = json.loads(pathlib.Path(sys.argv[2]).read_text(encoding="utf-8"))
if config.get("tui_glyphs") != "ascii":
    raise SystemExit(f"Symbols Apply did not persist ASCII: {config!r}")
before["tui_glyphs"] = "ascii"
if config != before:
    raise SystemExit(
        f"Symbols Apply changed unrelated config state: before={before!r} after={config!r}"
    )
PY
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$RETURNING_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$RETURNING_EXIT_FILE" ]] || {
  echo "rust-tui-journey: returning UI did not exit" >&2
  exit 1
}
[[ "$(cat "$RETURNING_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: returning UI exit = $(cat "$RETURNING_EXIT_FILE")" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

# Relaunch through the packaged Go router, not carina-ui directly. The visible
# retained plan glyphs prove the router loaded and forwarded persisted ASCII.
GLYPH_RESTART_EXIT_FILE="$WORK/glyph-restart-ui-exit"
SESSION="carina-rust-tui-glyph-restart-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color CARINA_RUNTIME_MODE=legacy CARINA_UI_BIN='$STAGE/carina-ui' '$STAGE/carina' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_tools --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$GLYPH_RESTART_EXIT_FILE'; sleep 300"

wait_for_text "+ Inspect renderer"
wait_for_text "* Run tests"
for leaked in "✓ Inspect renderer" "● Run tests"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: persisted ASCII relaunch leaked Unicode plan glyphs" >&2
    exit 1
  fi
done

# Save Unicode from the relaunched UI, then relaunch once more with NO_COLOR.
# Unicode remaining active proves color suppression does not select a symbol
# tier or override the persisted preference.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /symbols
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Settings / Symbols"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" 2
wait_for_text "✓ Done"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Symbols  ·  Unicode"
grep -Eq '"tui_glyphs"[[:space:]]*:[[:space:]]*"unicode"' "$GLYPH_CONFIG"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_without_text "Symbols  ·  Unicode"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$GLYPH_RESTART_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$GLYPH_RESTART_EXIT_FILE" && "$(cat "$GLYPH_RESTART_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: persisted ASCII relaunch did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

GLYPH_NO_COLOR_EXIT_FILE="$WORK/glyph-no-color-ui-exit"
SESSION="carina-rust-tui-glyph-no-color-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color NO_COLOR=1 CARINA_RUNTIME_MODE=legacy CARINA_UI_BIN='$STAGE/carina-ui' '$STAGE/carina' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_tools --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$GLYPH_NO_COLOR_EXIT_FILE'; sleep 300"

wait_for_text "✓ Inspect renderer"
wait_for_text "● Run tests"
for leaked in "+ Inspect renderer" "* Run tests"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: NO_COLOR overrode persisted Unicode glyphs" >&2
    exit 1
  fi
done
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$GLYPH_NO_COLOR_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$GLYPH_NO_COLOR_EXIT_FILE" && "$(cat "$GLYPH_NO_COLOR_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: NO_COLOR glyph relaunch did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

TRANSCRIPT_EXIT_FILE="$WORK/transcript-ui-exit"
SESSION="carina-rust-tui-transcript-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 100 -y 24 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_transcript --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$TRANSCRIPT_EXIT_FILE'; sleep 300"

wait_for_text "TRANSCRIPT-FINAL-LINE"
if grep -Fq "TRANSCRIPT-FIRST-LINE" <<<"$SCREEN"; then
  echo "rust-tui-journey: long transcript did not open at its final visual row" >&2
  exit 1
fi
# A normal assistant message has no click action. Under the previous behavior,
# this pointer event collapsed the whole message and hid the final line.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l $'\033[<0;20;10M\033[<0;20;10m'
sleep 0.2
capture
grep -Fq "TRANSCRIPT-FINAL-LINE" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: clicking assistant output collapsed the conversation" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" PPage PPage PPage
wait_for_text "TRANSCRIPT-FIRST-LINE"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" NPage NPage NPage
wait_for_text "TRANSCRIPT-FINAL-LINE"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$TRANSCRIPT_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$TRANSCRIPT_EXIT_FILE" && "$(cat "$TRANSCRIPT_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: transcript scroll UI did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

STREAM_EXIT_FILE="$WORK/stream-ui-exit"
SESSION="carina-rust-tui-stream-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 100 -y 28 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_streaming --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$STREAM_EXIT_FILE'; sleep 300"

wait_for_text "STREAM-FIRST-PREFIX"
wait_for_text "STREAM-REPLACEMENT-TAIL"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" PPage PPage PPage PPage
wait_for_text "STREAM-REPLACEMENT-TOP"
touch "$STREAM_CONTINUE"
sleep 1.0
capture
if ! grep -Fq "STREAM-REPLACEMENT-TOP" <<<"$SCREEN" || grep -Fq "STREAM-NEW-TAIL" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: streaming output stole the reader's scrolled viewport" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" NPage NPage NPage NPage
wait_for_text "STREAM-FINAL-ONCE"
wait_without_text "STREAM-FIRST-PREFIX"
capture
if [[ "$(grep -Fo "STREAM-FINAL-ONCE" <<<"$SCREEN" | wc -l | tr -d ' ')" != "1" ]]; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: stream completion duplicated the retained assistant block" >&2
  exit 1
fi
if grep -Fq 'A **partial' <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: streaming Markdown leaked raw syntax or a reset generation" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$STREAM_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$STREAM_EXIT_FILE" && "$(cat "$STREAM_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: streaming UI did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

STREAM_RESTART_EXIT_FILE="$WORK/stream-restart-ui-exit"
SESSION="carina-rust-tui-stream-restart-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 100 -y 28 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_streaming --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$STREAM_RESTART_EXIT_FILE'; sleep 300"
wait_for_text "STREAM-FINAL-ONCE"
capture
if [[ "$(grep -Fo "STREAM-FINAL-ONCE" <<<"$SCREEN" | wc -l | tr -d ' ')" != "1" ]] \
  || grep -Fq "STREAM-FIRST-PREFIX" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: restart did not hydrate only the durable assistant body" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$STREAM_RESTART_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$STREAM_RESTART_EXIT_FILE" && "$(cat "$STREAM_RESTART_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: restarted streaming UI did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

TOOLS_EXIT_FILE="$WORK/tools-ui-exit"
SESSION="carina-rust-tui-tools-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_tools --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$TOOLS_EXIT_FILE'; sleep 300"

wait_for_text "Read    src/snake.cpp"
wait_for_text "✓ Inspect renderer"
wait_for_text "● Run tests"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /dyn
wait_for_text "dynamic-review"
grep -Fq "project" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: daemon command source badge was not rendered" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Tab
wait_for_text "/dynamic-review"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l " parser"
wait_for_text "<target>"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u
[[ "$(grep -Fc "• Plan" <<<"$SCREEN")" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: todo updates did not reduce to one Plan component" >&2
  exit 1
}
if grep -Eq '[▸▾][[:space:]]+Plan' <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: Plan checklist exposed a disclosure control" >&2
  exit 1
fi
[[ "$(grep -Fc "Read    src/snake.cpp" <<<"$SCREEN")" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: hydrated read lifecycle did not reduce to one row" >&2
  exit 1
}
grep -Fq "• Read    src/snake.cpp" <<<"$SCREEN"
for leaked in "TOOL" "CommandStarted" " live" " completed"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: tool projection leaked $leaked" >&2
    exit 1
  fi
done
if grep -Eq '^[[:space:]]*read[[:space:]]*$' <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: read tool name leaked as a redundant body row" >&2
  exit 1
fi
if grep -Eq '[▸▾][[:space:]]+Read' <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: compact read receipt exposed a disclosure control" >&2
  exit 1
fi
wait_for_text "MCP     docs.search"
grep -Fq "▸ MCP     docs.search" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: artifact-backed MCP result did not become inspectable" >&2
  exit 1
}
if grep -Fq "ARTIFACT-OUTPUT-UNIQUE" <<<"$SCREEN"; then
  echo "rust-tui-journey: artifact-backed MCP output started expanded" >&2
  exit 1
fi
mcp_row="$(awk '/MCP     docs.search/ { print NR; exit }' <<<"$SCREEN")"
printf -v mcp_click '\033[<0;5;%dM\033[<0;5;%dm' "$mcp_row" "$mcp_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$mcp_click"
wait_for_text "ARTIFACT-OUTPUT-UNIQUE"
[[ "$(grep -Fc "ARTIFACT-OUTPUT-UNIQUE" <<<"$SCREEN")" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: artifact output was not owned by exactly one component" >&2
  exit 1
}
[[ "$(grep -Fc "Run     cmake --build build" <<<"$SCREEN")" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: command lifecycle did not reduce to one component" >&2
  exit 1
}
grep -Fq "▸ Run     cmake --build build" <<<"$SCREEN"
if grep -Fq "COMMAND-OUTPUT-UNIQUE" <<<"$SCREEN"; then
  echo "rust-tui-journey: completed command output started expanded" >&2
  exit 1
fi

command_row="$(awk '/Run     cmake --build build/ { print NR; exit }' <<<"$SCREEN")"
printf -v command_click '\033[<0;5;%dM\033[<0;5;%dm' "$command_row" "$command_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$command_click"
wait_for_text "COMMAND-OUTPUT-UNIQUE"
[[ "$(grep -Fc "COMMAND-OUTPUT-UNIQUE" <<<"$SCREEN")" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: command output was not owned by exactly one component" >&2
  exit 1
}

# Expanding historical components intentionally preserves the reader's
# viewport. Collapse them before asserting lower hydrated components instead
# of assuming expansion will force-follow the transcript bottom.
capture
command_row="$(awk '/Run     cmake --build build/ { print NR; exit }' <<<"$SCREEN")"
printf -v command_click '\033[<0;5;%dM\033[<0;5;%dm' "$command_row" "$command_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$command_click"
wait_without_text "COMMAND-OUTPUT-UNIQUE"
capture
mcp_row="$(awk '/MCP     docs.search/ { print NR; exit }' <<<"$SCREEN")"
printf -v mcp_click '\033[<0;5;%dM\033[<0;5;%dm' "$mcp_row" "$mcp_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$mcp_click"
wait_without_text "ARTIFACT-OUTPUT-UNIQUE"

wait_for_text "Edited ×2 · src/snake.cpp, src/broken.cpp"
grep -Fq "▾ Edited ×2 · src/snake.cpp, src/broken.cpp  +1 -1  1 failed" <<<"$SCREEN"
grep -Eq "src/snake.cpp.*applied" <<<"$SCREEN"
[[ "$(grep -Fc "EDIT-DIFF-UNIQUE" <<<"$SCREEN")" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: hydrated applied edit did not start with one visible diff" >&2
  exit 1
}
edit_row="$(awk '/Edited ×2 · src\/snake.cpp, src\/broken.cpp/ { print NR; exit }' <<<"$SCREEN")"
printf -v edit_click '\033[<0;5;%dM\033[<0;5;%dm' "$edit_row" "$edit_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$edit_click"
wait_without_text "EDIT-DIFF-UNIQUE"
grep -Fq "▸ Edited ×2 · src/snake.cpp, src/broken.cpp  +1 -1  1 failed" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: hydrated applied edit could not be explicitly folded" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$edit_click"
wait_for_text "EDIT-DIFF-UNIQUE"
[[ "$(grep -Fc "EDIT-DIFF-UNIQUE" <<<"$SCREEN")" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: edit diff was not owned by exactly one component" >&2
  exit 1
}
grep -Eq "src/broken.cpp.*failed" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: failed edit lost its visible semantic status" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$TOOLS_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$TOOLS_EXIT_FILE" && "$(cat "$TOOLS_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: tool projection UI did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

LIVE_PATCH_EXIT_FILE="$WORK/live-patch-ui-exit"
SESSION="carina-rust-tui-live-patch-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_live_patch --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$LIVE_PATCH_EXIT_FILE'; sleep 300"

# This conversation hydrates no items. The diff must therefore arrive through
# the live canonical event stream, then settle in the same retained component.
wait_for_text "EDIT-DIFF-LIVE-UNIQUE"
[[ "$(grep -Fc "Edit    src/live.rs" <<<"$SCREEN")" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: live patch proposal did not reduce to one Edit component" >&2
  exit 1
}
wait_for_text "Edit    src/live.rs  +1  applied"
grep -Eq 'Edit    src/live\.rs.*\+1.*applied' <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: applied edit did not expose its diff statistics" >&2
  exit 1
}
grep -Fq "EDIT-DIFF-LIVE-UNIQUE" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: applied live patch hid its reviewable diff" >&2
  exit 1
}
if grep -Fq "PATCH-RECEIPT-LIVE-UNIQUE" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: terminal artifact receipt replaced the reviewable edit diff" >&2
  exit 1
fi
[[ "$(grep -Fc "EDIT-DIFF-LIVE-UNIQUE" <<<"$SCREEN")" == "1" ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: live patch diff was duplicated after apply" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$LIVE_PATCH_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$LIVE_PATCH_EXIT_FILE" && "$(cat "$LIVE_PATCH_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: live patch UI did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

INVALID_SESSION_EXIT_FILE="$WORK/invalid-session-ui-exit"
SESSION="carina-rust-tui-invalid-session-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 80 -y 28 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_missing --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$INVALID_SESSION_EXIT_FILE'; sleep 300"

wait_for_text "That conversation is no longer available. Choose another conversation."
grep -Fq "Conversations" <<<"$SCREEN"
grep -Fq "Search conversations" <<<"$SCREEN"
grep -Fq "Tab  Scope" <<<"$SCREEN"
grep -Fq "New conversation" <<<"$SCREEN"
if [[ -f "$INVALID_SESSION_EXIT_FILE" ]] || grep -Fq "sess_missing" <<<"$SCREEN"; then
  echo "rust-tui-journey: invalid requested session exited or leaked its internal id" >&2
  exit 1
fi
grep -Fq "Rename selected" <<<"$SCREEN"
rename_row="$(awk '/Rename selected/ { print NR; exit }' <<<"$SCREEN")"
rename_col="$(python3 -c 'import sys; line=next(line for line in sys.stdin if "Rename selected" in line); print(line.index("Rename selected") + 2)' <<<"$SCREEN")"
printf -v rename_click '\033[<0;%d;%dM\033[<0;%d;%dm' "$rename_col" "$rename_row" "$rename_col" "$rename_row"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "$rename_click"
wait_for_text "Rename  _"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "Release cleanup"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Conversation renamed to Release cleanup"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" r
wait_for_text "Rename  Release cleanup_"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Search conversations"
grep -Fq "Release cleanup" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" a
wait_for_text "Archive selected conversation?"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Search conversations"
grep -Fq "Release cleanup" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" a Enter
wait_for_text "Conversation archived. Open Archived to restore it."
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Tab Tab
wait_for_text "Archived"
wait_for_text "Release cleanup"
grep -Fq "Restore selected" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" u
wait_for_text "Conversation restored to the active list."
wait_for_text "No archived conversations match this search."
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Tab
wait_for_text "Current workspace"
wait_for_text "Release cleanup"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" / workspace
wait_for_text "/  workspace"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Down Enter
wait_for_text "Image received"
if grep -Fq "Search conversations" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: restored conversation remained in the session browser" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$INVALID_SESSION_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$INVALID_SESSION_EXIT_FILE" ]] || {
  echo "rust-tui-journey: invalid-session recovery UI did not exit" >&2
  exit 1
}
[[ "$(cat "$INVALID_SESSION_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: invalid-session recovery exit = $(cat "$INVALID_SESSION_EXIT_FILE")" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

EMPTY_MODELS_EXIT_FILE="$WORK/empty-models-ui-exit"
SESSION="carina-rust-tui-empty-models-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$EMPTY_MODELS_SOCKET' --workspace '$WORKSPACE' --session sess_returning --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$EMPTY_MODELS_EXIT_FILE'; sleep 300"

wait_for_text "Test has no compatible models."
grep -Fq "Diagnostic only" <<<"$SCREEN"
if grep -Fq "╭ Message" <<<"$SCREEN" || grep -Fq "sess_returning" <<<"$SCREEN"; then
  echo "rust-tui-journey: zero-model startup mounted a composer or leaked a session id" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
for _ in $(seq 1 50); do
  [[ -s "$EMPTY_MODELS_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$EMPTY_MODELS_EXIT_FILE" ]] || {
  echo "rust-tui-journey: zero-model diagnostic UI did not exit" >&2
  exit 1
}
[[ "$(cat "$EMPTY_MODELS_EXIT_FILE")" == "6" ]] || {
  echo "rust-tui-journey: zero-model diagnostic exit = $(cat "$EMPTY_MODELS_EXIT_FILE")" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

REVOCATION_EXIT_FILE="$WORK/revocation-ui-exit"
SESSION="carina-rust-tui-revocation-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$REVOCATION_SOCKET' --workspace '$WORKSPACE' --session sess_returning --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$REVOCATION_EXIT_FILE'; sleep 300"

wait_for_text "Describe the change you want to make."
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l REVOCATION-DRAFT
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Execution is not ready. Your draft was kept"
grep -Fq "Connect a provider" <<<"$SCREEN"
[[ ! -e "$REVOCATION_SUBMIT_CAPTURE" ]] || {
  echo "rust-tui-journey: revoked provider reached execution.start" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" d
wait_for_text "Diagnostic only"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" r
wait_for_text "REVOCATION-DRAFT"
grep -Fq "Describe the change you want to make." <<<"$SCREEN"
[[ ! -e "$REVOCATION_SUBMIT_CAPTURE" ]] || {
  echo "rust-tui-journey: provider repair submitted the retained draft implicitly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-r
wait_for_text "Prompt history"
wait_for_text "Workspace history unavailable"
wait_for_text "No matching prompts"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_without_text "Prompt history"
wait_for_text "REVOCATION-DRAFT"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$REVOCATION_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$REVOCATION_EXIT_FILE" && "$(cat "$REVOCATION_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: provider revocation recovery did not exit cleanly" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true

SESSION="carina-rust-tui-governance-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_1 --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$GOV_EXIT_FILE'; sleep 300"

wait_for_text "Approval required"
grep -Fq "Run verification" <<<"$SCREEN"
for internal in user_question_resolved "Value: 北京" "Timed out" q_old; do
  if grep -Fq "$internal" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: hydrated governance metadata leaked into the conversation: $internal" >&2
    exit 1
  fi
done
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_without_text "Approval required"
wait_for_text "Carina needs your input"
grep -Fq "How thorough should the verification be?" <<<"$SCREEN"
grep -Fq "Focused  Run affected checks" <<<"$SCREEN"
grep -Fq "Full  Run the complete suite" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Down Enter
wait_without_text "Carina needs your input"
wait_for_text "Verification complete"
for internal in user_question_resolved "Value: full" "Timed out" "Question q_1" "Approval perm_1"; do
  if grep -Fq "$internal" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: live governance metadata leaked into the conversation: $internal" >&2
    exit 1
  fi
done
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$GOV_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$GOV_EXIT_FILE" ]] || {
  echo "rust-tui-journey: governance UI did not exit" >&2
  exit 1
}
[[ "$(cat "$GOV_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: governance UI exit = $(cat "$GOV_EXIT_FILE")" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
GOV_RESTART_EXIT_FILE="$WORK/governance-restart-ui-exit"
SESSION="carina-rust-tui-governance-restart-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_governance_restart --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$GOV_RESTART_EXIT_FILE'; sleep 300"

# No live permission/question events are emitted for this session. Both input
# owners must be reconstructed from durable session.items after process start.
wait_for_text "Approval required"
wait_for_text "Resume verification after restart"
if grep -Fq "Which recovered verification scope?" <<<"$SCREEN"; then
  echo "rust-tui-journey: queued recovered question replaced the active approval" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_without_text "Approval required"
wait_for_text "Which recovered verification scope?"
grep -Fq "Focused  Affected checks" <<<"$SCREEN"
grep -Fq "Full  Complete suite" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Down Enter
wait_without_text "Carina needs your input"
wait_for_text "Recovered verification completed"
capture
for leaked in perm_restart q_restart user_question_requested "ACTION  Needs input"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: restart governance leaked internal state: $leaked" >&2
    exit 1
  fi
done
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$GOV_RESTART_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$GOV_RESTART_EXIT_FILE" && "$(cat "$GOV_RESTART_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: restarted governance UI did not exit cleanly" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
PLAN_EXIT_FILE="$WORK/plan-review-ui-exit"
SESSION="carina-rust-tui-plan-review-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$PLAN_SOCKET' --workspace '$WORKSPACE' --session sess_plan --locale zh-Hans --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$PLAN_EXIT_FILE'; sleep 300"

wait_for_text "描述你想完成的改动。"
wait_for_text "执行 ⇧Tab"
# xterm encodes Shift-Tab as CSI Z. It changes the idle session mode through
# the typed daemon RPC instead of leaking into the composer as a plain Tab.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l $'\033[Z'
wait_for_text "规划模式已启用"
wait_for_text "计划 ⇧Tab"
for _ in $(seq 1 100); do
  [[ -f "$PLAN_CAPTURE" ]] && grep -Fq '"method":"session.plan_mode"' "$PLAN_CAPTURE" && break
  sleep 0.05
done
[[ -f "$PLAN_CAPTURE" ]] && grep -Fq '"method":"session.plan_mode"' "$PLAN_CAPTURE" || {
  echo "rust-tui-journey: Shift-Tab did not call session.plan_mode" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "Draft a provider recovery plan"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "审阅计划"
grep -Fq "Implement provider discovery with typed readiness and recovery." <<<"$SCREEN"
grep -Fq "等待你的决定" <<<"$SCREEN"
grep -Fq "批准" <<<"$SCREEN"
grep -Fq "修改" <<<"$SCREEN"
grep -Fq "评论" <<<"$SCREEN"
grep -Fq "关闭审阅" <<<"$SCREEN"
grep -Fq "A 批准 S 修改 C 评论 M 范围 Q 关闭" <<<"$SCREEN"
python3 - "$PLAN_CAPTURE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    requests = [json.loads(line) for line in stream if line.strip()]
methods = [request.get("method") for request in requests]
if "session.plan_mode" not in methods or "execution.start" not in methods:
    raise SystemExit("plan mode or plan submit request was not captured")
if methods.index("session.plan_mode") > methods.index("execution.start"):
    raise SystemExit("plan prompt was submitted before plan mode was enabled")
submit = next(request for request in requests if request.get("method") == "execution.start")
params = submit.get("params", {})
if params.get("session_id") != "sess_plan" or params.get("agent") != "plan":
    raise SystemExit(f"unexpected plan submit payload: {params!r}")
if params.get("prompt") != "Draft a provider recovery plan":
    raise SystemExit(f"unexpected plan prompt: {params.get('prompt')!r}")
if params.get("locale") != "zh":
    raise SystemExit(f"Simplified Chinese plan locale was not submitted: {params!r}")
PY

# Q closes only the local Review. Plan mode and the retained typed result stay
# intact, so the exact same plan can be reopened without daemon mutation.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l Q
wait_without_text "审阅计划"
wait_for_text "已关闭计划审阅。规划模式保持启用，未执行任何操作。"
wait_for_text "计划 ⇧Tab"
assert_plan_review_local_only "Q close"

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /view-plan
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "审阅计划"
grep -Fq "Implement provider discovery with typed readiness and recovery." <<<"$SCREEN"

# C owns input inside Review. Saving a Unicode line comment stays local; S
# closes Review and places an editable, anchored revision request in composer.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l C
wait_for_text "为 L1 添加评论。Enter 保存；Esc 放弃。"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "补充回滚验证"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
# The retained in-review count is authoritative; the success notice may be
# replaced by the next render before a PTY capture observes it.
wait_for_text "1 条评论"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l S
wait_without_text "审阅计划"
wait_for_text "修改草稿已放入输入框。规划模式仍处于启用状态"
wait_for_text "请根据下面的审阅评论修改此计划。"
wait_for_text "第 1 行：补充回滚验证"
wait_for_text "计划 ⇧Tab"
assert_plan_review_local_only "S revise"

# Esc remains the compatibility alias for Revise, not a close action.
for _ in $(seq 1 8); do
  TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u
done
wait_without_text "补充回滚验证"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /view-plan
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "审阅计划"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_without_text "审阅计划"
wait_for_text "修改草稿已放入输入框。规划模式仍处于启用状态"
assert_plan_review_local_only "Esc revise"

for _ in $(seq 1 8); do
  TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u
done
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /view-plan
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "审阅计划"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l A
wait_without_text "审阅计划"
for _ in $(seq 1 100); do
  grep -Fq '"method":"session.approve_plan"' "$PLAN_CAPTURE" && break
  sleep 0.05
done
grep -Fq '"method":"session.approve_plan"' "$PLAN_CAPTURE" || {
  echo "rust-tui-journey: Plan Review approval did not call session.approve_plan" >&2
  exit 1
}
python3 - "$PLAN_CAPTURE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    requests = [json.loads(line) for line in stream if line.strip()]
approvals = [request for request in requests if request.get("method") == "session.approve_plan"]
if len(approvals) != 1:
    raise SystemExit(f"expected exactly one plan approval, got {len(approvals)}")
params = approvals[0].get("params", {})
if params != {"session_id": "sess_plan", "run_id": "run_plan"}:
    raise SystemExit(f"Plan Review approval lost its run identity: {params!r}")
if sum(request.get("method") == "execution.start" for request in requests) != 1:
    raise SystemExit("Plan Review local actions started an extra execution")
PY
wait_for_text "计划已批准，实施已排队。"
# The mock leaves a bounded interval before lifecycle events so this notice
# proves Rust adopted the canonical approval `task` directly from the RPC.
wait_for_text "Approved plan implemented"
grep -Fq "╭ 消息" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: completed implementation did not restore the ready composer" >&2
  exit 1
}
if grep -Fq "审阅计划" <<<"$SCREEN"; then
  echo "rust-tui-journey: completed implementation reopened the consumed Plan Review" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Up
wait_for_text "输入历史"
grep -Fq "↑↓ 浏览  Enter 编辑  Esc 恢复草稿" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: Prompt History browse journey was not localized" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /st
wait_for_text "查看运行状态"
grep -Fq "命令" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: Simplified Chinese slash completion title was not localized" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "当前会话"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /help
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "打开检查点历史"
for keymap_action in \
  "在下一个安全点暂停" \
  "强制取消当前任务" \
  "立即引导当前任务" \
  "运行时立即发送" \
  "打开检查点历史" \
  "回复运行时排队后续输入"; do
  grep -Fq "$keymap_action" <<<"$SCREEN" || {
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: /keymap missing $keymap_action" >&2
    exit 1
  }
done
grep -Fq "命令" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: help did not open the localized command palette" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" PageDown PageDown PageDown
wait_for_text "/settings"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-u
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /settings
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "语言  ·  zh-Hans"
grep -Fq "服务商  ·  Test" <<<"$SCREEN"
grep -Fq "模式  ·  执行 · 直接实施" <<<"$SCREEN"
grep -Fq "状态  ·  运行时、Agent、用量与改动" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l /status
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "当前会话"
grep -Fq "运行时" <<<"$SCREEN"
grep -Fq "2 个排队" <<<"$SCREEN"
grep -Fq "1 个活动" <<<"$SCREEN"
grep -Fq "输入 120" <<<"$SCREEN"
grep -Fq "1 项改动" <<<"$SCREEN"
for leaked in "internal-agent-task" "internal-artifact-id" "internal-patch-id"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: status overview leaked internal ID: $leaked" >&2
    exit 1
  fi
done
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$PLAN_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$PLAN_EXIT_FILE" && "$(cat "$PLAN_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: plan review UI did not exit cleanly" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
PAUSED_EXIT_FILE="$WORK/paused-ui-exit"
SESSION="carina-rust-tui-paused-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_paused --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$PAUSED_EXIT_FILE'; sleep 300"

wait_for_text "run paused"
grep -Fq "Resume" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" /resume Enter
wait_for_text "Esc pause safely"
capture
grep -Fq "Esc pause safely" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: active execution did not expose its interrupt affordance" >&2
  exit 1
}
grep -Eq '[0-9]+s' <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: active execution did not expose elapsed time" >&2
  exit 1
}
if grep -Fq "ctx 12%" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: routine context telemetry displaced the compact active status" >&2
  exit 1
fi
wait_for_text "Returning paused execution complete"
wait_without_text "Esc pause safely"
if grep -Fq "ExecutionCompleted" <<<"$SCREEN"; then
  echo "rust-tui-journey: paused completion rendered as an event receipt" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$PAUSED_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$PAUSED_EXIT_FILE" ]] || {
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
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Up
for _ in $(seq 1 50); do
  capture
  [[ "$(grep -Fc "second prompt" <<<"$SCREEN")" -ge 2 ]] && break
  sleep 0.1
done
[[ "$(grep -Fc "second prompt" <<<"$SCREEN")" -ge 2 ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: Up did not recall the newest prompt into the composer" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Down
for _ in $(seq 1 50); do
  capture
  [[ "$(grep -Fc "second prompt" <<<"$SCREEN")" -eq 1 ]] && break
  sleep 0.1
done
[[ "$(grep -Fc "second prompt" <<<"$SCREEN")" -eq 1 ]] || {
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: Down did not restore the empty draft" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l draft
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
wait_for_text "Could not create the history branch"
grep -Fq "second prompt" <<<"$SCREEN"
if grep -Fq "sess_history" <<<"$SCREEN"; then
  echo "rust-tui-journey: branch failure leaked the internal session id" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "draft"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Press Esc again to edit an earlier prompt"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
wait_for_text "Choose a prompt in the conversation"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Up
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Could not create the history branch"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Branched before the selected prompt"
wait_for_text "first prompt"
python3 - "$HISTORY_FORK_CAPTURE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    requests = [json.loads(line) for line in stream if line.strip()]
if len(requests) != 3:
    raise SystemExit(f"expected 3 history fork requests, got {len(requests)}")
retry_first, retry_second = requests[-2:]
if retry_first.get("client_fork_id") != retry_second.get("client_fork_id"):
    raise SystemExit("history fork retry changed client_fork_id")
if not retry_first.get("before_first") or "last_run_id" in retry_first:
    raise SystemExit(f"first-prompt fork did not use before_first: {retry_first}")
PY
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$HISTORY_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$HISTORY_EXIT_FILE" ]] || {
  echo "rust-tui-journey: history edit UI did not exit" >&2
  exit 1
}
[[ "$(cat "$HISTORY_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: history edit UI exit = $(cat "$HISTORY_EXIT_FILE")" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
RECONNECT_EXIT_FILE="$WORK/reconnect-ui-exit"
SESSION="carina-rust-tui-reconnect-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_reconnect --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$RECONNECT_EXIT_FILE'; sleep 300"

wait_for_text "reconnect source prompt"
wait_for_text "Runtime unavailable"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "UNSENT-RECONNECT-DRAFT"
wait_for_text "UNSENT-RECONNECT-DRAFT"
touch "$RECONNECT_CONTROL_CONTINUE"
touch "$RECONNECT_CONTINUE"
wait_for_text "RECONNECT-RECOVERED-ANSWER"
grep -Fq "UNSENT-RECONNECT-DRAFT" <<<"$SCREEN"
for leaked in "run_reconnect" "ExecutionStarted" "event stream closed"; do
  if grep -Fq "$leaked" <<<"$SCREEN"; then
    printf '%s\n' "$SCREEN" >&2
    echo "rust-tui-journey: reconnect leaked internal lifecycle text: $leaked" >&2
    exit 1
  fi
done
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
for _ in $(seq 1 100); do
  [[ -s "$RECONNECT_SUBMIT_CAPTURE" ]] && break
  sleep 0.1
done
python3 - "$RECONNECT_SUBMIT_CAPTURE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    request = json.load(stream)
if request.get("session_id") != "sess_reconnect":
    raise SystemExit(f"reconnected control client switched session: {request!r}")
if request.get("prompt") != "UNSENT-RECONNECT-DRAFT":
    raise SystemExit(f"reconnected control client lost the draft: {request!r}")
PY
wait_for_text "RECONNECT-DRAFT-COMPLETED"
python3 - "$RECONNECT_CAPTURE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    requests = [json.loads(line) for line in stream if line.strip()]
if len(requests) != 2:
    raise SystemExit(f"expected exactly two event streams, got {requests!r}")
if requests[0].get("since") != 0 or requests[1].get("since") != 1:
    raise SystemExit(f"event stream did not resume from the durable cursor: {requests!r}")
if any(request.get("session_id") != "sess_reconnect" for request in requests):
    raise SystemExit(f"event stream switched conversation during reconnect: {requests!r}")
PY
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$RECONNECT_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$RECONNECT_EXIT_FILE" && "$(cat "$RECONNECT_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: reconnect UI did not exit cleanly" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
UNKNOWN_EXIT_FILE="$WORK/unknown-submit-exit"
SESSION="carina-rust-tui-unknown-submit-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --socket '$GOV_SOCKET' --workspace '$WORKSPACE' --session sess_unknown_submit --locale en --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$UNKNOWN_EXIT_FILE'; sleep 300"
wait_for_text "Describe the change"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l "UNKNOWN-SUBMISSION-DRAFT"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "Submission status is unknown"
grep -Fq "UNKNOWN-SUBMISSION-DRAFT" <<<"$SCREEN"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" -l -- "-EDITED"
wait_for_text "UNKNOWN-SUBMISSION-DRAFT-EDITED"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "UNKNOWN-SUBMISSION-RECONCILED"
grep -Fq "UNKNOWN-SUBMISSION-DRAFT-EDITED" <<<"$SCREEN"
python3 - "$UNKNOWN_SUBMIT_CAPTURE" <<'PY'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as stream:
    requests = [json.loads(line) for line in stream if line.strip()]
if len(requests) != 2:
    raise SystemExit(f"expected one unknown submit and one reconciliation, got {requests!r}")
first, second = requests
if first != second:
    raise SystemExit(f"submission reconciliation changed the envelope: {requests!r}")
if first.get("prompt") != "UNKNOWN-SUBMISSION-DRAFT":
    raise SystemExit(f"unknown submission captured the wrong prompt: {first!r}")
if not first.get("client_submission_id"):
    raise SystemExit(f"unknown submission had no idempotency key: {first!r}")
PY
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c C-c
for _ in $(seq 1 50); do
  [[ -s "$UNKNOWN_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$UNKNOWN_EXIT_FILE" && "$(cat "$UNKNOWN_EXIT_FILE")" == "0" ]] || {
  echo "rust-tui-journey: unknown submission UI did not exit cleanly" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
DIAGNOSTIC_EXIT_FILE="$WORK/runtime-diagnostic-exit"
DIAGNOSTIC_LOG="$WORK/runtime-diagnostic.log"
printf '%s\n' 'runtime-log-marker: active execution is draining' > "$DIAGNOSTIC_LOG"
SESSION="carina-rust-tui-runtime-diagnostic-$$"
TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' PATH='$STAGE:/usr/bin:/bin' TERM=xterm-256color '$STAGE/carina-ui' --runtime-diagnostic --workspace '$WORKSPACE' --runtime-id runtime_legacy --runtime-log '$DIAGNOSTIC_LOG' --missing-method execution.start --obligation execution:run_active --locale zh-Hans --carina-bin '$STAGE/carina-runtime-stop-blocked' --no-alt-screen; code=\$?; printf '%s' \"\$code\" > '$DIAGNOSTIC_EXIT_FILE'; sleep 300"

wait_for_text "诊断"
wait_for_text "运行时不可用"
wait_for_text "execution.start"
wait_for_text "execution:run_active"
if grep -Fq "initialize runtime protocol" <<<"$SCREEN"; then
  printf '%s\n' "$SCREEN" >&2
  echo "rust-tui-journey: runtime incompatibility leaked bootstrap stderr" >&2
  exit 1
fi
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" d
wait_for_text "runtime-log-marker"
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Enter
wait_for_text "execution:run_active"
[[ ! -s "$DIAGNOSTIC_EXIT_FILE" ]] || {
  echo "rust-tui-journey: blocked safe restart exited the diagnostic scene" >&2
  exit 1
}
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" Escape
for _ in $(seq 1 50); do
  [[ -s "$DIAGNOSTIC_EXIT_FILE" ]] && break
  sleep 0.1
done
[[ -s "$DIAGNOSTIC_EXIT_FILE" && "$(cat "$DIAGNOSTIC_EXIT_FILE")" == "2" ]] || {
  echo "rust-tui-journey: runtime diagnostic did not exit with user-cancel outcome" >&2
  exit 1
}

echo "rust-tui-journey: ok"
