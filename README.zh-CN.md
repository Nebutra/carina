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

当前仓库适合源码构建、本地实验，以及团队设计自己的 Agent 执行底座。它仍处于 alpha。macOS 公开安装包已经通过 Nebutra Homebrew tap 提供；Apple 签名和 notarization 自动化已经实现，但仍等待发布凭据。Linux 归档/系统包、npm、Windows worker、容器以及已打包的 VS Code/Web Operator 客户端均已进入发布流水线，剩余工作是外部 registry、publisher、凭据和托管激活。

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
| Agent loop | ReAct loop、结构化 action、双阈值/token 触发且逐字保留用户消息的 prompt compaction、结构化压缩摘要、规范签名 loop detection、连续失败熔断器、可选 best-of-N patch 生成、success check、verifier、risk review |
| Memory | 本地受控记忆库，区分 `memory` / `user` target；每次运行使用冻结 prompt snapshot；原生 `memory` tool、本地 `memory.*` RPC、kernel-gated `MemoryWrite` 审计 |
| 权限 | 内置 profile、approval mode、带理由的 approval overlay、workspace trust、org 锁定配置键、声明式子智能体 manifest（每 agent 工具白名单 + kernel-gated spawn capability）|
| 审计 | 哈希链事件日志、audit export、verify、规范化 `session.items`、turn net diff |
| 文件修改 | 事务性 patch propose/apply/rollback 和 post-edit diagnostics |
| 命令 | 风险分类、审批 gate、命令输出事件、可选 OS sandbox backend |
| 网络和 secret | 默认拒绝的 egress proxy、allowlist、daemon 侧凭证注入、显式 per-host HTTPS MITM opt-in |
| 模型 | BYOK auth chain、provider catalog、OpenAI/Anthropic/Gemini/OpenRouter 风格 adapter、catalog 门控的图片输入（原始字节只存 artifact store，永不进 transcript/审计）|
| Context engine | 原生 context engine 边界、bundled/configured Headroom 发现、私有 managed MCP transport、`carina context` 诊断 |
| 集成 | MCP client/server（含 `mcp_find` 工具搜索）、WASM plugin boundary（org/user/project 只紧不松 enable merge）、worker、workflow DAG |
| Nebutra 边界 | 本地 runtime 保持动作权威；身份和多端同步归 Nebutra Cloud（`nebutra.com`）边界 |

仍需外部激活：

- 首次通过 Apple 验收的签名和 notarization 公开发布；自动化已经
  fail-closed 落地，但所需 Apple 凭据尚未配置；
- Linux/npm/container 的公开 registry 与 trusted publisher 配置；
- 已打包 VS Code/Web Operator 客户端的 Marketplace 与托管发布；
- 让未添加 tap 的 `brew install carina` 生效所需的 Homebrew Core 上游审核；
- 需要真实 provider 凭据和代表性终端硬件的 CJK/reconnect 验证；
- Nebutra Cloud 的 API、tenant、identity、retention 外部合同；本地 sync 默认关闭；
- Windows 当前支持远程 worker 包，不宣称桌面 daemon/CLI。

## 使用 Homebrew 安装

Carina 通过 Nebutra 官方 tap 提供 Apple Silicon 和 Intel macOS 安装包：

```bash
brew install Nebutra/tap/carina
```

这个完整名称会添加 tap 并信任 Carina Formula。首次安装后，
`brew install carina` 会解析到同一个 Formula。

使用 Homebrew 的标准流程升级：

```bash
brew update
brew upgrade carina
```

`brew update carina` 不是有效的 Homebrew 命令；`brew update` 更新包索引，
`brew upgrade carina` 升级已安装的 Carina。安装后不会自动启动 daemon。

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

提交后 CLI 会打印续会话提示：

```bash
To continue this session, run:
  carina resume <session_id>
```

检查执行结果：

```bash
./bin/carina sessions
./bin/carina resume <session_id> "继续上一个任务"
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

### 受控记忆

Carina 的长期记忆保存在 daemon state 目录下。本地 runtime 区分 agent/project notes（`target=memory`）和用户画像事实（`target=user`）。记忆会作为冻结 snapshot 进入一次 agent run，因此运行中写入会持久化，但不会重写当前运行的稳定 prompt 前缀。可以通过本地 `memory.*` RPC 或原生 `memory` tool 执行 add/replace/remove/batch。写入走默认需要审批的 `MemoryWrite` capability，受大小限制和内容扫描保护，审计只记录 target/scope/action/content hash，不记录原始记忆正文。

外部语义记忆 provider 和 Nebutra Cloud 记忆同步尚未启用。

### 原生 Context Engine

release 包会把锁定版本的 Headroom 作为 `bin/headroom` 随 Carina 一起发布。
`context_engine=auto` 只启用随包内建或显式配置的 Headroom；仅在 `PATH` 上找到的全局 `headroom` 会被报告，但不会当作内建引擎使用。

```bash
./bin/carina context status
./bin/carina context doctor
./bin/carina context stats
```

managed Headroom MCP server 只供 Carina context adapter 内部调用，不会出现在 agent 的公开 MCP tool 列表里。

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
8. 持久记忆写入需要 capability gate，并按 scope、大小和审计边界约束。

Alpha 限制：

- Carina 本身不是 VM，也不是完整容器隔离系统。
- OS sandbox backend 已存在，但生产 profile 需要部署前评审。
- 策略正确性依赖命令通过 Carina daemon 和 toolchain 执行。
- 发布归档已有 checksum 和 GitHub build provenance。Apple code signing 和
  notarization 自动化已经实现，但尚未完成一次带真实凭据、通过 Apple
  验收的公开发布。

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
