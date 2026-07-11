"""Blocking Carina JSON-RPC SDK compatible with Runtime 0.6.1."""

from __future__ import annotations

import json
import socket
import threading
import time
from collections import deque
from pathlib import Path
from typing import Any, Iterator, TypedDict

__version__ = "0.2.0"
compatible_runtime_version = "0.6.1"
_stream_queue_limit = 64
__all__ = [
    "CarinaClient",
    "CarinaRpcError",
    "CarinaStreamOverflow",
    "CarinaThread",
    "SessionAttachment",
    "UsageCostReport",
    "compatible_runtime_version",
    "default_socket_path",
]


class CarinaEvent(TypedDict, total=False):
    event_id: str
    session_id: str
    task_id: str
    type: str
    timestamp: str
    payload: dict[str, Any]


SessionAttachment = TypedDict(
    "SessionAttachment",
    {"events": list[CarinaEvent], "from": int, "cursor": int},
)


class UsageCostReport(TypedDict):
    providers: list[dict[str, Any]]
    totals: dict[str, Any]
    estimated: bool


def default_socket_path() -> Path:
    return Path.home() / ".carina" / "daemon.sock"


class CarinaRpcError(RuntimeError):
    def __init__(self, code: int, message: str) -> None:
        super().__init__(f"rpc {code}: {message}")
        self.code = code
        self.message = message


class CarinaStreamOverflow(RuntimeError):
    pass


class CarinaClient:
    """Thread-safe blocking client with bounded calls and event notifications."""

    def __init__(
        self,
        socket_path: Path | str | None = None,
        timeout: float = 15.0,
    ) -> None:
        if timeout <= 0:
            raise ValueError("timeout must be positive")
        self._path = str(socket_path or default_socket_path())
        self._timeout = timeout
        self._sock: socket.socket | None = None
        self._buffer = b""
        self._next_id = 0
        self._lock = threading.Lock()
        self._notifications: deque[tuple[str, Any]] = deque(maxlen=_stream_queue_limit)
        self._notification_lock = threading.Lock()
        self._next_listener_id = 0
        self._session_notifications: dict[int, dict[str, Any]] = {}

    def connect(self) -> None:
        if self._sock is not None:
            return
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.settimeout(self._timeout)
        try:
            sock.connect(self._path)
        except OSError as err:
            sock.close()
            raise ConnectionError(
                f"cannot reach carina-daemon at {self._path}: {err}"
            ) from err
        self._sock = sock

    def call(self, method: str, params: dict[str, Any] | None = None) -> Any:
        with self._lock:
            self.connect()
            assert self._sock is not None
            self._next_id += 1
            request_id = self._next_id
            request = {
                "jsonrpc": "2.0",
                "id": request_id,
                "method": method,
                "params": params or {},
            }
            try:
                self._sock.sendall(json.dumps(request).encode() + b"\n")
                while True:
                    response = self._read_message()
                    if "id" not in response and response.get("method"):
                        self._dispatch_notification(response["method"], response.get("params"))
                        continue
                    if response.get("id") != request_id:
                        continue
                    if response.get("error"):
                        err = response["error"]
                        raise CarinaRpcError(err.get("code", -1), err.get("message", "unknown"))
                    return response.get("result")
            except socket.timeout as err:
                self._disconnect()
                raise TimeoutError(f"rpc {method} timed out after {self._timeout}s") from err
            except OSError as err:
                self._disconnect()
                raise ConnectionError(f"rpc {method}: carina-daemon disconnected: {err}") from err

    def create_session(self, workspace_root: str, profile: str = "safe-edit") -> dict[str, Any]:
        return self.call("session.create", {"workspace_root": workspace_root, "profile": profile})

    def get_session(self, session_id: str) -> dict[str, Any]:
        return self.call("session.get", {"session_id": session_id})

    def start_thread(self, working_directory: str, profile: str = "safe-edit") -> CarinaThread:
        self.initialize()
        return CarinaThread(self, self.create_session(working_directory, profile))

    def resume_thread(self, session_id: str) -> CarinaThread:
        self.initialize()
        return CarinaThread(self, self.get_session(session_id))

    def fork_thread(self, session_id: str, last_task_id: str | None = None, through_turn: int | None = None) -> CarinaThread:
        self.initialize(); params: dict[str, Any] = {"session_id": session_id}
        if last_task_id: params["last_task_id"] = last_task_id
        if through_turn: params["through_turn"] = through_turn
        return CarinaThread(self, self.call("session.fork", params))

    def list_sessions(self) -> list[dict[str, Any]]:
        return self.call("session.list")

    def submit_task(self, session_id: str, prompt: str) -> dict[str, Any]:
        return self.call("task.submit", {"session_id": session_id, "prompt": prompt})

    def submit_goal(self, session_id: str, prompt: str, success_criteria: list[dict[str, Any]]) -> dict[str, Any]:
        return self.call("task.submit", {"session_id": session_id, "prompt": prompt, "success_criteria": success_criteria})

    def replay_session(self, session_id: str) -> list[CarinaEvent]:
        return self.call("session.replay", {"session_id": session_id})

    def attach_session(self, session_id: str, since: int = 0) -> dict[str, Any]:
        return self.call("session.attach", {"session_id": session_id, "since": since})

    def fork_session(self, session_id: str) -> dict[str, Any]:
        return self.call("session.fork", {"session_id": session_id})

    def cost(self, session_id: str | None = None, task_id: str | None = None) -> UsageCostReport:
        params: dict[str, Any] = {}
        if session_id:
            params["session_id"] = session_id
        if task_id:
            params["task_id"] = task_id
        return self.call("usage.cost", params)

    def steer_task(self, task_id: str, message: str) -> dict[str, Any]:
        return self.call("task.steer", {"task_id": task_id, "message": message})

    def answer_question(self, question_id: str, value: str) -> dict[str, Any]:
        return self.call("task.user.answer", {"question_id": question_id, "value": value})

    def list_workflows(self) -> list[dict[str, Any]]:
        return self.call("workflow.list")

    def initialize(self, client_name: str = "carina-sdk", client_version: str = __version__) -> dict[str, Any]:
        return self.call("runtime.initialize", {"protocol_version": "1.2.0", "schema_version": "1.2.0", "client_name": client_name, "client_version": client_version})

    def workflow_detail(self, run_id: str) -> dict[str, Any]:
        return self.call("workflow.detail", {"run_id": run_id})

    def run_workflow(self, session_id: str, workflow: str, input: str = "") -> dict[str, Any]:
        return self.call("workflow.run", {"session_id": session_id, "workflow": workflow, "input": input})

    def pause_workflow(self, run_id: str) -> dict[str, Any]:
        return self.call("workflow.pause", {"run_id": run_id})

    def resume_workflow(self, run_id: str) -> dict[str, Any]:
        return self.call("workflow.resume", {"run_id": run_id})

    def stop_workflow(self, run_id: str) -> dict[str, Any]:
        return self.call("workflow.stop", {"run_id": run_id})

    def restart_workflow(self, run_id: str) -> dict[str, Any]:
        return self.call("workflow.restart", {"run_id": run_id})

    def list_workers(self) -> list[dict[str, Any]]:
        return self.call("worker.list")

    def resolve_approval(self, decision_id: str, allow: bool, approver: str = "", scope: str = "once") -> Any:
        return self.call("task.approval.resolve", {"decision_id": decision_id, "allow": allow, "approver": approver, "scope": scope})

    def doctor(self) -> dict[str, Any]:
        return self.call("daemon.doctor")

    def list_agents(self, workspace_root: str = "") -> dict[str, Any]:
        return self.call("agent.list", {"workspace_root": workspace_root})

    def agent_view(self) -> dict[str, list[dict[str, Any]]]:
        return self.call("agent.view")

    def list_checkpoints(self, session_id: str) -> list[dict[str, Any]]:
        return self.call("session.checkpoint.list", {"session_id": session_id})

    def preview_checkpoint(self, session_id: str, checkpoint_id: str) -> dict[str, Any]:
        return self.call("session.checkpoint.preview", {"session_id": session_id, "checkpoint_id": checkpoint_id})

    def summarize_checkpoint(self, session_id: str, checkpoint_id: str) -> dict[str, Any]:
        return self.call("session.checkpoint.summarize", {"session_id": session_id, "checkpoint_id": checkpoint_id})

    def restore_checkpoint(self, session_id: str, checkpoint_id: str, confirmed: bool = False) -> dict[str, Any]:
        return self.call("session.checkpoint.restore", {"session_id": session_id, "checkpoint_id": checkpoint_id, "confirmed": confirmed})

    def inject_channel_event(self, event: dict[str, Any], signature: str) -> dict[str, Any]:
        return self.call("channel.event.inject", {"event": event, "signature": signature})

    def list_extensions(self) -> dict[str, Any]:
        return self.call("extension.list")

    def set_extension_enabled(self, name: str, enabled: bool) -> dict[str, Any]:
        return self.call("extension.enable" if enabled else "extension.disable", {"name": name})

    def subscribe_session_events(self, session_id: str) -> str:
        result = self.call("session.events.stream", {"session_id": session_id}) or {}
        return str(result.get("subscription_id", ""))

    def unsubscribe_session_events(self, subscription_id: str) -> None:
        self.call("session.events.unsubscribe", {"subscription_id": subscription_id})

    def read_notification(self) -> tuple[str, Any]:
        with self._notification_lock:
            if self._notifications:
                return self._notifications.popleft()
        with self._lock:
            self.connect()
            while True:
                message = self._read_message()
                if "id" not in message and message.get("method"):
                    self._dispatch_notification(message["method"], message.get("params"))
                    with self._notification_lock:
                        return self._notifications.popleft()

    def stream_session_events(self, session_id: str) -> Iterator[CarinaEvent]:
        listener_id = self._add_session_listener(session_id)
        subscription_id = ""
        try:
            subscription_id = self.subscribe_session_events(session_id)
            while True:
                events = self._drain_session_listener(listener_id)
                if events:
                    yield from events
                    continue
                with self._lock:
                    self.connect(); message = self._read_message()
                    if "id" not in message and message.get("method"):
                        self._dispatch_notification(message["method"], message.get("params"))
        finally:
            self._remove_session_listener(listener_id)
            if subscription_id:
                try:
                    self.unsubscribe_session_events(subscription_id)
                except Exception:
                    pass

    def _dispatch_notification(self, method: str, params: Any) -> None:
        with self._notification_lock:
            self._notifications.append((method, params))
            if method == "event" and isinstance(params, dict):
                session_id = params.get("session_id")
                for stream in self._session_notifications.values():
                    if stream["session_id"] != session_id or stream["overflow"]:
                        continue
                    queue: deque[CarinaEvent] = stream["queue"]
                    if len(queue) >= _stream_queue_limit:
                        stream["overflow"] = True
                    else:
                        queue.append(params)

    def _add_session_listener(self, session_id: str) -> int:
        with self._notification_lock:
            self._next_listener_id += 1
            listener_id = self._next_listener_id
            self._session_notifications[listener_id] = {
                "session_id": session_id,
                "queue": deque(),
                "overflow": False,
            }
            return listener_id

    def _remove_session_listener(self, listener_id: int) -> None:
        with self._notification_lock:
            self._session_notifications.pop(listener_id, None)

    def _drain_session_listener(self, listener_id: int) -> list[CarinaEvent]:
        with self._notification_lock:
            entry = self._session_notifications.get(listener_id)
            if entry is None:
                return []
            queue: deque[CarinaEvent] = entry["queue"]
            events = list(queue)
            queue.clear()
            overflow = entry["overflow"]
            if overflow:
                entry["overflow"] = False
                raise CarinaStreamOverflow("Carina event stream overflow")
            return events

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
        sock = self._sock
        self._sock = None
        if sock is not None:
            try:
                sock.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            sock.close()

    def _disconnect(self) -> None:
        sock = self._sock
        self._sock = None
        if sock is not None:
            sock.close()

    def _read_message(self) -> dict[str, Any]:
        assert self._sock is not None
        while b"\n" not in self._buffer:
            chunk = self._sock.recv(65536)
            if not chunk:
                raise ConnectionResetError("carina-daemon closed the connection")
            self._buffer += chunk
        line, self._buffer = self._buffer.split(b"\n", 1)
        return json.loads(line)


class CarinaThread:
    def __init__(self, client: CarinaClient, session: dict[str, Any]) -> None:
        self.client, self.session = client, session

    def fork(self, last_task_id: str | None = None, through_turn: int | None = None) -> CarinaThread:
        return self.client.fork_thread(self.session["session_id"], last_task_id, through_turn)

    def run(self, prompt: str, *, output_schema: dict[str, Any] | None = None, cancel: threading.Event | None = None, poll_interval: float = .05) -> dict[str, Any]:
        params: dict[str, Any] = {"session_id": self.session["session_id"], "prompt": prompt}
        if output_schema is not None: params["output_schema"] = output_schema
        task = self.client.call("task.submit", params); task_id = task["task_id"]
        while True:
            if cancel is not None and cancel.is_set():
                self.client.call("task.cancel", {"task_id": task_id})
                raise InterruptedError("Carina run cancelled")
            current = self.client.call("task.result", {"task_id": task_id})
            if current["status"] in {"completed", "degraded", "failed", "cancelled", "needs_input"}:
                result: dict[str, Any] = {"task": current, "final_response": current.get("summary", "")}
                if output_schema is not None:
                    try: result["structured_output"] = json.loads(result["final_response"])
                    except json.JSONDecodeError: pass
                return result
            time.sleep(poll_interval)

    def run_streamed(self, prompt: str, **options: Any) -> Iterator[dict[str, Any]]:
        output_schema = options.get("output_schema")
        cancel = options.get("cancel")
        poll_interval = options.get("poll_interval", .05)
        session_id = self.session["session_id"]
        listener_id = self.client._add_session_listener(session_id)
        subscription_id = ""
        task_id = ""
        try:
            subscription_id = self.client.subscribe_session_events(session_id)
            params: dict[str, Any] = {"session_id": self.session["session_id"], "prompt": prompt}
            if output_schema is not None:
                params["output_schema"] = output_schema
            task = self.client.call("task.submit", params)
            task_id = task["task_id"]
            while True:
                for event in self.client._drain_session_listener(listener_id):
                    yield {"type": "event", "event": event}
                if cancel is not None and cancel.is_set():
                    self.client.call("task.cancel", {"task_id": task_id})
                    raise InterruptedError("Carina run cancelled")
                current = self.client.call("task.result", {"task_id": task_id})
                if current["status"] in {"completed", "degraded", "failed", "cancelled", "needs_input"}:
                    for event in self.client._drain_session_listener(listener_id):
                        yield {"type": "event", "event": event}
                    result: dict[str, Any] = {"task": current, "final_response": current.get("summary", "")}
                    if output_schema is not None:
                        try:
                            result["structured_output"] = json.loads(result["final_response"])
                        except json.JSONDecodeError:
                            pass
                    yield {"type": "turn.completed", "result": result}
                    return
                time.sleep(poll_interval)
        finally:
            self.client._remove_session_listener(listener_id)
            if subscription_id:
                try:
                    self.client.unsubscribe_session_events(subscription_id)
                except Exception:
                    pass
