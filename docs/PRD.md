# PRD：Pi Agent Runtime 重写方案

## 1. 项目名称

**Pi Agent OS Runtime**(内部代号:**Carina**)

一句话定位:一个面向 Coding Agent / General Agent 的本地优先、安全可控、可扩展、可远程调度的 Agent Runtime。

核心技术路线:**Go Control Plane + Rust Capability Kernel + Zig Native Toolchain + LLM Agent Surface**

## 2. 背景

当前多数 Coding Agent 工具本质上是:`LLM + prompt + tool calls + shell + file edit`。这种形态足够灵活,但存在几个根本问题:

1. **权限边界弱** — Agent 可以读写文件、执行命令、访问网络、读取环境变量,很多安全依赖用户确认或容器隔离。
2. **状态不可重放** — Agent 执行了哪些步骤、哪个 patch 是何时产生的、命令是否成功、上下文是否过期,通常缺少强事件模型。
3. **工具层不统一** — grep、diff、patch、shell、git、test runner 依赖宿主系统,跨平台行为不一致。
4. **扩展和执行耦合** — 插件/工具扩展往往与主进程权限混在一起,插件可以间接获得过高系统权限。
5. **单机 CLI 上限明显** — 很多 Agent 工具适合个人交互,但难以自然演化为团队级 daemon、远程 worker、CI agent、审计系统。

因此,本项目目标不是简单"用 Go/Rust/Zig 重写 Pi",而是将 Pi 重新定义为:**Agent Runtime / Agent OS**。

## 3. 产品目标

### 3.1 核心目标

1. 本地极快执行
2. 强权限边界
3. 可重放事件流
4. 原子化代码修改
5. 多 Agent 并发调度
6. 可远程执行
7. 可审计、可回滚、可扩展

### 3.2 非目标

本阶段不追求:完整 IDE、云端 SaaS 产品、通用聊天机器人、自研大模型、复杂知识库/RAG 平台、完整项目管理系统。

本项目的边界是:**Agent Runtime,不是 Agent App**。它应该能被 CLI、TUI、IDE 插件、CI、Web UI、第三方 Agent 框架调用。

## 4. 核心判断

本项目采用三语言分层,不采用单语言全量重写。

- **Go = Control Plane**:daemon、RPC、session manager、scheduler、worker pool、remote execution、observability、model routing、task queue。→ 让 Agent 规模化运行。
- **Rust = Capability Kernel**:permission system、sandbox broker、policy engine、event-sourced state、transactional patch engine、WASM plugin runtime、audit log、rollback system。→ 让 Agent 可信。
- **Zig = Native Toolchain**:file scan、grep、diff primitive、patch primitive、process wrapper、pty、cross-platform shell adapter、fast diagnostics collector。→ 让 Agent 贴近系统、高速、低依赖。
- **Agent Surface**(可先保留 TypeScript):LLM interaction、prompt templates、skills、user commands、extension orchestration、high-level agent loop、UX/TUI。→ 让 Agent 好用、好改、好扩展。

## 5. 总体架构

```
┌─────────────────────────────────────────────┐
│              Agent Surface                  │
│  CLI / TUI / IDE / SDK / Prompt / Skills    │
└──────────────────────┬──────────────────────┘
                       │ JSON-RPC / gRPC
┌──────────────────────▼──────────────────────┐
│              Go Control Plane               │
│ daemon / scheduler / sessions / workers     │
│ model router / remote exec / observability  │
└──────────────────────┬──────────────────────┘
                       │ Capability API
┌──────────────────────▼──────────────────────┐
│            Rust Capability Kernel           │
│ policy / permission / audit / sandbox       │
│ transaction patch / event log / WASM plugins│
└──────────────────────┬──────────────────────┘
                       │ Native Tool Calls
┌──────────────────────▼──────────────────────┐
│              Zig Native Toolchain           │
│ scan / grep / diff / patch / pty / runner   │
└─────────────────────────────────────────────┘
```

### 5.2 核心原则

1. Agent 不直接接触系统资源
2. 所有副作用都必须经过 Capability Kernel
3. 所有执行都必须写入 Event Log
4. 所有 patch 都必须可预览、可验证、可回滚
5. 所有工具能力都必须显式声明权限
6. 默认本地优先,远程能力作为扩展
7. CLI 是客户端,不是核心 runtime

## 6. 用户画像

1. **个人高级开发者** — 需要更强的 Claude Code / Codex CLI 替代品。关注:速度、本地执行、代码修改质量、低延迟、可控性。
2. **团队工程负责人** — 需要团队级 Agent Runtime。关注:审计、权限、远程执行、CI 集成、任务队列、多人协作。
3. **Agent 框架开发者** — 需要可嵌入的 Agent 执行内核。关注:SDK、RPC、插件系统、沙箱、工具调用标准化。
4. **企业安全团队** — 需要可控、可审计、可限制的 AI 代码执行环境。关注:文件访问边界、命令执行策略、网络访问策略、secret 保护、日志追踪、回滚机制。

## 7. 产品形态

### 7.1 CLI

```
carina run "fix failing tests"
carina ask "explain this repo"
carina edit "refactor auth middleware"
carina plan "migrate this package to async"
carina audit last
pi rollback last
carina status
```

### 7.2 Daemon

`carina daemon start | status | stop` — 长期 session、多任务调度、后台索引、远程 worker、RPC 服务。

### 7.3 RPC / SDK

供 IDE、CI、Web UI、其他 Agent 调用:`create_session`、`submit_task`、`stream_events`、`approve_action`、`apply_patch`、`rollback_transaction`、`query_workspace`。

### 7.4 Worker

`carina worker join --server https://carina.internal` — 远程机器执行、CI runner、多 repo agent pool、企业内部 agent 集群。

## 8. 功能需求

### 8.1 Session System

每个 Agent 任务必须运行在明确 session 中。功能:创建/恢复/暂停/终止/导出/回放 session。

Session 包含:session_id、workspace_id、user_id、task_id、model config、permission profile、event log、file snapshots、patch transactions、command history、approval history。

**验收标准**:任意任务执行后可完整查看事件流;任意 session 可恢复上下文继续执行;session 不依赖 CLI 进程存活;session 可导出为 JSONL / SQLite bundle。

### 8.2 Event Log

所有 Agent 行为必须可审计、可回放。事件类型:

```
TaskCreated  ModelRequested  ModelResponded  ToolRequested  ToolApproved
ToolDenied  FileRead  FileWriteProposed  PatchProposed  PatchApplied
PatchFailed  CommandStarted  CommandOutput  CommandExited  NetworkRequested
SecretRequested  PolicyViolation  RollbackStarted  RollbackCompleted  SessionClosed
```

要求:append-only;每个事件有 timestamp 和 session_id;每个副作用事件关联 permission decision;事件可流式输出给 CLI/TUI/IDE。

**验收标准**:可通过 event log 重建任务执行过程;可查询某文件被哪个 agent 在何时修改;可查询某命令为何被允许或拒绝;可生成审计报告。

### 8.3 Capability Kernel

Agent 不直接操作系统资源,所有能力通过 capability 暴露。

Capability 类型:`FileRead FileWrite CommandExec NetworkAccess SecretRead GitOperation PatchApply ProcessSpawn PluginLoad RemoteExecute`

内置权限 Profile:`read-only`、`safe-edit`、`full-workspace`、`ci-runner`、`sandboxed`、`trusted-local`、`enterprise-restricted`。

```
read-only:
- allow FileRead within workspace
- deny FileWrite / CommandExec / NetworkAccess / SecretRead

safe-edit:
- allow FileRead within workspace
- allow FileWrite only through PatchApply
- allow CommandExec only from allowlist
- deny SecretRead
- network requires approval

ci-runner:
- allow test/build commands
- deny arbitrary shell
- deny SecretRead unless explicitly scoped
```

**验收标准**:Agent 无法绕过 Kernel 直接写文件;所有 command execution / network access 必须经过 policy;policy violation 必须被记录;用户可自定义 permission profile。

### 8.4 Transactional Patch Engine

代码修改必须事务化、可验证、可回滚。

Patch 生命周期:`Proposed → Validated → Approved → Applied → Verified → Committed | RolledBack | Failed`

功能:多文件 patch proposal、conflict detection、pre-apply validation、post-apply verification、atomic apply、rollback、patch provenance、human-readable diff。

Patch 元数据:patch_id、session_id、agent_step_id、affected_files、base_hash、new_hash、diff、reason、risk_level、approval_status、test_status、rollback_pointer。

**验收标准**:patch 应用失败时不得产生半修改状态;应用后可一键 rollback;必须记录由哪个 task 生成;必须能展示 human-readable diff;并发 patch 修改同一文件时必须检测冲突。

### 8.5 Zig Native Toolchain

工具列表:`carina-scan carina-grep carina-diff carina-patch carina-run carina-pty pi-json pi-tree pi-stat pi-watch`

- **carina-scan**:快速扫描 workspace 文件树;ignore rules、大小限制、binary detection、language detection。
- **carina-grep**:快速文本搜索;regex、glob、context lines、结构化 JSON 输出。
- **carina-diff**:结构化 diff;line diff、file-level diff、rename detection。
- **carina-patch**:应用/验证/回滚/dry-run patch。
- **carina-run**:执行命令;捕获 stdout/stderr、timeout、cwd、env allowlist。
- **carina-pty**:交互式 terminal session;流式输出、resize、kill。

**验收标准**:所有工具输出机器可读 JSON;跨平台行为一致;支持 timeout;不能绕过 Kernel policy;工具调用延迟显著低于 shell pipeline。

### 8.6 Go Control Plane

模块:daemon、session manager、task scheduler、worker pool、RPC server、model router、event streamer、remote executor、workspace manager、observability collector。

- **Daemon**:启动/停止/健康检查;管理本地 runtime 状态;维护后台索引与 session;提供 RPC 接口。
- **Scheduler**:任务排队/取消/暂停/恢复;优先级调度;多 agent 并发。
- **Worker Pool**:local / remote / ci / sandbox worker。
- **Model Router**:统一模型调用接口;provider fallback;rate limit;token usage log;streaming response。

**验收标准**:CLI 退出后 session 仍存在;多任务并发;可通过 RPC 订阅事件流;daemon 崩溃后可恢复未完成 session;所有任务状态可查询。

### 8.7 Plugin Runtime

插件类型:Command / Tool / Model Provider / Prompt / Policy / UI / Workflow Plugin。执行方式:优先 WASM。插件只能通过 capability API 访问资源。

```toml
name = "example-plugin"
version = "0.1.0"
[permissions]
file_read = ["workspace"]
file_write = ["patch_only"]
command_exec = ["npm test", "pytest"]
network = ["api.example.com"]
secret = []
```

**验收标准**:插件不能直接访问宿主文件系统/环境变量/shell;权限必须在安装时展示;运行行为必须进入 event log。

### 8.8 Command Execution

执行流程:Agent proposes → Kernel checks policy → User/auto approves → Go scheduler assigns worker → Zig runner executes → Event log streams output → Kernel records result。

命令风险分级与默认策略:

| Level | 类别 | 默认策略 |
|-------|------|----------|
| 0 | read-only commands | auto allow |
| 1 | test/build/lint | auto allow under safe profile |
| 2 | package install | require approval |
| 3 | file mutation commands | require approval |
| 4 | network / deployment / credential-related | deny or require explicit profile |
| 5 | destructive command | deny by default |

**验收标准**:`rm -rf` 类默认拒绝;`curl | sh` 默认拒绝;package install 默认需确认;test/lint 在 safe profile 自动执行;所有命令输出可流式查看。

### 8.9 Workspace System

Workspace 元数据:workspace_id、root_path、git_repo、allowed_paths、ignored_paths、language_profile、index_status、permission_profile。

功能:初始化/扫描 workspace;设置 allowed/ignored paths;读取 git 状态;生成 repo map;后台增量索引。

**验收标准**:Agent 默认不能访问 workspace 外文件;symbolic link 不能绕过权限;ignored 文件不进入上下文;大文件默认跳过;workspace 配置可提交到仓库。

## 9. 数据模型

```json
// Task
{"task_id":"task_123","session_id":"sess_123","workspace_id":"ws_123",
 "status":"running","user_prompt":"fix failing tests",
 "created_at":"...","updated_at":"...","risk_level":2}

// Event
{"event_id":"evt_123","session_id":"sess_123","task_id":"task_123",
 "type":"CommandStarted","timestamp":"...","payload":{},
 "permission_decision_id":"perm_123"}

// Permission Decision
{"decision_id":"perm_123","capability":"CommandExec","requested_by":"agent",
 "resource":"npm test","decision":"allowed",
 "reason":"safe-edit profile allows test commands","policy_id":"policy_safe_edit"}

// Patch Transaction
{"patch_id":"patch_123","session_id":"sess_123","status":"applied",
 "affected_files":["src/auth.ts"],"base_hash":"...","new_hash":"...",
 "rollback_pointer":"snapshot_123"}
```

## 10. API 设计

- **Session API**:CreateSession GetSession ListSessions PauseSession ResumeSession CloseSession ExportSession ReplaySession
- **Task API**:SubmitTask CancelTask GetTaskStatus StreamTaskEvents ApproveAction DenyAction
- **Workspace API**:OpenWorkspace ScanWorkspace GetWorkspaceTree SearchWorkspace GetFile ProposePatch ApplyPatch RollbackPatch
- **Capability API**:RequestFileRead RequestFileWrite RequestCommandExec RequestNetworkAccess RequestSecretRead RequestPatchApply
- **Worker API**:RegisterWorker HeartbeatWorker AssignTask StreamWorkerOutput RevokeWorker

## 11. CLI 设计

```
# 基础
carina init | carina run "..." | carina ask "..." | carina edit "..." | carina plan "..."
carina status | pi sessions | pi resume <session>

# 审计
carina audit | carina audit session <id> | carina audit file src/auth.ts | carina audit command

# Patch
carina patch list | carina patch show <id> | carina patch apply <id> | carina patch rollback <id>

# Daemon
carina daemon start | stop | status | logs

# Worker
carina worker start | carina worker join <server> | carina worker status
```

## 12. 安全模型

### 12.1 默认安全原则

最小权限;workspace 外不可访问;secret 默认不可读;network 默认受限;destructive command 默认拒绝;patch 必须事务化;插件默认无权限。

### 12.2 高风险行为(默认需要人工确认)

删除大量文件、修改 lockfile、安装依赖、执行远程脚本、访问 secret、访问 workspace 外文件、推送代码、部署命令、访问网络、修改 CI/CD 配置。

### 12.3 Secret 处理

Agent 不直接读取 env;secret 通过 broker 暴露;secret 只能以 handle 形式传递;event log 不记录 secret 原文;command output 自动 redaction。

## 13. 性能目标

| 指标 | 目标 |
|------|------|
| CLI 冷启动 / 热启动 | < 100ms / < 30ms |
| Workspace scan 10k / 100k files | < 1s / < 5s |
| Grep 中型 / 大型 repo | < 300ms / < 2s |
| Patch apply 单文件 / 多文件 | < 50ms / < 300ms |
| Event streaming 端到端 | < 100ms |
| Daemon 异常退出恢复 | < 3s |

## 14. 里程碑

### Phase 0:技术验证

证明三语言边界可行。交付:Go daemon prototype、Rust capability kernel prototype、Zig grep/scan/patch prototype、JSON-RPC 协议草案、单 workspace demo。

**验收**:可以通过 CLI 发起任务,经过 Go 调度、Rust 授权、Zig 读取文件,并返回结果。

### Phase 1:MVP

可用的本地 Coding Agent Runtime。交付:carina init、carina run、session system、event log、file read/search、command execution、patch proposal/apply/rollback、safe-edit permission profile。

**验收**:可以在真实 repo 中完成:读代码、找 bug、修改文件、跑测试、回滚修改、查看审计记录。

### Phase 2:安全内核增强

交付:policy engine、capability graph、custom permission profile、command risk classifier、secret broker、plugin sandbox MVP、audit report。

**验收**:可以阻止 Agent 访问 workspace 外文件、读取 secret、执行危险命令,并给出清晰原因。

### Phase 3:Daemon / Worker / Remote

交付:long-running daemon、task queue、worker pool、remote worker、RPC SDK、CI runner mode、observability dashboard basic API。

**验收**:本机提交任务,远程 worker 执行,流式返回事件、diff、测试结果。

### Phase 4:Plugin / Ecosystem

交付:WASM plugin runtime、plugin manifest、plugin permission review、command/tool/model provider/workflow plugin。

**验收**:第三方插件可安装、声明权限、运行,并被 runtime 限制在授权范围内。

### Phase 5:Enterprise Hardening

交付:team policy、centralized audit、role-based approval、remote worker fleet、SSO integration interface、policy bundle、offline mode、signed plugin。

**验收**:企业可内部部署 Pi Runtime,并对所有 Agent 行为进行审计和限制。

## 15. 技术边界

- **通信**:MVP 用 JSON-RPC over stdio / unix socket;后续 gRPC / Cap'n Proto / FlatBuffers。不过早优化协议。
- **存储**:MVP 用 SQLite + JSONL event log + file snapshots;后续 RocksDB / DuckDB / append-only log store / content-addressed storage。
- **插件**:MVP 用 WASM plugin;后续 WASI capability model、signed plugin package、remote plugin registry。

## 16. 风险

1. **复杂度风险**(三语言构建/调试复杂)→ 严格模块边界、单一 RPC 协议、每层独立测试、MVP 先跑通闭环。
2. **过度工程风险** → CLI 体验优先、daemon 可选、默认本地单机可用、安全能力内置但不打扰。
3. **性能收益不明显**(LLM 延迟 >> 本地工具延迟)→ 重点强调安全、审计、回滚、可重放;性能服务于本地工具体验。
4. **安全模型太重** → permission profile、command risk level、allowlist、session-scoped approval、dry-run preview。

## 17. 成功指标

- **开发者体验**:5 分钟内完成安装和首次任务;常见 repo 中稳定完成 bugfix;patch 可读、可控、可回滚;CLI 延迟低于现有 Node 工具链体感。
- **安全**:workspace 外访问默认拦截率 100%;secret 原文泄露到 event log 次数为 0;高风险命令默认拦截率 100%;所有副作用均有 audit event。
- **稳定性**:daemon 异常退出后可恢复 session;patch 应用失败不产生半修改状态;worker 掉线不导致 event log 损坏。
- **扩展性**:插件可声明权限、不能越权、行为可审计;新 tool 可通过 manifest 注册。

## 18. MVP 最小闭环

```
用户输入任务
↓ Go daemon 创建 session
↓ Agent Surface 调用模型
↓ 模型请求读文件 → Rust Kernel 检查 FileRead 权限 → Zig Toolchain 扫描/读取/搜索
↓ 模型生成 patch → Rust Kernel 创建 PatchTransaction → 用户确认 → Zig carina-patch 应用
↓ Go daemon 运行测试命令 → Rust Kernel 检查 CommandExec 权限 → Zig carina-run 执行
↓ Event Log 记录全过程 → 用户查看结果 → 用户可 rollback
```

MVP 命令:`carina init`、`carina run "fix failing tests"`、`carina patch show`、`carina patch rollback`、`carina audit`。

MVP 只支持:本地 workspace、单用户、单 daemon、safe-edit profile、基础 patch rollback、基础 event log、基础 command allowlist。

## 19. 仓库结构

```
carina/
  apps/        carina-cli/ carina-tui/ carina-daemon/
  crates/      carina-kernel/ carina-policy/ carina-patch/ carina-audit/ carina-plugin-runtime/
  zig/         carina-scan/ carina-grep/ carina-diff/ carina-patch-native/ carina-run/ carina-pty/
  go/          daemon/ scheduler/ worker/ rpc/ model-router/ session-store/
  sdk/         typescript/ python/ go/
  protocol/    jsonrpc/ schemas/ events/ capabilities/
  docs/        architecture.md security-model.md plugin-model.md rpc-api.md
```

## 20. 技术选型

- **Go**:daemon、scheduler、worker pool、RPC server、model router、session manager
- **Rust**:policy engine、capability kernel、event model、transactional patch engine、WASM runtime、audit system、secret broker
- **Zig**:native file scan/grep/diff/patch、process runner、pty、cross-platform helpers
- **Storage**:MVP 用 SQLite + JSONL
- **Protocol**:MVP 用 JSON-RPC;后续 gRPC / Cap'n Proto

## 21. 一句话总结

本项目不是"重写 Pi",而是把 Pi 从 **LLM CLI Tool** 升级为 **Agent OS Runtime**。

> Go makes it run.
> Rust makes it safe.
> Zig makes it sharp.
> LLM makes it useful.
