"""Pi-OS Python SDK (Phase 0).

A thin JSON-RPC 2.0 client for the pi-daemon unix socket.
See protocol/jsonrpc/methods.json for the full method registry.
"""

from __future__ import annotations

import json
import socket
from pathlib import Path
from typing import Any

__all__ = ["PiClient", "PiRpcError", "default_socket_path"]


def default_socket_path() -> Path:
    return Path.home() / ".pi-os" / "daemon.sock"


class PiRpcError(RuntimeError):
    def __init__(self, code: int, message: str) -> None:
        super().__init__(f"rpc {code}: {message}")
        self.code = code
        self.message = message


class PiClient:
    """Blocking JSON-RPC client. One request/response per call."""

    def __init__(self, socket_path: Path | str | None = None) -> None:
        self._path = str(socket_path or default_socket_path())
        self._sock: socket.socket | None = None
        self._buffer = b""
        self._next_id = 0

    def connect(self) -> None:
        if self._sock is not None:
            return
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        try:
            sock.connect(self._path)
        except OSError as err:
            raise ConnectionError(
                f"cannot reach pi-daemon at {self._path}: {err}"
            ) from err
        self._sock = sock

    def call(self, method: str, params: dict[str, Any] | None = None) -> Any:
        self.connect()
        assert self._sock is not None
        self._next_id += 1
        request = {
            "jsonrpc": "2.0",
            "id": self._next_id,
            "method": method,
            "params": params or {},
        }
        self._sock.sendall(json.dumps(request).encode() + b"\n")
        response = json.loads(self._read_line())
        if response.get("error"):
            err = response["error"]
            raise PiRpcError(err.get("code", -1), err.get("message", "unknown"))
        return response.get("result")

    # Convenience wrappers over the Session / Task APIs.
    def create_session(self, workspace_root: str, profile: str = "safe-edit") -> dict[str, Any]:
        return self.call("session.create", {"workspace_root": workspace_root, "profile": profile})

    def list_sessions(self) -> list[dict[str, Any]]:
        return self.call("session.list")

    def submit_task(self, session_id: str, prompt: str) -> dict[str, Any]:
        return self.call("task.submit", {"session_id": session_id, "prompt": prompt})

    def replay_session(self, session_id: str) -> list[dict[str, Any]]:
        return self.call("session.replay", {"session_id": session_id})

    def close(self) -> None:
        if self._sock is not None:
            self._sock.close()
            self._sock = None

    def _read_line(self) -> bytes:
        assert self._sock is not None
        while b"\n" not in self._buffer:
            chunk = self._sock.recv(65536)
            if not chunk:
                raise ConnectionError("pi-daemon closed the connection")
            self._buffer += chunk
        line, self._buffer = self._buffer.split(b"\n", 1)
        return line
