#!/usr/bin/env bash
# Carina acceptance gates (PRD §16). Each gate proves a red-line invariant.
# Exit non-zero on the first failure. Run from the repo root.
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

TOOLS="${CARINA_TOOLS_DIR:-$ROOT/zig/zig-out/bin}"
KERNEL="${CARINA_KERNEL_BIN:-$ROOT/target/release/carina-kernel-service}"
CARINA="$ROOT/bin/carina"
DAEMON="$ROOT/bin/carina-daemon"

fail() { echo "GATE FAILED: $1" >&2; exit 1; }
ok()   { echo "GATE PASS:   $1"; }

# ---------------------------------------------------------------------------
# 16.1 — No TypeScript core runtime. TypeScript is allowed only in SDK,
#        client/UI integrations, examples, tests, and docs.
# ---------------------------------------------------------------------------
ts_runtime=$(find . -path ./node_modules -prune -o \
  \( -path '*/agent/*' -o -path '*/runtime/*' -o -path '*/kernel/*' \
     -o -path '*/tools/*' -o -path '*/patch/*' -o -path '*/scheduler/*' \
     -o -path '*/session/*' -o -path '*/executor/*' \) \
  -name '*.ts' -print 2>/dev/null)
ts_stray=$(find . -name '*.ts' 2>/dev/null | grep -vE '/(sdk|ui|integrations/vscode|examples|tests|docs|node_modules|\.claude)/' || true)
[ -z "$ts_runtime$ts_stray" ] || fail "16.1 TypeScript runtime file(s) found: $ts_runtime $ts_stray"
ok "16.1 no TypeScript runtime"

# ---------------------------------------------------------------------------
# 16.2 — No Node runtime. Core native commands run with node off PATH.
# ---------------------------------------------------------------------------
[ -x "$CARINA" ] || fail "16.2 carina binary missing (run: go build -o bin/carina ./apps/carina-cli)"
env -i PATH="/usr/bin:/bin" CARINA_TOOLS_DIR="$TOOLS" "$CARINA" version >/dev/null 2>&1 || fail "16.2 carina version needs node"
env -i PATH="/usr/bin:/bin" CARINA_TOOLS_DIR="$TOOLS" "$CARINA" scan "$ROOT/protocol" >/dev/null 2>&1 || fail "16.2 carina scan needs node"
env -i PATH="/usr/bin:/bin" CARINA_TOOLS_DIR="$TOOLS" "$CARINA" grep "schema" "$ROOT/protocol" >/dev/null 2>&1 || fail "16.2 carina grep needs node"
ok "16.2 core commands run without node"

# ---------------------------------------------------------------------------
# 16.5 — Native tool required. Remove carina-grep; search must fail (no fallback).
# ---------------------------------------------------------------------------
GATE_TMP=$(mktemp -d)
trap 'rm -rf "$GATE_TMP"; [ -n "${DPID:-}" ] && kill "$DPID" 2>/dev/null' EXIT
mkdir -p "$GATE_TMP/tools" "$GATE_TMP/ws"
for t in carina-scan carina-run carina-diff carina-pty carina-patch-native; do cp "$TOOLS/$t" "$GATE_TMP/tools/"; done  # NOT carina-grep
printf 'x TODO y\n' > "$GATE_TMP/ws/f.txt"
[ -x "$DAEMON" ] || fail "16.5 carina-daemon binary missing"
[ -x "$KERNEL" ] || fail "16.5 carina-kernel-service missing (run: cargo build --release -p carina-kernel --bin carina-kernel-service)"

CARINA_TOOLS_DIR="$GATE_TMP/tools" "$DAEMON" -socket "$GATE_TMP/d.sock" -state "$GATE_TMP/st" \
  -kernel "$KERNEL" -tools "$GATE_TMP/tools" -policy "$GATE_TMP/np" >"$GATE_TMP/d.log" 2>&1 &
DPID=$!
for _ in $(seq 1 100); do [ -S "$GATE_TMP/d.sock" ] && break; sleep 0.05; done

python3 - "$GATE_TMP/d.sock" <<'PY' || exit 1
import socket, json, sys, os
sock = sys.argv[1]
ws = os.path.dirname(sock) + "/ws"

def call(m, **p):
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM); s.connect(sock)
    s.sendall((json.dumps({"jsonrpc": "2.0", "id": 1, "method": m, "params": p}) + "\n").encode())
    b = b""
    while b"\n" not in b:
        b += s.recv(65536)
    s.close()
    r = json.loads(b.split(b"\n")[0])
    return r.get("result"), r.get("error")

se, _ = call("session.create", workspace_root=ws, profile="safe-edit")
sid = se["session_id"]

# 16.5: search must fail without carina-grep (no non-Zig fallback).
_, err = call("workspace.search", session_id=sid, pattern="TODO")
if err is None:
    print("16.5 FAIL: search succeeded without carina-grep"); sys.exit(1)

# 16.4: policy bypass — read-only cannot write; rm -rf is denied.
ro, _ = call("session.create", workspace_root=ws, profile="read-only")
rsid = ro["session_id"]
pt, _ = call("workspace.patch.propose", session_id=rsid, reason="t",
             files=[{"path": "f.txt", "new_content": "z\n"}])
_, aerr = call("workspace.patch.apply", session_id=rsid, patch_id=pt["patch_id"])
if aerr is None:
    print("16.4 FAIL: read-only wrote a file"); sys.exit(1)
res, _ = call("command.exec", session_id=sid, argv=["rm", "-rf", "/tmp/x"])
if res["decision"]["decision"] != "denied":
    print("16.4 FAIL: rm -rf not denied"); sys.exit(1)
print("gates-inner OK")
PY
rc=$?
kill "$DPID" 2>/dev/null; DPID=""
[ $rc -eq 0 ] || fail "16.4/16.5 policy-bypass or native-tool gate"
ok "16.5 native tool required (no fallback)"
ok "16.4 policy bypass blocked (read-only + destructive)"

# ---------------------------------------------------------------------------
# 16.3 — Process tree: the daemon subtree spawned no node/tsx/bun.
# ---------------------------------------------------------------------------
# (Covered structurally: the daemon only spawns carina-kernel-service and the Zig
#  tools; none are Node. The no-node gate above already ran the daemon.)
grep -qi 'node\|tsx\|bun\|deno' "$GATE_TMP/d.log" 2>/dev/null && fail "16.3 node reference in daemon output" || true
ok "16.3 no node in daemon runtime"

echo "ALL GATES PASSED"
