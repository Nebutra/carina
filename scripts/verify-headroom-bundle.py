#!/usr/bin/env python3
import json
import subprocess
import sys
import threading
from queue import Empty, Queue


def main() -> None:
    if len(sys.argv) != 2:
        raise SystemExit("usage: verify-headroom-bundle.py <headroom-binary>")

    proc = subprocess.Popen(
        [sys.argv[1], "mcp", "serve"],
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        bufsize=1,
    )
    lines: Queue[str] = Queue()

    def read_stdout() -> None:
        assert proc.stdout is not None
        for line in proc.stdout:
            lines.put(line)

    threading.Thread(target=read_stdout, daemon=True).start()

    def request(request_id: int, method: str, params: dict) -> dict:
        assert proc.stdin is not None
        proc.stdin.write(
            json.dumps(
                {
                    "jsonrpc": "2.0",
                    "id": request_id,
                    "method": method,
                    "params": params,
                }
            )
            + "\n"
        )
        proc.stdin.flush()
        while True:
            try:
                message = json.loads(lines.get(timeout=20))
            except Empty as exc:
                stderr = proc.stderr.read() if proc.poll() is not None and proc.stderr else ""
                raise RuntimeError(f"timed out waiting for {method}; stderr={stderr}") from exc
            if message.get("id") == request_id:
                if "error" in message:
                    raise RuntimeError(f"{method} failed: {message['error']}")
                return message["result"]

    try:
        initialized = request(
            1,
            "initialize",
            {
                "protocolVersion": "2025-06-18",
                "capabilities": {},
                "clientInfo": {"name": "carina-release-verifier", "version": "1"},
            },
        )
        if initialized.get("serverInfo", {}).get("name") != "headroom":
            raise RuntimeError(f"unexpected server info: {initialized}")

        assert proc.stdin is not None
        proc.stdin.write(
            json.dumps({"jsonrpc": "2.0", "method": "notifications/initialized"}) + "\n"
        )
        proc.stdin.flush()
        tools = request(2, "tools/list", {})
        names = {tool["name"] for tool in tools.get("tools", [])}
        required = {"headroom_compress", "headroom_retrieve", "headroom_stats"}
        missing = required - names
        if missing:
            raise RuntimeError(f"missing MCP tools: {sorted(missing)}")

        compressed = request(
            3,
            "tools/call",
            {
                "name": "headroom_compress",
                "arguments": {
                    "content": "Carina validates every tool call before execution. " * 120
                },
            },
        )
        if compressed.get("isError"):
            raise RuntimeError(f"headroom_compress failed: {compressed}")
        if not compressed.get("content"):
            raise RuntimeError("headroom_compress returned no content")
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)


if __name__ == "__main__":
    main()
