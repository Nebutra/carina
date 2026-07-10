from __future__ import annotations

import json
import os
import socket
import tempfile
import threading
import time
import unittest
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Callable, Iterator

from carina_sdk import CarinaClient, compatible_runtime_version


@contextmanager
def daemon_server(
    handler: Callable[[dict[str, Any], socket.socket], None]
) -> Iterator[Path]:
    with tempfile.TemporaryDirectory(prefix="carina-sdk-py-") as directory:
        path = Path(directory) / "daemon.sock"
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        server.bind(str(path))
        server.listen()
        stopped = threading.Event()

        def serve() -> None:
            while not stopped.is_set():
                try:
                    conn, _ = server.accept()
                except OSError:
                    return
                with conn:
                    source = conn.makefile("rb")
                    for line in source:
                        handler(json.loads(line), conn)

        thread = threading.Thread(target=serve, daemon=True)
        thread.start()
        try:
            yield path
        finally:
            stopped.set()
            server.close()
            thread.join(timeout=1)


class ClientTest(unittest.TestCase):
    def test_typed_parity_and_stream(self) -> None:
        methods: list[str] = []

        def handler(request: dict[str, Any], conn: socket.socket) -> None:
            methods.append(request["method"])
            result: Any = {}
            if request["method"] == "session.attach":
                result = {"events": [], "from": request["params"]["since"], "cursor": 7}
            elif request["method"] == "session.fork":
                result = {"session_id": "child"}
            elif request["method"] == "usage.cost":
                result = {"providers": [], "totals": {}, "estimated": False}
            elif request["method"] == "task.steer":
                result = {"queued": True, "task_id": "t1", "status": "running"}
            elif request["method"] == "task.user.answer":
                result = {"question_id": "q1", "accepted": True, "value": "yes"}
            conn.sendall(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}).encode() + b"\n")
            if request["method"] == "session.events.stream":
                conn.sendall(json.dumps({
                    "jsonrpc": "2.0",
                    "method": "event",
                    "params": {"session_id": "s1", "type": "ModelResponded", "timestamp": "now"},
                }).encode() + b"\n")

        with daemon_server(handler) as path:
            client = CarinaClient(path, timeout=0.5)
            self.assertEqual(compatible_runtime_version, "0.6.1")
            self.assertEqual(client.attach_session("s1", 3)["cursor"], 7)
            self.assertEqual(client.fork_session("s1")["session_id"], "child")
            self.assertFalse(client.cost("s1")["estimated"])
            self.assertTrue(client.steer_task("t1", "continue")["queued"])
            self.assertTrue(client.answer_question("q1", "yes")["accepted"])
            client.subscribe_session_events("s1")
            method, event = client.read_notification()
            self.assertEqual(method, "event")
            self.assertEqual(event["type"], "ModelResponded")
            client.close()

        self.assertEqual(methods, [
            "session.attach", "session.fork", "usage.cost", "task.steer",
            "task.user.answer", "session.events.stream",
        ])

    def test_disconnect_fails_pending_call(self) -> None:
        def handler(_request: dict[str, Any], conn: socket.socket) -> None:
            conn.shutdown(socket.SHUT_RDWR)

        with daemon_server(handler) as path:
            client = CarinaClient(path, timeout=5)
            started = time.monotonic()
            with self.assertRaises(ConnectionError):
                client.call("daemon.status")
            self.assertLess(time.monotonic() - started, 1)

    def test_timeout_bounds_unresponsive_daemon(self) -> None:
        with daemon_server(lambda _request, _conn: None) as path:
            client = CarinaClient(path, timeout=0.03)
            with self.assertRaises(TimeoutError):
                client.call("daemon.status")


if __name__ == "__main__":
    unittest.main()
