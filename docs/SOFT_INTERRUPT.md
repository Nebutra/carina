# Soft Interrupt and Steering Contract

Carina has three distinct execution controls. Clients must not present them as
aliases:

| Control | RPC | Effect |
| --- | --- | --- |
| Steer / follow-up | `execution.steer` | Durable message injected at the next turn boundary |
| Soft interrupt | `execution.interrupt` | Pause at the next safe boundary; active tool is allowed to settle |
| Hard cancel | `execution.cancel` | Terminal cancellation; task context and command process group are killed |

## Safe Point

A safe point has no open tool lifecycle. Steering messages are peeked from the
durable queue, appended as pinned user turns, checkpointed, and only then
acknowledged. Soft interrupt uses the same boundary and transitions the run to
`paused`, making it eligible for `execution.resume`.

If a soft interrupt arrives while a command or PTY-backed tool is running, the
RPC reports `active_tool=true`. The daemon does not signal the process. It waits
for `CommandExited` and the matching terminal `ToolCall*` event, checkpoints the
observation, then pauses. This prevents an assistant/tool protocol transcript
with a request but no result.

Hard cancel is the emergency path. On Unix the toolchain places the native
runner in its own process group and sends `SIGKILL` to that group when the task
context is cancelled, so descendants cannot outlive the cancelled run.

## Reconnect

Pending steering entries and soft-interrupt intent are stored in versioned
`runs/<run_id>.control.json` sidecars. `execution.status`, `execution.result`,
and `execution.list` expose `queue_depth` and `soft_interrupt_pending`. Message
bodies are not exposed by list/status projections.

The queue is at-least-once across a crash around checkpoint acknowledgement:
before the checkpoint it remains queued; after acknowledgement its pinned
transcript turn is already durable. Stable `steer_id` values make client retries
idempotent; reusing an ID with different content is rejected.

## PTY Verification

Backend tests cover a long command receiving a soft interrupt and prove every
`ToolCallStarted` has a terminal event before the run pauses. Toolchain Unix
tests separately prove hard cancellation kills the complete child process
group. Real terminal clients should additionally verify that draft preservation
and key chords map to the intended RPC rather than treating Escape as cancel.
