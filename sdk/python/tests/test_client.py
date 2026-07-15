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

from carina_sdk import CarinaClient, CarinaRpcError, CarinaStreamOverflow, CarinaThread, compatible_runtime_version


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
    def test_resolve_approval_uses_canonical_approve_param(self) -> None:
        captured: list[dict[str, Any]] = []

        def handler(request: dict[str, Any], conn: socket.socket) -> None:
            captured.append(request["params"])
            conn.sendall(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": {"resolved": True}}).encode() + b"\n")

        with daemon_server(handler) as path:
            client = CarinaClient(path, timeout=.5)
            client.resolve_approval("decision-1", True, "sdk", "once")
            client.close()
        self.assertTrue(captured[0]["approve"])
        self.assertNotIn("allow", captured[0])

    def test_session_listener_dispatch_is_independent_and_bounded(self) -> None:
        client = CarinaClient(timeout=.5)
        first = client._add_session_listener("s")
        second = client._add_session_listener("s")
        other = client._add_session_listener("other")
        client._dispatch_notification("event", {"session_id": "s", "type": "one"})
        self.assertEqual([event["type"] for event in client._drain_session_listener(first)], ["one"])
        self.assertEqual([event["type"] for event in client._drain_session_listener(second)], ["one"])
        self.assertEqual(client._drain_session_listener(other), [])

        client._remove_session_listener(first)
        client._dispatch_notification("event", {"session_id": "s", "type": "two"})
        self.assertEqual([event["type"] for event in client._drain_session_listener(second)], ["two"])

        for index in range(65):
            client._dispatch_notification("event", {"session_id": "other", "type": "burst", "index": index})
        with self.assertRaises(CarinaStreamOverflow):
            client._drain_session_listener(other)

    def test_typed_parity_and_stream(self) -> None:
        methods: list[str] = []

        def handler(request: dict[str, Any], conn: socket.socket) -> None:
            methods.append(request["method"])
            result: Any = {}
            if request["method"] == "session.attach":
                result = {"events": [], "from": request["params"]["since"], "cursor": 7}
            elif request["method"] == "session.fork":
                result = {"session_id": "child"}
            elif request["method"] == "session.review":
                result={"session_id":"s1","projection_version":"1.0.0","source_cursor":"cp1.payload.signature","state":"completed","intent":"ship","success_criteria":[{"kind":"command"}],"changes":[],"commands":[],"tools":[],"checks":[],"diagnostics":[],"policy_decisions":[],"questions":[],"conflicts":[],"risk_and_policy":[],"artifact_ids":[],"rollback":{"available":False,"patch_ids":[]},"stats":{}}
            elif request["method"] == "session.items":
                result={"data":[{"type":"turn.started","session_id":"s1","task_id":"t1"}],"next_cursor":"cp1.payload.signature","projection_version":"1.0.0"}
            elif request["method"] == "usage.cost":
                result = {"providers": [], "totals": {}, "estimated": False}
            elif request["method"] == "task.steer":
                result = {"queued": True, "task_id": "t1", "status": "running"}
            elif request["method"] == "task.resume":
                result = {"task_id": "t1", "session_id": "s1", "status": "running"}
            elif request["method"] == "session.checkpoint.preview":
                result = {"checkpoint":{"checkpoint_id":"t1:1:9","created_at":"2026-07-14T00:00:00Z","sequence":"00000000000000000009","task_id":"t1","session_id":"s1","turn":1,"applied_patches":[]},"conversation_turns":1,"rollback_patches":[],"will_resume":"paused"}
            elif request["method"] == "session.checkpoint.summarize":
                result = {"checkpoint_id":"t1:1:9","task_id":"t1","turn":1,"recent":[]}
            elif request["method"] == "session.checkpoint.restore":
                result = {"restored":True,"checkpoint_id":"t1:1:9","task_id":"t1","turn":1,"rolled_back":[],"status":"paused","idempotent":True,"reconciliation_required":False,"journal_cleanup_pending":False}
            elif request["method"] == "task.user.answer":
                result = {"question_id": "q1", "accepted": True, "value": "yes"}
            elif request["method"] == "session.events.stream":
                result = {"subscription_id": "sub-1"}
            conn.sendall(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}).encode() + b"\n")
            if request["method"] == "session.events.stream":
                conn.sendall(json.dumps({
                    "jsonrpc": "2.0",
                    "method": "event",
                    "params": {"session_id": "s1", "type": "ModelResponded", "timestamp": "now"},
                }).encode() + b"\n")

        with daemon_server(handler) as path:
            client = CarinaClient(path, timeout=0.5)
            self.assertEqual(compatible_runtime_version, "0.6.2")
            self.assertEqual(client.attach_session("s1", 3)["cursor"], 7)
            self.assertEqual(client.fork_session("s1")["session_id"], "child")
            self.assertEqual(client.review_session("s1")["projection_version"], "1.0.0")
            self.assertEqual(client.list_session_items("s1",limit=1)["data"][0]["task_id"],"t1")
            self.assertFalse(client.cost("s1")["estimated"])
            self.assertTrue(client.steer_task("t1", "continue")["queued"])
            self.assertEqual(client.resume_task("t1")["status"], "running")
            self.assertEqual(client.preview_checkpoint("s1", "t1:1:9")["checkpoint"]["sequence"], "00000000000000000009")
            self.assertEqual(client.summarize_checkpoint("s1", "t1:1:9")["turn"], 1)
            self.assertTrue(client.restore_checkpoint("s1", "t1:1:9", True)["idempotent"])
            self.assertTrue(client.answer_question("q1", "yes")["accepted"])
            client.subscribe_session_events("s1")
            method, event = client.read_notification()
            self.assertEqual(method, "event")
            self.assertEqual(event["type"], "ModelResponded")
            client.close()

        self.assertEqual(methods, [
            "session.attach", "session.fork", "session.review", "session.items", "usage.cost", "task.steer", "task.resume",
            "session.checkpoint.preview", "session.checkpoint.summarize", "session.checkpoint.restore",
            "task.user.answer", "session.events.stream",
        ])

    def test_event_subscription_preserves_raw_cursor_contract(self) -> None:
        captured: list[dict[str, Any]] = []
        def handler(request: dict[str, Any], conn: socket.socket) -> None:
            captured.append(request["params"])
            result = {"subscription_id":"sub","cursor":17,"replayed":2,"event_mode":"canonical"}
            conn.sendall(json.dumps({"jsonrpc":"2.0","id":request["id"],"result":result}).encode()+b"\n")
        with daemon_server(handler) as path:
            client=CarinaClient(path,timeout=.5)
            result=client.subscribe_session_events_from("s",11,"canonical")
            self.assertEqual(result["cursor"],17)
            self.assertEqual(result["event_mode"],"canonical")
            client.close()
        self.assertEqual(captured[0]["since"],11)

    def test_run_streamed_emits_authoritative_events_and_unsubscribes(self) -> None:
        methods: list[str] = []
        def handler(request: dict[str, Any], conn: socket.socket) -> None:
            methods.append(request["method"])
            result: Any = {}
            if request["method"] == "session.events.stream":
                result = {"subscription_id": "sub-1"}
            elif request["method"] == "task.submit":
                result = {"task_id": "t", "session_id": "s", "status": "queued"}
            elif request["method"] == "task.result":
                conn.sendall(json.dumps({
                    "jsonrpc": "2.0", "method": "event",
                    "params": {"session_id": "s", "task_id": "t", "type": "ModelResponded", "timestamp": "now"},
                }).encode() + b"\n")
                result = {"task_id": "t", "session_id": "s", "status": "completed", "summary": "ok"}
            conn.sendall(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}).encode() + b"\n")
        with daemon_server(handler) as path:
            client = CarinaClient(path, timeout=.5)
            thread = CarinaThread(client, {"session_id": "s"})
            streamed = list(thread.run_streamed("go", poll_interval=.001))
            client.close()
        self.assertEqual([item["type"] for item in streamed], ["event", "turn.completed"])
        self.assertEqual(streamed[0]["event"]["type"], "ModelResponded")
        self.assertEqual(methods, ["session.events.stream", "task.submit", "task.result", "session.events.unsubscribe"])

    def test_run_streamed_cancellation_cancels_task_and_unsubscribes(self) -> None:
        methods: list[str] = []
        def handler(request: dict[str, Any], conn: socket.socket) -> None:
            methods.append(request["method"])
            result: Any = {"subscription_id": "sub-cancel"} if request["method"] == "session.events.stream" else {}
            if request["method"] == "task.submit":
                result = {"task_id": "t", "session_id": "s", "status": "queued"}
            conn.sendall(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}).encode() + b"\n")
        with daemon_server(handler) as path:
            client = CarinaClient(path, timeout=.5)
            cancelled = threading.Event(); cancelled.set()
            with self.assertRaises(InterruptedError):
                list(CarinaThread(client, {"session_id": "s"}).run_streamed("go", cancel=cancelled, poll_interval=.001))
            client.close()
        self.assertEqual(methods, ["session.events.stream", "task.submit", "task.cancel", "session.events.unsubscribe"])

    def test_concurrent_run_streamed_routes_by_session_and_cleans_up(self) -> None:
        methods: list[tuple[str, str]] = []
        def handler(request: dict[str, Any], conn: socket.socket) -> None:
            params=request.get("params",{});session=params.get("session_id","");result:Any={}
            if request["method"]=="session.events.stream":result={"subscription_id":"sub-"+session}
            elif request["method"]=="task.submit":result={"task_id":"task-"+session,"session_id":session,"status":"queued"}
            elif request["method"]=="task.result":
                session=params["task_id"].removeprefix("task-");conn.sendall(json.dumps({"jsonrpc":"2.0","method":"event","params":{"session_id":session,"task_id":params["task_id"],"type":"ModelResponded"}}).encode()+b"\n");result={"task_id":params["task_id"],"session_id":session,"status":"completed","summary":session}
            elif request["method"]=="session.events.unsubscribe":session=params["subscription_id"].removeprefix("sub-")
            methods.append((request["method"],session));conn.sendall(json.dumps({"jsonrpc":"2.0","id":request["id"],"result":result}).encode()+b"\n")
        with daemon_server(handler) as path:
            client=CarinaClient(path,timeout=.5);results:dict[str,list[dict[str,Any]]]={}
            def consume(session:str)->None:results[session]=list(CarinaThread(client,{"session_id":session}).run_streamed("go",poll_interval=.001))
            workers=[threading.Thread(target=consume,args=(session,)) for session in ("s1","s2")]
            for worker in workers:worker.start()
            for worker in workers:worker.join(timeout=2)
            client.close()
        self.assertTrue(all(not worker.is_alive() for worker in workers));self.assertEqual(results["s1"][0]["event"]["session_id"],"s1");self.assertEqual(results["s2"][0]["event"]["session_id"],"s2");self.assertIn(("session.events.unsubscribe","s1"),methods);self.assertIn(("session.events.unsubscribe","s2"),methods)

    def test_session_listener_preserves_other_notifications(self) -> None:
        client=CarinaClient("/unused");listener=client._add_session_listener("s1");client._dispatch_notification("event",{"session_id":"s2","type":"Other"});client._dispatch_notification("event",{"session_id":"s1","type":"Mine"});self.assertEqual([event["type"] for event in client._drain_session_listener(listener)],["Mine"]);self.assertEqual(client.read_notification()[1]["session_id"],"s2");self.assertEqual(client.read_notification()[1]["session_id"],"s1");client._remove_session_listener(listener)

    def test_disconnect_fails_pending_call(self) -> None:
        def handler(_request: dict[str, Any], conn: socket.socket) -> None:
            conn.shutdown(socket.SHUT_RDWR)

        with daemon_server(handler) as path:
            client = CarinaClient(path, timeout=5)
            started = time.monotonic()
            with self.assertRaises(ConnectionError):
                client.call("daemon.status")
            self.assertLess(time.monotonic() - started, 1)

    def test_typed_cursor_recovery_survives_transport(self) -> None:
        def handler(request:dict[str,Any],conn:socket.socket)->None:conn.sendall(json.dumps({"jsonrpc":"2.0","id":request["id"],"error":{"code":-32010,"message":"cursor_expired","data":{"code":"cursor_expired","projection_version":"1.0.0","recovery":"snapshot","snapshot_method":"session.items","earliest_cursor":"cp1.x.y"}}}).encode()+b"\n")
        with daemon_server(handler) as path:
            client=CarinaClient(path,timeout=.5)
            with self.assertRaises(CarinaRpcError) as raised:client.list_session_items("s","bad",1)
            self.assertEqual(raised.exception.code,-32010);self.assertEqual(raised.exception.data["code"],"cursor_expired");client.close()

    def test_timeout_bounds_unresponsive_daemon(self) -> None:
        with daemon_server(lambda _request, _conn: None) as path:
            client = CarinaClient(path, timeout=0.03)
            with self.assertRaises(TimeoutError):
                client.call("daemon.status")

    def test_high_level_thread_run(self) -> None:
        submitted: list[dict[str, Any]] = []
        def handler(request: dict[str, Any], conn: socket.socket) -> None:
            result: Any = {}
            if request["method"] == "runtime.initialize": result = {"runtime_version":"0.6.2","protocol_version":"1.2.0","projection_version":"1.0.0","capabilities":{"tool_call_lifecycle":True,"event_schema_version":"0.3.0"}}
            elif request["method"] == "session.create": result = {"session_id":"s","workspace_id":"w","workspace_root":"/tmp","status":"active"}
            elif request["method"] == "task.submit": submitted.append(request["params"]); result = {"task_id":"t","status":"queued"}
            elif request["method"] == "task.result": result = {"task_id":"t","status":"completed","summary":"{\"status\":\"ok\"}"}
            conn.sendall(json.dumps({"jsonrpc":"2.0","id":request["id"],"result":result}).encode()+b"\n")
        with daemon_server(handler) as path:
            client=CarinaClient(path,timeout=.5);thread=client.start_thread("/tmp");schema={"type":"object","properties":{"status":{"type":"string"}},"required":["status"]};result=thread.run("status",output_schema=schema,poll_interval=.001);self.assertEqual(result["structured_output"],{"status":"ok"});client.close()
        self.assertEqual(submitted[0]["output_schema"],schema)

    def test_initialize_rejects_incompatible_protocol_and_missing_capability(self) -> None:
        for result in (
            {"runtime_version":"x","protocol_version":"2.0.0","capabilities":{"tool_call_lifecycle":True}},
            {"runtime_version":"x","protocol_version":"1.2.0","capabilities":{}},
			{"runtime_version":"x","protocol_version":"1.2.0","capabilities":{"tool_call_lifecycle":True,"event_schema_version":"0.4.0"}},
        ):
            def handler(request: dict[str, Any], conn: socket.socket, response=result) -> None:
                conn.sendall(json.dumps({"jsonrpc":"2.0","id":request["id"],"result":response}).encode()+b"\n")
            with daemon_server(handler) as path:
                client = CarinaClient(path, timeout=.5)
                with self.assertRaises(RuntimeError): client.initialize()
                client.close()


if __name__ == "__main__":
    unittest.main()
