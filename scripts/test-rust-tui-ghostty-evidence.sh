#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CMUX="${CMUX_BIN:-$(command -v cmux || true)}"
OUTPUT="${1:-$ROOT/.trellis/tasks/07-24-provider-onboarding/evidence/ghostty-media-preview.png}"
[[ "$(uname -s)" == "Darwin" && -n "$CMUX" ]] || {
  echo "ghostty-evidence: macOS cmux/Ghostty is required" >&2
  exit 77
}
[[ -x "$ROOT/target/debug/carina-ui" ]] || {
  echo "ghostty-evidence: build target/debug/carina-ui first" >&2
  exit 1
}

WORK="$(mktemp -d /tmp/carina-ghostty-evidence.XXXXXX)"
SOCKET="$WORK/carina.sock"
WORKSPACE="$WORK/workspace"
IMAGE="$WORK/carina-symbol.png"
WINDOW=""
SERVER_PID=""
UI_PID=""
cleanup() {
  if [[ -z "$UI_PID" && -f "$WORK/ui.pid" ]]; then
    read -r UI_PID < "$WORK/ui.pid" || UI_PID=""
  fi
  [[ -n "$WINDOW" ]] && "$CMUX" close-window --window "$WINDOW" >/dev/null 2>&1 || true
  if [[ "$UI_PID" =~ ^[0-9]+$ ]]; then
    kill "$UI_PID" >/dev/null 2>&1 || true
    for _ in $(seq 1 20); do
      kill -0 "$UI_PID" >/dev/null 2>&1 || break
      sleep 0.05
    done
    kill -9 "$UI_PID" >/dev/null 2>&1 || true
  fi
  [[ -n "$SERVER_PID" ]] && kill "$SERVER_PID" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
find_cmux_window() {
  swift -e 'import CoreGraphics; let xs=CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID)! as! [[String:Any]]; let cmux=xs.filter{($0[kCGWindowOwnerName as String] as? String)?.lowercased()=="cmux"}; let named=cmux.first{($0[kCGWindowName as String] as? String)?.contains("Carina Graphics Evidence")==true}; let best=named ?? cmux.max{a,b in let aa=(a[kCGWindowBounds as String] as? [String:CGFloat] ?? [:]); let bb=(b[kCGWindowBounds as String] as? [String:CGFloat] ?? [:]); return (aa["Width",default:0]*aa["Height",default:0]) < (bb["Width",default:0]*bb["Height",default:0])}; if let x=best { print(x[kCGWindowNumber as String] as! Int) }' 2>/dev/null
}
capture_cmux_window() {
  local output="$1"
  local window_number=""
  for _ in $(seq 1 20); do
    window_number="$(find_cmux_window)"
    if [[ -n "$window_number" ]] && screencapture -x -o -l "$window_number" "$output" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  echo "ghostty-evidence: focused cmux window could not be captured" >&2
  return 1
}
trap cleanup EXIT
mkdir -p "$WORKSPACE" "$(dirname "$OUTPUT")"
cp "$ROOT/docs/brand/assets/logo/raster/carina-symbol.png" "$IMAGE"

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
                result = {
                    "runtime_version":"ghostty-evidence",
                    "protocol_version":"1.3.0",
                    "projection_version":"1.0.0",
                    "capabilities":{"rpc_methods":[
                        "execution.start", "model.list", "session.create",
                        "session.events.stream", "session.list"
                    ]}
                }
            elif method == "model.list":
                result = {"default_model":"test/model","reasoner":{"backend":"test","available":True},"providers":[{"id":"test","name":"Test","registered":True,"available":True,"models":[{"id":"test/model","display_id":"gpt-5.5","name":"Test Model","available":True,"reasoning":True,"default_reasoning_effort":"high","image_input":True}]}]}
            elif method == "session.list":
                result = [{"session_id":"sess_graphics","workspace_root":workspace,"status":"active","next_model":"test/model","next_reasoning_effort":"high","execution_status":"ready"}]
            elif method == "session.resume":
                result = {"session_id":"sess_graphics","workspace_root":workspace,"status":"active","next_model":"test/model","next_reasoning_effort":"high","execution_status":"ready"}
            elif method == "session.items": result = []
            elif method == "history.recent": result = {"entries":[],"count":0,"scope":"workspace"}
            elif method == "artifact.upload":
                content = base64.b64decode(params.get("content_base64", ""))
                if not content.startswith(b"\x89PNG"):
                    if params.get("chunk_index") == 0:
                        send(stream,{"jsonrpc":"2.0","id":request_id,"error":{"code":-32602,"message":"expected PNG"}}); continue
                if not params.get("final"):
                    result = {"upload_id":params["upload_id"],"next_chunk_index":params["chunk_index"] + 1}
                else:
                    result = {"artifact_id":params["sha256"],"media_type":"image/png","bytes":params["total_bytes"],"origin":params.get("origin","")}
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
[[ -S "$SOCKET" ]] || { echo "ghostty-evidence: fake daemon did not start" >&2; exit 1; }

WINDOW="$(CMUX_QUIET=1 "$CMUX" new-window | awk '{print $2}')"
workspace_result="$(CMUX_QUIET=1 "$CMUX" new-workspace --window "$WINDOW" --name "Carina Graphics Evidence" --cwd "$WORKSPACE" --focus true \
  --command "env -u TMUX -u NO_COLOR CARINA_TERMINAL_GRAPHICS=kitty sh -c 'env > \"$WORK/terminal.env\"; echo \$\$ > \"$WORK/ui.pid\"; exec \"$ROOT/target/debug/carina-ui\" --socket \"$SOCKET\" --workspace \"$WORKSPACE\" --session sess_graphics --locale en'")"
WORKSPACE_ID="$(awk '{print $2}' <<<"$workspace_result")"
for _ in $(seq 1 100); do
  tree="$($CMUX tree --window "$WINDOW" --workspace "$WORKSPACE_ID" --id-format both 2>/dev/null || true)"
  SURFACE="$(sed -n 's/.*surface:[0-9][0-9]* \([A-F0-9-][A-F0-9-]*\) \[terminal\].*/\1/p' <<<"$tree" | head -1)"
  [[ -n "${SURFACE:-}" ]] && screen="$($CMUX read-screen --surface "$SURFACE" 2>/dev/null || true)" && grep -Fq "Describe the change" <<<"$screen" && break
  sleep 0.1
done
[[ -n "${SURFACE:-}" ]] || { echo "ghostty-evidence: terminal surface not found" >&2; exit 1; }

CMUX_QUIET=1 "$CMUX" focus-window --window "$WINDOW" >/dev/null
sleep 1
capture_cmux_window "$WORK/before.png"

CMUX_QUIET=1 "$CMUX" rpc terminal.paste "{\"surface_id\":\"$SURFACE\",\"text\":$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$IMAGE")}" >/dev/null
for _ in $(seq 1 100); do
  screen="$($CMUX read-screen --surface "$SURFACE" 2>/dev/null || true)"
  grep -Fq "image  carina-symbol.png" <<<"$screen" && break
  sleep 0.1
done
grep -Fq "image  carina-symbol.png" <<<"$screen" || { printf '%s\n' "$screen" >&2; echo "ghostty-evidence: image chip did not render" >&2; exit 1; }
if grep -Fq "Format      image/png" <<<"$screen"; then
  printf '%s\n' "$screen" >&2
  grep -E '^(CARINA_TERMINAL_GRAPHICS|TERM|TERM_PROGRAM|TMUX|NO_COLOR)=' "$WORK/terminal.env" >&2 || true
  echo "ghostty-evidence: Ghostty rendered the text fallback instead of Kitty pixels" >&2
  exit 1
fi

CMUX_QUIET=1 "$CMUX" focus-window --window "$WINDOW" >/dev/null
sleep 1
rm -f "$OUTPUT"
capture_cmux_window "$OUTPUT"
python3 - "$OUTPUT" <<'PY'
import struct, sys
data=open(sys.argv[1],"rb").read()
if data[:8] != b"\x89PNG\r\n\x1a\n": raise SystemExit("evidence is not PNG")
width,height=struct.unpack(">II",data[16:24])
if width < 640 or height < 400 or len(data) < 20000: raise SystemExit(f"implausible screenshot {width}x{height}, {len(data)} bytes")
print(f"ghostty-evidence: captured {width}x{height} ({len(data)} bytes) -> {sys.argv[1]}")
PY
swift - "$WORK/before.png" "$OUTPUT" <<'SWIFT'
import CoreGraphics
import Foundation
import ImageIO

func rgba(_ path: String) -> (Int, Int, [UInt8]) {
    let url = URL(fileURLWithPath: path) as CFURL
    guard let source = CGImageSourceCreateWithURL(url, nil),
          let image = CGImageSourceCreateImageAtIndex(source, 0, nil) else {
        fatalError("cannot decode screenshot: \(path)")
    }
    let width = image.width, height = image.height
    var bytes = [UInt8](repeating: 0, count: width * height * 4)
    let colorSpace = CGColorSpaceCreateDeviceRGB()
    bytes.withUnsafeMutableBytes { raw in
        let context = CGContext(data: raw.baseAddress, width: width, height: height,
            bitsPerComponent: 8, bytesPerRow: width * 4, space: colorSpace,
            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
        context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
    }
    return (width, height, bytes)
}

let before = rgba(CommandLine.arguments[1])
let after = rgba(CommandLine.arguments[2])
var changed = 0
if before.0 == after.0 && before.1 == after.1 {
    for offset in stride(from: 0, to: before.2.count, by: 4) {
        let delta = abs(Int(before.2[offset]) - Int(after.2[offset]))
            + abs(Int(before.2[offset + 1]) - Int(after.2[offset + 1]))
            + abs(Int(before.2[offset + 2]) - Int(after.2[offset + 2]))
        if delta >= 24 { changed += 1 }
    }
    guard changed >= 4_000 else {
        fatalError("preview did not create a material pixel change: \(changed) pixels")
    }
}

let width = after.0, height = after.1, pixels = after.2
var rose = [Bool](repeating: false, count: width * height)
for index in 0..<(width * height) {
    let offset = index * 4
    let red = Int(pixels[offset]), green = Int(pixels[offset + 1]), blue = Int(pixels[offset + 2])
    rose[index] = red >= 90 && red >= green + 25 && red >= blue + 12
}
var visited = [Bool](repeating: false, count: rose.count)
var largest = (count: 0, minX: 0, maxX: 0, minY: 0, maxY: 0)
for seed in rose.indices where rose[seed] && !visited[seed] {
    var stack = [seed], count = 0
    var minX = width, maxX = 0, minY = height, maxY = 0
    visited[seed] = true
    while let index = stack.popLast() {
        let x = index % width, y = index / width
        count += 1
        minX = min(minX, x); maxX = max(maxX, x)
        minY = min(minY, y); maxY = max(maxY, y)
        if x > 0 && rose[index - 1] && !visited[index - 1] {
            visited[index - 1] = true; stack.append(index - 1)
        }
        if x + 1 < width && rose[index + 1] && !visited[index + 1] {
            visited[index + 1] = true; stack.append(index + 1)
        }
        if y > 0 && rose[index - width] && !visited[index - width] {
            visited[index - width] = true; stack.append(index - width)
        }
        if y + 1 < height && rose[index + width] && !visited[index + width] {
            visited[index + width] = true; stack.append(index + width)
        }
    }
    if count > largest.count {
        largest = (count, minX, maxX, minY, maxY)
    }
}
let markWidth = largest.maxX - largest.minX + 1
let markHeight = largest.maxY - largest.minY + 1
let markRatio = Double(markWidth) / Double(markHeight)
guard largest.count >= 5_000, (0.88...1.12).contains(markRatio) else {
    fatalError("preview mark aspect ratio regressed: \(markWidth)x\(markHeight), ratio \(markRatio), \(largest.count) rose pixels")
}
let formattedRatio = String(format: "%.3f", markRatio)
let geometry = "\(before.0)x\(before.1) -> \(after.0)x\(after.1)"
print("ghostty-evidence: window \(geometry); mark \(markWidth)x\(markHeight) (ratio \(formattedRatio))")
SWIFT
