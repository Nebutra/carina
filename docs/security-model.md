# Security Model

## Default posture

1. Least privilege by default.
2. No access outside the workspace.
3. Secrets unreadable by default.
4. Network restricted by default.
5. Destructive commands denied by default.
6. All patches are transactional.
7. Plugins start with zero permissions.

## Capabilities

Every side effect is one of ten capability types:

```
FileRead  FileWrite  CommandExec  NetworkAccess  SecretRead
GitOperation  PatchApply  ProcessSpawn  PluginLoad  RemoteExecute
```

A capability request carries: requesting principal (agent / plugin / user), resource (path, command, host), session id, and task id. The kernel returns a `PermissionDecision` (allow / deny / require-approval) with the policy that produced it. Every decision is an audit event; side-effect events reference their decision id.

## Permission profiles

Built-in profiles (see `protocol/capabilities/profiles/`):

| Profile | Summary |
|---------|---------|
| `read-only` | FileRead within workspace only; everything else denied |
| `safe-edit` | FileRead in workspace; FileWrite only via PatchApply; CommandExec from allowlist; network requires approval; secrets denied |
| `full-workspace` | Read/write anywhere in workspace; commands up to level 3 with approval |
| `ci-runner` | test/build commands allowed; no arbitrary shell; secrets only when explicitly scoped |
| `sandboxed` | everything mediated through sandbox broker |
| `trusted-local` | relaxed local development profile; still audited |
| `enterprise-restricted` | org policy bundle; central approval required for levels ≥ 2 |

Users can define custom profiles; profiles are session-scoped and recorded in the session metadata.

## Command risk levels

| Level | Class | Default policy |
|-------|-------|----------------|
| 0 | read-only commands | auto allow |
| 1 | test / build / lint | auto allow under `safe-edit` |
| 2 | package install | require approval |
| 3 | file mutation commands | require approval |
| 4 | network / deploy / credential-related | deny, or explicit profile |
| 5 | destructive (`rm -rf`, `curl \| sh`, …) | deny by default |

## High-risk actions (always require human confirmation)

Deleting many files · modifying lockfiles · installing dependencies · executing remote scripts · reading secrets · accessing files outside the workspace · pushing code · deploy commands · network access · modifying CI/CD configuration.

## Workspace boundary

- Agents cannot access paths outside `workspace.allowed_paths`.
- Symbolic links are resolved before policy checks — a symlink cannot escape the boundary.
- Ignored files never enter model context; oversized files are skipped by default.

## Secret handling

1. Agents never read the environment directly.
2. Secrets are exposed through a broker, as opaque handles.
3. Event logs never contain secret plaintext.
4. Command output is auto-redacted against known secret values.

## Audit guarantees

- Event log is append-only; every event has a timestamp and session id.
- Any file's modification history is queryable: which agent, which task, which patch, when.
- Every allow/deny is explainable: the decision records the policy and reason.
- Success metrics: 100% interception of out-of-workspace access, zero secret plaintext in logs, 100% interception of high-risk commands, every side effect has an audit event.
