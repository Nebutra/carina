#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ "$(uname -s)" == "Darwin" ]] || {
  echo "native-clipboard: macOS is required" >&2
  exit 77
}
command -v tmux >/dev/null 2>&1 || {
  echo "native-clipboard: tmux is required" >&2
  exit 127
}
[[ -x "$ROOT/target/debug/carina-ui" ]] || {
  echo "native-clipboard: build target/debug/carina-ui first" >&2
  exit 1
}

WORK="$(mktemp -d /tmp/carina-native-clipboard.XXXXXX)"
SOCKET="$WORK/carina.sock"
WORKSPACE="$WORK/workspace"
HOME_DIR="$WORK/home"
TEMP_DIR="$WORK/tmp"
BACKUP="$WORK/pasteboard.plist"
IMAGE="$WORK/source.png"
SESSION="carina-native-clipboard-$$"
SERVER_PID=""

restore_clipboard() {
  [[ -f "$BACKUP" ]] || return 0
  swift - "$BACKUP" <<'SWIFT' >/dev/null 2>&1 || true
import AppKit
import Foundation
let url = URL(fileURLWithPath: CommandLine.arguments[1])
let data = try Data(contentsOf: url)
let items = try PropertyListDecoder().decode([[String: Data]].self, from: data)
let pasteboard = NSPasteboard.general
pasteboard.clearContents()
let restored = items.map { values -> NSPasteboardItem in
    let item = NSPasteboardItem()
    for (type, data) in values {
        item.setData(data, forType: NSPasteboard.PasteboardType(type))
    }
    return item
}
pasteboard.writeObjects(restored)
SWIFT
}

cleanup() {
  restore_clipboard
  TMUX_TMPDIR="$WORK" tmux kill-session -t "$SESSION" >/dev/null 2>&1 || true
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" >/dev/null 2>&1 || true
  find "$WORK" -depth -delete >/dev/null 2>&1 || true
}
trap cleanup EXIT
mkdir -p "$WORKSPACE" "$HOME_DIR" "$TEMP_DIR"
cp "$ROOT/docs/brand/assets/logo/raster/carina-symbol.png" "$IMAGE"

# Preserve every current pasteboard item and UTI before injecting the fixture.
swift - "$BACKUP" "$IMAGE" <<'SWIFT'
import AppKit
import Foundation
let backup = URL(fileURLWithPath: CommandLine.arguments[1])
let imagePath = CommandLine.arguments[2]
let pasteboard = NSPasteboard.general
let items = (pasteboard.pasteboardItems ?? []).map { item -> [String: Data] in
    var values: [String: Data] = [:]
    for type in item.types {
        if let data = item.data(forType: type) { values[type.rawValue] = data }
    }
    return values
}
let encoded = try PropertyListEncoder().encode(items)
try encoded.write(to: backup, options: .atomic)
guard let image = NSImage(contentsOfFile: imagePath) else {
    fatalError("cannot decode clipboard fixture")
}
pasteboard.clearContents()
guard pasteboard.writeObjects([image]) else {
    fatalError("cannot write clipboard fixture")
}
SWIFT

python3 - "$SOCKET" "$WORKSPACE" <<'PY' &
import base64, json, os, socket, sys, threading, time
socket_path, workspace = sys.argv[1:]

def send(stream, value):
    stream.write((json.dumps(value) + "\n").encode()); stream.flush()

def handle(connection):
    with connection:
        stream = connection.makefile("rwb")
        while line := stream.readline():
            request = json.loads(line); request_id = request.get("id")
            method = request.get("method"); params = request.get("params", {})
            if method == "session.events.stream":
                send(stream, {"jsonrpc":"2.0","id":request_id,"result":{"cursor":0}})
                time.sleep(60); return
            if method == "runtime.initialize":
                result = {"runtime_version":"clipboard-evidence","protocol_version":"1.3.0","projection_version":"1.0.0","capabilities":{"rpc_methods":["execution.start","model.list","session.create","session.events.stream","session.list"]}}
            elif method == "model.list":
                result = {"default_model":"test/vision","reasoner":{"backend":"test","available":True},"providers":[{"id":"test","name":"Test","registered":True,"available":True,"models":[{"id":"test/vision","display_id":"vision","name":"Vision","available":True,"image_input":True}]}]}
            elif method == "session.list":
                result = [{"session_id":"sess_clipboard","workspace_root":workspace,"status":"active","next_model":"test/vision","execution_status":"ready"}]
            elif method == "session.resume":
                result = {"session_id":"sess_clipboard","workspace_root":workspace,"status":"active","next_model":"test/vision","execution_status":"ready"}
            elif method == "session.items": result = []
            elif method == "history.recent": result = {"entries":[],"count":0,"scope":"workspace"}
            elif method == "artifact.upload":
                content = base64.b64decode(params.get("content_base64", ""))
                if params.get("chunk_index") == 0 and not content.startswith(b"\x89PNG"):
                    send(stream,{"jsonrpc":"2.0","id":request_id,"error":{"code":-32602,"message":"expected PNG"}}); continue
                result = ({"upload_id":params["upload_id"],"next_chunk_index":params["chunk_index"] + 1}
                    if not params.get("final") else
                    {"artifact_id":params["sha256"],"media_type":"image/png","bytes":params["total_bytes"],"origin":params.get("origin","")})
            else:
                send(stream,{"jsonrpc":"2.0","id":request_id,"error":{"code":-32601,"message":method}}); continue
            send(stream,{"jsonrpc":"2.0","id":request_id,"result":result})

try: os.unlink(socket_path)
except FileNotFoundError: pass
listener = socket.socket(socket.AF_UNIX); listener.bind(socket_path); listener.listen()
while True:
    connection, _ = listener.accept()
    threading.Thread(target=handle, args=(connection,), daemon=True).start()
PY
SERVER_PID=$!
for _ in $(seq 1 50); do [[ -S "$SOCKET" ]] && break; sleep 0.1; done
[[ -S "$SOCKET" ]] || { echo "native-clipboard: fake daemon did not start" >&2; exit 1; }

TMUX_TMPDIR="$WORK" tmux new-session -d -s "$SESSION" -x 120 -y 40 \
  "cd '$WORKSPACE' && env -i HOME='$HOME_DIR' TMPDIR='$TEMP_DIR' PATH='/usr/bin:/bin' TERM=xterm-256color CARINA_TERMINAL_GRAPHICS=off '$ROOT/target/debug/carina-ui' --socket '$SOCKET' --workspace '$WORKSPACE' --session sess_clipboard --locale zh-Hans --no-alt-screen; code=\$?; echo UI_EXIT:\$code; sleep 30"

SCREEN=""
for _ in $(seq 1 100); do
  SCREEN="$(TMUX_TMPDIR="$WORK" tmux capture-pane -p -t "$SESSION" 2>/dev/null || true)"
  grep -Fq "描述你想完成的改动" <<<"$SCREEN" && break
  sleep 0.1
done
grep -Fq "描述你想完成的改动" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "native-clipboard: conversation did not become ready" >&2
  exit 1
}

TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-v
for _ in $(seq 1 100); do
  SCREEN="$(TMUX_TMPDIR="$WORK" tmux capture-pane -p -t "$SESSION" 2>/dev/null || true)"
  grep -Fq "图片" <<<"$SCREEN" && find "$TEMP_DIR" -type f -name 'carina-clipboard-*.png' -print -quit | grep -q . && break
  sleep 0.1
done
grep -Fq "图片" <<<"$SCREEN" || {
  printf '%s\n' "$SCREEN" >&2
  echo "native-clipboard: localized image chip did not render" >&2
  exit 1
}
TEMP_IMAGE="$(find "$TEMP_DIR" -type f -name 'carina-clipboard-*.png' -print -quit)"
[[ -s "$TEMP_IMAGE" ]] || { echo "native-clipboard: staged PNG was not retained" >&2; exit 1; }

# Exit without sending. App/MediaComposer drop owns deletion of the temporary PNG.
TMUX_TMPDIR="$WORK" tmux send-keys -t "$SESSION" C-c
for _ in $(seq 1 100); do
  TMUX_TMPDIR="$WORK" tmux has-session -t "$SESSION" 2>/dev/null || break
  sleep 0.1
done
[[ ! -e "$TEMP_IMAGE" ]] || {
  echo "native-clipboard: unsent temporary image survived TUI exit: $TEMP_IMAGE" >&2
  exit 1
}
restore_clipboard
swift - "$BACKUP" <<'SWIFT'
import AppKit
import Foundation
let expected = try PropertyListDecoder().decode(
    [[String: Data]].self,
    from: Data(contentsOf: URL(fileURLWithPath: CommandLine.arguments[1]))
)
let actual = (NSPasteboard.general.pasteboardItems ?? []).map { item -> [String: Data] in
    var values: [String: Data] = [:]
    for type in item.types {
        if let data = item.data(forType: type) { values[type.rawValue] = data }
    }
    return values
}
guard actual == expected else { fatalError("pasteboard restoration was not byte-equivalent") }
SWIFT
echo "native-clipboard: real pasteboard image attached, unsent PNG deleted, pasteboard restored"
