# TUI session lifecycle

Carina's TUI keeps task recovery and session navigation as separate commands:

- `/new` creates a session for the current workspace and switches to it.
- `/resume` opens the historical session picker. `/resume <session_id>` resumes
  and switches directly.
- `/fork [task_id]` creates a child session from a completed task checkpoint,
  persists the parent/task/turn lineage, and switches to the child.
- `/task-resume [task_id]` resumes a task restored from a checkpoint. The older
  `/resume task_*` form remains a compatibility alias and reports its new name.

Bare `carina` renders its connection state before it starts or attaches the
current workspace runtime. Session selection stays inside that workspace's
state root: an unacknowledged submission is preferred, then the last active
session, then the most recent recoverable session reported by that runtime. A
new session is created only when no suitable current-workspace session exists.

`/resume` lists only sessions owned by the attached workspace runtime. Press
Tab to explicitly enter the all-project browser. That browser scans passive
runtime registry metadata without starting projects, then starts and validates
only the selected workspace runtime before it lists that workspace's sessions.
Tab returns to the current-project scope; Esc returns from destination sessions
to the project list.

A session switch is rejected while the current session owns an active task,
submission or retry, queued or unsent draft, pending approval/question,
external editor, or unfinished goal. Drafts and active work are never silently
moved between sessions.

Same-runtime switching interrupts the old event stream and attaches the new
session at cursor zero. Cross-runtime switching first validates and resumes the
destination, acquires its submission lease, establishes both call and stream
connections, and closes the attach/subscribe gap while the source remains
live. Only then does one commit publish the complete runtime/session target and
release the source lease. Validation, attach, or lease failure leaves the
source target and lease unchanged. Session and generation tags fence delayed
events and RPC responses from the old target.

Call and event-stream reconnects re-read the persisted runtime spec but accept
it only when the stable workspace and runtime IDs are unchanged. Every new
connection repeats the identity handshake. A process restart may change the
epoch; a socket that proves a different workspace or runtime fails closed.
Closing the TUI detaches the client and does not cancel background work. The
workspace runtime exits only after there are no connections or durable
obligations for the configured idle grace period.
