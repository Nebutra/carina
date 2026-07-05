"""Carina Python SDK (Phase 0).

A thin JSON-RPC 2.0 client for the carina-daemon unix socket.
See protocol/jsonrpc/methods.json for the full method registry.
"""

from __future__ import annotations

import json
import socket
from pathlib import Path
from typing import Any

__all__ = ["PiClient", "PiRpcError", "default_socket_path"]


def default_socket_path() -> Path:
    return Path.home() / ".carina" / "daemon.sock"


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
                f"cannot reach carina-daemon at {self._path}: {err}"
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

    # ---- sessions & tasks ----
    def create_session(self, workspace_root: str, profile: str = "safe-edit") -> dict[str, Any]:
        return self.call("session.create", {"workspace_root": workspace_root, "profile": profile})

    def list_sessions(self) -> list[dict[str, Any]]:
        return self.call("session.list")

    def submit_task(self, session_id: str, prompt: str) -> dict[str, Any]:
        return self.call("task.submit", {"session_id": session_id, "prompt": prompt})

    def replay_session(self, session_id: str) -> list[dict[str, Any]]:
        return self.call("session.replay", {"session_id": session_id})

    # ---- workspace & patches ----
    def search(self, session_id: str, pattern: str) -> list[dict[str, Any]]:
        return self.call("workspace.search", {"session_id": session_id, "pattern": pattern})

    def get_file(self, session_id: str, path: str) -> dict[str, Any]:
        return self.call("workspace.file.get", {"session_id": session_id, "path": path})

    def propose_patch(self, session_id: str, files: list[dict[str, str]], reason: str = "") -> dict[str, Any]:
        return self.call("workspace.patch.propose", {"session_id": session_id, "reason": reason, "files": files})

    def apply_patch(self, session_id: str, patch_id: str) -> dict[str, Any]:
        return self.call("workspace.patch.apply", {"session_id": session_id, "patch_id": patch_id})

    def rollback_patch(self, session_id: str, patch_id: str) -> dict[str, Any]:
        return self.call("workspace.patch.rollback", {"session_id": session_id, "patch_id": patch_id})

    # ---- commands, approvals, audit ----
    def exec(self, session_id: str, argv: list[str], task_id: str | None = None) -> dict[str, Any]:
        params: dict[str, Any] = {"session_id": session_id, "argv": argv}
        if task_id:
            params["task_id"] = task_id
        return self.call("command.exec", params)

    def approve(self, session_id: str, decision_id: str) -> dict[str, Any]:
        return self.call("task.action.approve", {"session_id": session_id, "decision_id": decision_id})

    def deny(self, session_id: str, decision_id: str, reason: str = "denied") -> dict[str, Any]:
        return self.call("task.action.deny", {"session_id": session_id, "decision_id": decision_id, "reason": reason})

    def audit_report(self, session_id: str) -> dict[str, Any]:
        return self.call("audit.report", {"session_id": session_id})

    def close(self) -> None:
        if self._sock is not None:
            self._sock.close()
            self._sock = None

    def _read_line(self) -> bytes:
        assert self._sock is not None
        while b"\n" not in self._buffer:
            chunk = self._sock.recv(65536)
            if not chunk:
                raise ConnectionError("carina-daemon closed the connection")
            self._buffer += chunk
        line, self._buffer = self._buffer.split(b"\n", 1)
        return line
