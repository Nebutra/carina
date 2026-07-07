<div align="center">

<img src="docs/assets/carina-hero.png" alt="Nebutra Carina" width="100%" />

# Nebutra Carina

**本地优先的 Agent Runtime，用来在明确的权限、审计与回滚边界内运行编程智能体。**

[![status](https://img.shields.io/badge/status-alpha-0033FE)](#当前仓库状态)
[![build](https://img.shields.io/badge/build-source%20first-0B7285)](#从源码快速开始)
[![runtime](https://img.shields.io/badge/runtime-local--first-0BF1C3)](#carina-是什么)
[![audit](https://img.shields.io/badge/audit-hash--chained-6D28D9)](#核心概念)
[![license](https://img.shields.io/badge/license-Apache--2.0-informational)](LICENSE)

[English](README.md) · **简体中文** · [日本語](README.ja.md)

</div>

状态：**alpha**。本仓库已经实现核心执行与约束机制，但打包发布、公开安装渠道和部分 UX
仍在早期阶段。CLI 细节和配置格式仍可能变化。

---

## Carina 是什么

Carina 不是编辑器、聊天产品，也不是托管沙箱。它位于 AI 编程智能体和真实机器之间，是一层运行时。

当智能体需要读文件、提出代码修改、执行命令、访问网络、调用插件或使用 secret 时，Carina 会把这些动作送入能力内核。能力内核根据当前策略决定：允许、拒绝，或要求人工审批。被允许的副作用会写入哈希链式审计日志；文件改动会以事务性 patch 的形式应用，并可检查、可回滚。

目标很直接：让智能体能在真实仓库里做有用的事，同时不要把每次 tool call 都变成隐式、不可追踪的机器权限。

## 适合什么场景

Carina 面向的是需要给编程智能体真实执行权限，同时又关心 prompt 发出之后如何约束和追踪的场景。

适合：

- 让智能体在本地仓库中执行任务，同时把写入、命令、网络访问和 secret 放在策略之后。
- 让长任务或后台任务在 CLI 退出之后继续存在。
- 生成可回答“智能体做了什么、何时做、为什么被允许”的事件流。
- 回滚智能体产生的文件改动，而不是只依赖临时的 Git 清理。
- 为 IDE、CI、内部 Agent 平台或工作流引擎提供可复用的执行底座。
- 让子智能体或插件以比父任务更窄的权限运行。

不适合：

- 你只需要编辑器助手或聊天 UI。
- 你想要的是托管式、开箱即用的 Agent 服务。
- 你不需要审计日志、权限边界、回滚或 daemon 会话。
- 你今天就需要稳定的二进制发布版。本阶段从源码构建最可靠。

## 当前仓库状态

当前代码库已实现：

- Go daemon 和 CLI client：会话、任务、调度、JSON-RPC、模型路由、worker 和事件流。
- Rust 能力内核：权限决策、策略执行、事务性 patch、审计日志和插件执行边界。
- Zig 原生工具：scan、grep、diff、patch、命令执行和 pty 原语。
- 内置权限 profile，例如 `read-only`、`safe-edit`、`full-workspace`、`ci-runner` 以及企业场景 profile。
- 可验证的哈希链式 append-only 审计日志。
- patch 的 propose、inspect、apply、rollback 流程。
- ReAct 风格 Agent loop：typed transcript、compaction、loop guard、完成验证和后台任务恢复。
- 子智能体能力衰减：子会话只能获得父权限的子集，不能获得超集。
- 声明式 DAG Agent workflow 编排。
- MCP client 和 server 互通，并通过同一能力边界执行。
- 默认拒绝的 egress proxy、网络 allowlist、daemon 侧凭证注入，以及显式 per-host opt-in 的 HTTPS MITM 凭证注入。
- 通过 broker 处理 secret，而不是把进程环境里的原始 secret 直接暴露给子命令。

尚未完成：

- 公开安装脚本和 Homebrew tap。
- 公开 `SECURITY.md` 和贡献指南。
- 带 provenance 的稳定发布产物。
- 完整打磨的 TUI / dashboard。
- Windows 支持。
- TypeScript、Python、Go SDK 的能力对齐。
- 面向远程 worker / 集群部署的生产运维文档。

## 从源码快速开始

要求：

- Go 1.25 或更新版本
- Rust 1.85 或更新版本
- Zig 0.15.x
- macOS 或 Linux

构建：

```bash
git clone https://github.com/Nebutra/carina
cd carina
make all
```

启动 daemon：

```bash
./bin/carina-daemon &
```

把模型凭证放到 daemon 进程环境中。BYOK API key 优先；配置后 daemon 也支持 Nebutra OAuth 兜底。

```bash
export ANTHROPIC_API_KEY=sk-...
# 或
export OPENAI_API_KEY=sk-...
```

也可以把 BYOK 凭证存到本机，并查看 provider catalog：

```bash
./bin/carina auth login anthropic - < ~/.secrets/anthropic-key
./bin/carina auth login openai - < ~/.secrets/openai-key
./bin/carina auth list
./bin/carina providers list --refresh
```

daemon 启动时读取本地缓存的 catalog。如果希望启动前先刷新 models.dev，可设置
`CARINA_PROVIDER_REFRESH=1`。当前 runtime adapter 覆盖 Anthropic Messages、
OpenAI Responses、catalog 中的 OpenAI-compatible chat provider、OpenRouter 和
Google Gemini。Bedrock、Azure OpenAI、Vertex 这类云身份 provider 还需要单独的
region/project/credential chain 接线。

需要指定运行模型时，可以显式传 `provider/model`：

```bash
CARINA_REASONER_MODEL=openai/gpt-5 ./bin/carina-daemon &
./bin/carina run --model openrouter/anthropic/claude-sonnet-4-5 "fix the failing tests"
```

在当前仓库运行任务：

```bash
./bin/carina run "fix the failing tests and show the patch"
```

查看会话：

```bash
./bin/carina sessions
./bin/carina audit <session_id>
./bin/carina audit verify <session_id>
./bin/carina patch list <session_id>
./bin/carina patch show <session_id> <patch_id>
```

回滚已应用的 patch：

```bash
./bin/carina patch rollback <session_id> <patch_id>
```

## 常见工作流

### 个人仓库任务

日常编程任务可使用 `safe-edit` 或更严格的 profile。智能体可以读取 workspace、提出 patch、运行 allowlist 中的测试/构建命令。危险命令、网络访问和 secret 会根据当前 profile 被拒绝或要求审批。

### 团队或安全审查

使用 audit stream 和 audit export 查看哪些文件被读取、哪些命令被执行、哪些权限决策被作出，以及哪个 patch 修改了文件。哈希链可帮助验证者发现事件历史是否被改动。

### 后台或远程执行

CLI 是 client；daemon 持有运行时状态。会话和后台任务可以在 CLI 退出后继续存在。worker 接口面向本地、远程、CI 或沙箱化执行池。

### 嵌入到其它产品

当 Carina 需要位于其它产品表面之后时，可以使用 JSON-RPC server、SDK 或 MCP server mode，例如 IDE 插件、Web UI、CI workflow 或内部 Agent 平台。

## 核心概念

### 能力边界

Carina 把副作用表示为能力，例如文件读写、命令执行、网络访问、secret 访问、patch apply、插件加载和远程执行。会话的 permission profile 决定请求会被允许、拒绝，还是进入审批。

### 审计日志

每个权限决策和被允许的副作用都会记录为事件。事件被追加到哈希链：每条事件包含上一条事件的哈希。验证过程可以发现被插入、删除或修改的事件。

### 事务性 patch

智能体的文件修改以 patch transaction 表示。patch 可以被提出、检查、应用和回滚。patch 系统的目标是避免半应用状态，并保留每次修改的来源。

### Daemon 会话

daemon 存储会话状态、调度任务、流式输出事件，并协调 worker。这让任务不依赖单个终端进程的生命周期。

### 子智能体能力衰减

任务派生子智能体时，子会话获得衰减后的权限集合。它可以比父任务权限更小，但不能获得父任务没有的权限。

### Egress 与 secret

启用 egress proxy 后，网络默认拒绝。host 必须被策略允许。凭证可以从 daemon 侧 secret 在 egress 边界注入，因此子命令不需要在环境变量中拿到原始 secret。HTTPS 凭证注入需要显式 per-host MITM opt-in，并使用进程局部 trust bundle，不修改系统信任库。

## 架构

Carina 按职责拆分，而不是为了展示技术栈而拆分。

| 层 | 主要职责 | 当前实现 |
|---|---|---|
| Agent surface | ReAct loop、transcript、审批、子智能体、workflow 执行 | Go daemon 与 model-router 集成 |
| Control plane | 会话、调度、JSON-RPC、worker、事件流、egress proxy | Go |
| Capability kernel | 权限决策、策略、事务性 patch、审计链、插件边界 | Rust |
| Native toolchain | 仓库扫描、grep、diff、patch、进程执行、pty | Zig |
| Client surfaces | CLI、TUI、SDK、MCP server/client 集成 | Go 与 SDK 包 |

这个设计把面向模型的 loop 和副作用边界分开。智能体可以请求动作；运行时决定动作能否发生，并记录结果。

更多文档：

- [Architecture](docs/architecture.md)
- [Security model](docs/security-model.md)
- [RPC API](docs/rpc-api.md)
- [Plugin model](docs/plugin-model.md)
- [Enterprise notes](docs/enterprise.md)

## 安全模型

默认姿态：

1. 默认最小权限。
2. 未显式授权时不能访问 workspace 外部。
3. 默认不能读取 secret。
4. 默认限制网络访问。
5. 默认拒绝破坏性命令。
6. 文件改动走 patch transaction。
7. 插件没有隐式权限。

内置 profile 定义常见策略组合：

| Profile | 适用场景 |
|---|---|
| `read-only` | 检查 workspace，不允许写入、命令、网络或 secret。 |
| `safe-edit` | 读 workspace 文件，通过 patch 写入，运行 allowlist 中的测试/构建命令。 |
| `full-workspace` | 更宽的 workspace 访问，仍然审计并支持审批。 |
| `ci-runner` | 测试/构建自动化，限制任意 shell 和 secret 访问。 |
| `enterprise-restricted` | 组织策略叠加和中心化审批规则。 |

安全边界只有在限制被说清楚时才有意义。alpha 阶段的重要限制：

- Carina 本身不是 VM，也不是完整容器隔离系统。
- 选定后端已实现 OS 级 sandboxing，但生产部署 profile 仍需单独评审。
- 策略正确性依赖命令通过 Carina toolchain 和 daemon 控制的环境运行。
- 公开打包和供应链 provenance 还未完成。

## 与其它工具的关系

这不是胜负表。这个领域的工具优化目标不同，而且能力变化很快。具体功能应以各项目自己的文档为准。

| 如果你主要需要... | 常见工具 | Carina 的位置 |
|---|---|---|
| 编辑器内代码助手和交互体验 | Cursor、Windsurf、Cline、IDE 插件 | Carina 更底层。它可以支撑编辑器表面，但不试图替代编辑器。 |
| CLI 里的结对编程 | Aider、Claude Code 风格 CLI、Codex 风格 CLI | Carina 关注运行时边界：daemon 会话、策略、审计、回滚和 worker。 |
| 一次性托管执行环境 | E2B 和其它 sandbox provider | Carina 是本地优先的运行时基础设施。它可以使用 sandboxing，但核心关注点是逐动作控制和来源记录。 |
| 内部 Agent 的可复用执行底座 | 自研 Agent stack、CI 系统、内部平台 | Carina 设计为可嵌入到其它 UI 和 workflow 后面。 |

实际区别是：Carina 较少强调前端体验打磨，更多强调让 Agent 执行可检查、受策略约束、可回滚。

## 路线图

近期重点：

- 发布安装路径：签名 release、托管安装器、Homebrew tap 和供应链 provenance。
- 增加 `SECURITY.md`、贡献文档和 release 流程文档。
- 改进 TUI 和实时审计查看。
- 强化远程 worker 运行，并记录生产部署模式。
- 改进 TypeScript、Python、Go SDK 的能力对齐。
- 继续扩展 policy profile、sandbox backend 和插件签名。
- 在核心 Unix 路径稳定后增加 Windows 支持。

## 开发

构建全部组件：

```bash
make all
```

运行 Go 测试：

```bash
make go-test
```

运行 Rust 测试：

```bash
make rust-test
```

构建 Zig 工具：

```bash
make zig
```

有用文档：

- [PRD](docs/PRD.md)
- [Agent model](docs/agent.md)
- [Architecture](docs/architecture.md)
- [Security model](docs/security-model.md)
- [Research status](docs/research/absorption-status.md)

## 许可证

Apache-2.0。参见 [LICENSE](LICENSE)。
