# TUI session lifecycle

Carina's TUI keeps task recovery and session navigation as separate commands:

- `/new` creates a session for the current workspace and switches to it.
- `/resume` opens the historical session picker. `/resume <session_id>` resumes
  and switches directly.
- `/fork [task_id]` creates a child session from a completed task checkpoint,
  persists the parent/task/turn lineage, and switches to the child.
- `/task-resume [task_id]` resumes a task restored from a checkpoint. The older
  `/resume task_*` form remains a compatibility alias and reports its new name.

A session switch is rejected while the current session owns an active task,
submission or retry, queued or unsent draft, pending approval/question,
external editor, or unfinished goal. Drafts and active work are never silently
moved between sessions.

The connection controller interrupts the old event stream and attaches the new
session at cursor zero. Session and generation tags fence delayed events and RPC
responses from the old session. The TUI acquires the destination submission
lease before releasing the source lease; if the destination is already owned,
the switch is refused and the source lease remains held.
