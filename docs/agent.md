# Using Carina as a Coding Agent

Carina is not just a runtime — it drives a real ReAct coding agent. The model
**only decides**; every side effect is authorized by the Rust capability
kernel and executed by the Zig toolchain, and the whole run is a
tamper-evident audit trail you can replay and roll back.

```
Claude (decides)  →  Go agent loop  →  Rust kernel (authorizes)  →  Zig tools (execute)
     ▲                                                                      │
     └───────────────────────── observation ◀──────────────────────────────┘
```

## The loop

Each turn the reasoner emits one JSON action; carina runs it and feeds back an
observation:

| Action | Goes through | Runs on |
|--------|-------------|---------|
| `{"tool":"list"}` | FileRead capability | Zig `carina-scan` |
| `{"tool":"read","path":"…"}` | FileRead capability | kernel-gated read |
| `{"tool":"search","pattern":"…"}` | FileRead capability | Zig `carina-grep` |
| `{"tool":"run","command":["…"]}` | CommandExec capability (risk-classified) | Zig `carina-run` |
| `{"tool":"patch","path":"…","content":"…"}` | PatchApply capability | Rust transaction → Zig `carina-patch-native` |
| `{"tool":"done","summary":"…"}` | — | ends the task |

Destructive commands (`rm -rf`, `curl … | sh`) are **denied** before they run;
risky ones (installs) are surfaced for approval. Secret files (`.env`, `.ssh`)
are refused. Every file the agent edits is a rollbackable patch transaction.

## The reasoner (model backend)

`go/daemon/reasoner.go` defines the `Reasoner` interface (a pure "think"
step). Two implementations:

- **claude-cli** — uses the local `claude` binary in headless mode
  (`claude -p … --allowedTools "" `) as a pure inference engine, running in an
  isolated empty cwd so it cannot touch the workspace. This works with **CC
  Switch / gateway setups that only admit the Claude Code client** (e.g. the
  Mox gateway), because the request comes from the real `claude` binary. The
  agent's actual file/command/patch work happens in carina, not in Claude Code.
- **scripted** — replays fixed decisions; used by tests to drive the full loop
  deterministically with no model and no cost.

The daemon wires `claude-cli` automatically when the binary is present and the
daemon is not in `--offline` mode. Set `PI_REASONER_MODEL` to pin a model.

## Run it

```bash
# start the runtime (reasoner auto-wired if `claude` is on PATH)
carina-daemon -tools ./zig/zig-out/bin -kernel ./bin/carina-kernel-service &

cd your-repo
carina run "fix the failing test in parser.go"     # agent works autonomously
carina audit <session>        # replay every model decision + kernel-gated effect
carina audit verify <session> # confirm the hash chain wasn't tampered with
carina patch list <session>   # every edit, rollbackable
carina patch rollback <id>    # undo an edit
```

## Verified

- `TestAgentLoopExecutesThroughKernel` — scripted list→read→patch→run→done
  actually edits a file through the kernel; audit chain verifies.
- `TestAgentLoopBlocksDestructive` — the agent cannot `rm -rf` even when asked.
- Live: Claude over the Mox gateway autonomously fixed a Python function
  (`list → read → patch → verify → done`), every effect kernel-authorized, the
  18-event chain verified, and the edit rolled back cleanly.
