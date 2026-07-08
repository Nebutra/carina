# Hermes Memory Absorption For Carina

Source review: `https://github.com/NousResearch/hermes-agent` at commit
`9de9c25`.

Status: implemented as a local governed-memory core. External semantic memory
providers and Nebutra Cloud memory sync remain future, explicitly scoped
extensions.

## Goal

Absorb the durable parts of Hermes' memory architecture without importing its
Python monolith: memory should become a governed Carina runtime subsystem, not
just extra prompt text.

The target behavior is:

- local, inspectable long-term memory for agent notes and user profile facts;
- stable prompt-cache behavior via a frozen per-run snapshot;
- controlled mutation through explicit memory actions;
- ephemeral recall/injection that never becomes transcript history;
- write safety, audit provenance, and Nebutra identity/sync boundaries.

## Design

Implement a first local memory core behind the daemon:

1. `MemoryStore` persists bounded entries under the daemon state directory.
   It has two targets: `memory` for agent/project notes and `user` for user
   profile facts. Entries are append/replace/remove/batch mutated through a
   structured API, not by direct prompt text edits.
2. A frozen prompt snapshot is loaded once per agent run and appended to the
   stable system prompt. Writes during the run persist immediately, but the
   current run's stable prefix is not rebuilt.
3. Agent-visible memory mutation is a native tool action. The model can call
   `{"tool":"memory","action":"add|replace|remove|batch",...}` and receives a
   terminal JSON observation. Writes go through the kernel's `MemoryWrite`
   capability before storage; built-in policy requires approval by default.
   Successful writes are audited with target, scope, action, operation count,
   and content hash.
4. Operator-facing RPC methods expose `memory.list`, `memory.write`, and
   `memory.context`. These are local write/read surfaces and are registered in
   the Gateway descriptor catalog. `memory.write` returns
   `{decision, result?}`; when policy returns `requires_approval`, the write is
   queued and only applied by `task.action.approve`.
5. Memory injection is fenced as recalled context and treated as background
   data, not new user input. This preserves transcript integrity and prevents
   self-amplifying memory pollution.

## Boundaries

This phase does not ship external memory providers, vector search, or Nebutra
Cloud sync. Those belong behind a future provider adapter and Nebutra identity
boundary. The local core intentionally leaves an extension seam for:

- exactly one external memory provider at a time;
- Nebutra-scoped user/workspace/project/session identity;
- future semantic recall indexes.

## Safety

The implementation must:

- reject blank entries and unsupported targets/actions;
- bound memory size and provide batch all-or-nothing semantics;
- reject dangerous persistence phrases that look like prompt injection,
  exfiltration, secret capture, SSH backdoors, or agent config mutation;
- write files atomically;
- never log raw memory text, memory secret values, or hidden system context;
- route memory mutation through the `MemoryWrite` capability so policy bundles
  can require approval or deny it independently of file writes;
- keep current-run prompt cache stable after memory writes.

## Tests

Add focused Go tests for:

- memory store add/replace/remove/batch behavior;
- frozen snapshot remaining stable after writes;
- threat-pattern rejection;
- RPC method surface and descriptors;
- agent prompt injection and native memory tool dispatch.
