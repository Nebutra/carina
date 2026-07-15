from __future__ import annotations

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


if __name__ == "__main__":
    unittest.main()
