# Headless stream protocol

Carina exposes one bidirectional NDJSON protocol for SDK and CI integrations:

```sh
carina run --input-format stream-json --output-format stream-json [--session sess_id]
```

Every stdout frame carries `protocol: "carina-stream-json"` and `version: 1`.
Stdout contains no human-oriented progress text in this mode. Each stdin line is
one JSON object; malformed lines produce an `error` frame without terminating
the stream.

## Input frames

- `prompt`: `text`, optional `request_id`, `client_submission_id`, `model`,
  `reasoning_effort`, `agent`, and `mode`.
- `steer`: `task_id`, `text`, and optional `request_id`.
- `approval`: `decision_id`, `decision` (`allow` or `deny`), optional `scope`
  (`once`, `session`, or `project`), and optional `request_id`.
- `answer`: `question_id`, `value`, and optional `request_id`. `value` may be
  free text when a question has no predefined options.
- `interrupt`: `task_id` and optional `request_id`.
- `close`: optional `request_id`; closes the client stream but does not cancel
  daemon-owned tasks.

Unknown fields and unsafe approval scopes are rejected. Provider credentials,
permission grants, and raw secret values are never accepted as stream fields.

## Output frames

- `session`: emitted once after the session is created or resumed.
- `response`: acknowledges one input frame and echoes `request_id`.
- `event`: wraps one canonical daemon event in wire order.
- `control_request`: asks for an approval decision.
- `user_question`: asks for a structured or free-text answer.
- `error`: rejects one input frame and echoes `request_id` when available.
- `closed`: acknowledges a `close` frame.

The command connection and event-stream connection are separate. This preserves
JSON-RPC response ordering while approvals, questions, and task events continue
to arrive during input processing. All effects still pass through the daemon's
existing RPC permission, audit, and idempotency boundaries.
