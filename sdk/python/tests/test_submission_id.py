from __future__ import annotations

import base64
import hashlib
import unittest
from typing import Any

from carina_sdk import CarinaClient


class SubmissionIDTests(unittest.TestCase):
    def test_submit_task_forwards_optional_client_submission_id(self) -> None:
        client = object.__new__(CarinaClient)
        captured: dict[str, Any] = {}

        def call(method: str, params: dict[str, Any]) -> dict[str, Any]:
            captured.update(method=method, params=params)
            return {"task_id": "task_1", "client_submission_id": params.get("client_submission_id")}

        client.call = call  # type: ignore[method-assign]
        task = client.submit_task("sess_1", "work", "sdk_request_1")
        self.assertEqual(captured, {
            "method": "task.submit",
            "params": {"session_id": "sess_1", "prompt": "work", "client_submission_id": "sdk_request_1"},
        })
        self.assertEqual(task["client_submission_id"], "sdk_request_1")

    def test_submit_goal_forwards_command_as_string(self) -> None:
        client = object.__new__(CarinaClient)
        captured: dict[str, Any] = {}

        def call(method: str, params: dict[str, Any]) -> dict[str, Any]:
            captured.update(method=method, params=params)
            return {"task_id": "task_1", "status": "queued"}

        client.call = call  # type: ignore[method-assign]
        client.submit_goal("sess_1", "verify", [{"kind": "command_zero_exit", "command": "go test ./..."}])
        criterion = captured["params"]["success_criteria"][0]
        self.assertEqual(criterion["command"], "go test ./...")
        self.assertIsInstance(criterion["command"], str)

    def test_submit_task_forwards_media_refs(self) -> None:
        client = object.__new__(CarinaClient)
        captured: dict[str, Any] = {}
        ref = {"artifact_id": "a" * 64, "media_type": "image/png", "bytes": 3, "origin": "paste"}

        def call(method: str, params: dict[str, Any]) -> dict[str, Any]:
            captured.update(method=method, params=params)
            return {"task_id": "task_1", "input_media_refs": params.get("input_media_refs")}

        client.call = call  # type: ignore[method-assign]
        task = client.submit_task("sess_1", "work", input_media_refs=[ref])
        self.assertEqual(captured["params"]["input_media_refs"], [ref])
        self.assertEqual(task["input_media_refs"], [ref])

    def test_upload_artifact_sends_ordered_chunks(self) -> None:
        client = object.__new__(CarinaClient)
        content = b"x" * (512 * 1024 + 7)
        digest = hashlib.sha256(content).hexdigest()
        calls: list[dict[str, Any]] = []

        def call(method: str, params: dict[str, Any]) -> dict[str, Any]:
            calls.append({"method": method, "params": params})
            if not params["final"]:
                return {"upload_id": params["upload_id"], "next_chunk_index": params["chunk_index"] + 1}
            return {"artifact_id": digest, "media_type": "image/png", "bytes": len(content), "origin": "paste"}

        client.call = call  # type: ignore[method-assign]
        ref = client.upload_artifact("sess_1", "upload_1", "image/png", "paste", content)
        self.assertEqual(len(calls), 2)
        self.assertEqual([
            (item["params"]["chunk_index"], item["params"]["final"], len(base64.b64decode(item["params"]["content_base64"])))
            for item in calls
        ], [(0, False, 512 * 1024), (1, True, 7)])
        self.assertEqual(ref["artifact_id"], digest)


if __name__ == "__main__":
    unittest.main()
