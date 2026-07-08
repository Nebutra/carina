<div align="center">

<img src="docs/assets/carina-hero.png" alt="Nebutra Carina" width="100%" />

# Nebutra Carina

**在策略、审计和回滚边界内运行真实仓库上的编程智能体。**

[![status](https://img.shields.io/badge/status-alpha-0033FE)](#当前状态)
[![build](https://img.shields.io/badge/build-source%20first-0B7285)](#从源码快速开始)
[![runtime](https://img.shields.io/badge/runtime-local--first-0BF1C3)](#为什么用-carina)
[![audit](https://img.shields.io/badge/audit-hash--chained-6D28D9)](#审查与审计)
[![license](https://img.shields.io/badge/license-MIT-informational)](LICENSE)

[English](README.md) · **简体中文** · [日本語](README.ja.md)

</div>

Carina 是一个本地优先的 AI 编程智能体运行时。它不是编辑器、聊天产品，也不是托管沙箱。它位于智能体和机器之间，让文件读取、代码修改、命令、网络访问、插件和 secret 都先经过明确策略，再真正发生。

当前仓库适合源码构建、本地实验，以及团队设计自己的 Agent 执行底座。它仍处于 alpha：稳定二进制发布、公开安装器和完整 dashboard 还没有完成。

## 为什么用 Carina

很多时候，难点不是让模型写代码，而是模型决定行动之后，如何控制它能做什么。

Carina 提供：

- **逐动作权限决策**：覆盖文件、命令、网络、secret、patch apply、插件和远程任务。
- **可审计执行**：append-only 哈希链记录权限决策和被允许的副作用。
- **事务性文件修改**：patch 可以提出、检查、应用和回滚。
- **Daemon 会话**：会话和后台任务可以在 CLI 退出后继续存在。
- **BYOK 模型接入**：provider catalog 发现，本地 API key 优先；配置后可走 Nebutra OAuth 兜底。
- **MCP、插件、子智能体、workflow、egress 控制**都经过同一能力边界。

## 适合什么

适合：

- 在本地仓库运行 Agent 任务，但不想给智能体原始机器权限；
- 需要知道智能体读了什么、改了什么、跑了什么、为什么被允许；
- 要为 IDE 插件、CI 集成、内部 Agent 平台或 workflow runner 提供执行底座；
- 希望子智能体、插件、远程 worker 只能拿到比父任务更窄的权限；
- 在需要回滚和审计的环境里评估 Agent 输出。

不适合：

- 你只需要编辑器内助手；
- 你想要托管式、开箱即用的 Agent 服务；
- 你今天就需要稳定安装包和发布渠道。

## 当前状态

当前仓库已经实现：

| 领域 | 当前能力 |
|---|---|
| 会话和任务 | daemon 会话、后台任务、事件流、attach/replay、task steering |
| Agent loop | ReAct loop、结构化 action、prompt compaction、success check、verifier、risk review |
| 权限 | 内置 profile、approval mode、带理由的 approval overlay、workspace trust、子智能体权限衰减 |
| 审计 | 哈希链事件日志、audit export、verify、规范化 `session.items`、turn net diff |
| 文件修改 | 事务性 patch propose/apply/rollback 和 post-edit diagnostics |
| 命令 | 风险分类、审批 gate、命令输出事件、可选 OS sandbox backend |
| 网络和 secret | 默认拒绝的 egress proxy、allowlist、daemon 侧凭证注入、显式 per-host HTTPS MITM opt-in |
| 模型 | BYOK auth chain、provider catalog、OpenAI/Anthropic/Gemini/OpenRouter 风格 adapter |
| 集成 | MCP client/server、WASM plugin boundary、worker、workflow DAG |
| Nebutra 边界 | 本地 runtime 保持动作权威；身份和多端同步归 Nebutra Cloud（`nebutra.com`）边界 |

还没有产品化完成：

- 签名公开 release、Homebrew tap 和 npm 安装渠道；
- 完整 contributor/security 流程；
- 打磨后的 TUI/dashboard；
- Windows 支持；
- TypeScript、Python、Go SDK 能力对齐；
- 远程 worker 集群的生产部署指南。

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

把模型凭证提供给 daemon 进程。BYOK API key 优先；配置后支持 Nebutra OAuth 兜底。

```bash
export ANTHROPIC_API_KEY=sk-...
# 或
export OPENAI_API_KEY=sk-...
```

在当前仓库运行任务：

```bash
./bin/carina run "fix the failing tests and show the patch"
```

检查执行结果：

```bash
./bin/carina sessions
./bin/carina items <session_id>
./bin/carina audit verify <session_id>
./bin/carina patch list <session_id>
./bin/carina patch show <session_id> <patch_id>
```

回滚已应用 patch：

```bash
./bin/carina patch rollback <session_id> <patch_id>
```

## 常见工作流

### 本地仓库任务

日常开发可使用默认 `safe-edit` 会话。智能体可以读取 workspace、提出 patch、运行 allowlist 中的构建/测试命令。危险命令、网络访问、secret 和插件会按当前 profile 被拒绝或要求审批。

### 审查与审计

`carina items <session_id>` 提供规范化的 thread/turn/item 视图，包括每轮 patch 汇总。需要原始事件链和防篡改验证时，使用 `carina audit <session_id>` 或 `carina audit verify <session_id>`。

### BYOK Provider

保存本地凭证并查看 provider catalog：

```bash
./bin/carina auth login anthropic - < ~/.secrets/anthropic-key
./bin/carina auth login openai - < ~/.secrets/openai-key
./bin/carina auth list
./bin/carina providers list --refresh
```

需要时显式选择运行模型：

```bash
CARINA_REASONER_MODEL=openai/gpt-5 ./bin/carina-daemon &
./bin/carina run --model openrouter/anthropic/claude-sonnet-4-5 "inspect this migration"
```

### Agent Mode 和 Slash Command

运行时查看可复用 agent 和 command：

```bash
./bin/carina agents list
./bin/carina commands list
./bin/carina run --agent plan "inspect the release risk"
./bin/carina run "/review main"
```

内置 agent 包括 `build`、`plan`、`general`、`explore`。用户和项目覆盖位于 `~/.carina/agents`、`<repo>/.carina/agents`、`~/.carina/commands` 和 `<repo>/.carina/commands`。

### 嵌入到其它产品

当 Carina 需要位于其它 UI 后面时，可以使用 JSON-RPC、SDK 或 MCP server mode：例如 IDE 插件、Web console、CI workflow 或内部 Agent 平台。

## 与其它工具的关系

这不是胜负表。这个领域的项目优化目标不同，而且能力变化很快；具体功能以各项目官方文档为准。

| 如果你主要需要... | 常见选择 | Carina 的位置 |
|---|---|---|
| 编辑器内代码助手 | Cursor、Windsurf、Cline、IDE 插件 | Carina 可以支撑编辑器，但自身不是编辑器产品。 |
| 终端里的结对编程 | Claude Code、Codex CLI、Aider、OpenCode | Carina 不把重点放在聊天 UX，而是放在运行时边界、审计、回滚、worker 和可嵌入性。 |
| 云端托管 Agent 任务 | OpenAI Codex cloud tasks 和其它托管 Agent 服务 | Carina 是本地优先。云身份和多端同步归 Nebutra Cloud 边界，而不是塞进本地 runtime。 |
| 一次性云沙箱 | E2B 和其它 sandbox runtime | Carina 可以使用 sandboxing，但核心单位是对仓库逐动作策略控制，不是托管 VM 产品。 |
| 内部 Agent 基础设施 | 自研 stack、CI 系统、内部平台 | Carina 适合作为 control-plane/runtime 组件被嵌入。 |

## 架构

Carina 按职责拆分：

| 层 | 职责 |
|---|---|
| Agent surface | Agent loop、transcript、approval、sub-agent、workflow |
| Control plane | session、scheduler、JSON-RPC、worker、event streaming、egress |
| Capability kernel | 权限决策、策略、事务性 patch、审计链、插件 |
| Native toolchain | scan、grep、diff、patch、进程执行、pty |
| Client surfaces | CLI、TUI、SDK、MCP client/server |

重点不是语言拆分，而是边界：智能体请求动作，运行时决定动作能否发生，并记录结果。

## 安全模型

默认姿态：

1. 默认最小权限。
2. 未显式授权时不能访问 workspace 外部。
3. 默认不能读取 secret。
4. 默认限制网络访问。
5. 默认拒绝破坏性命令。
6. 文件改动走 patch transaction。
7. 插件没有隐式权限。

Alpha 限制：

- Carina 本身不是 VM，也不是完整容器隔离系统。
- OS sandbox backend 已存在，但生产 profile 需要部署前评审。
- 策略正确性依赖命令通过 Carina daemon 和 toolchain 执行。
- 公开发布签名和供应链 provenance 还未完成。

见 [SECURITY.md](SECURITY.md) 和 [docs/security-model.md](docs/security-model.md)。

## 开发

构建和测试：

```bash
make all
go test ./go/... ./apps/...
cargo test
go test -race ./go/daemon ./go/config ./apps/carina-daemon
```

运行本地 release gate：

```bash
make release-check
```

构建本地 release candidate 归档：

```bash
make release-package
```

更多文档：

- [产品定位](docs/product.md)
- [Nebutra Cloud 边界](docs/nebutra-cloud-boundary.md)
- [Roadmap](docs/roadmap.md)
- [发布流程](docs/release.md)
- [架构](docs/architecture.md)
- [RPC API](docs/rpc-api.md)
- [插件模型](docs/plugin-model.md)
- [吸收状态](docs/research/absorption-status.md)

## 许可证

MIT License。参见 [LICENSE](LICENSE)。
