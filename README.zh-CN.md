<div align="center">

<img src="docs/assets/carina-hero.png" alt="Nebutra Carina —— 你的智能体运行其上的安全龙骨" width="100%" />

# Nebutra Carina

**一个安全的智能体运行时（agent runtime），由 Go、Rust 与 Zig 编写。**

*你的智能体运行其上的安全龙骨 —— 每一次副作用（side effect）都被把关、审计、可回滚。*

[![build](https://img.shields.io/badge/build-passing-0033FE)](#)
[![release](https://img.shields.io/badge/release-v0.1.0--alpha-0BF1C3)](#)
[![stack](https://img.shields.io/badge/Go%20%C2%B7%20Rust%20%C2%B7%20Zig-polyglot-8b5cf6)](#)
[![license](https://img.shields.io/badge/license-Apache--2.0-informational)](#)
[![signed releases](https://img.shields.io/badge/releases-signed-0033FE)](#)
[![no telemetry](https://img.shields.io/badge/telemetry-none-0BF1C3)](#)
[![powered by Nebutra](https://img.shields.io/badge/powered%20by-Nebutra-0033FE)](#)

`curl -fsSL https://get.nebutra.com/carina | sh`

[English](README.md) · **简体中文** · [日本語](README.ja.md)

</div>

---

## 什么是 Carina

**Carina 是真正把 AI 编程智能体「跑起来」的安全底座** —— 不是操作系统，不是框架，而是一个运行时。你把一项任务交给智能体，Carina 执行它的 ReAct 循环，同时让它的**能力内核（capability kernel）在边界处为每一次副作用把关**，把每一次都记入一份**防篡改、哈希链式的审计日志（audit log）**，并以**可回滚的事务性补丁（patch）**形式落地文件改动。

其他所有人卖的是一个会*行动*的智能体。Carina 运行的智能体不仅会行动，**而且值得信任、可被撤销**。

它面向的是这样一类人：想给智能体开放一台真实机器的真实权限 —— 写文件、跑命令、调工具 —— 却不必把钥匙拱手交出、然后只能祈祷。如果你曾想用一个密码学意义上确凿的「是」，来回答*「智能体到底做了什么，我能不能撤回？」*，那么 Carina 正是你以往靠一个 CLI、一个沙箱和一层审计补丁手工拼凑出来的那个运行时。

一个二进制文件取代那一整堆东西。你只需要 `carina`。

```
Go makes it run.  Rust makes it safe.  Zig makes it sharp.  LLM makes it useful.
```

---

## 为什么要三种语言（外加一个 LLM）

这就是全部立论所在。每种语言只干**一件事**，选它是因为它在这件事上独一无二地擅长。没有为堆技术而堆技术的设计 —— 每一个依赖都物有所值。

| 层 | 语言 | 唯一职责 | 具体机制 | 可度量的效果 |
|---|---|---|---|---|
| **控制平面** | **Go** | *让它跑起来* | daemon、调度器、会话存储、JSON-RPC 接口、模型路由。每会话一个 goroutine 的并发模型，单个长驻进程。 | 众多智能体会话，可远程调度，全部跑在一个受监管的 daemon 上。CLI 只是一个**客户端** —— 把它杀掉，会话照样存活。 |
| **能力内核** | **Rust** | *让它安全* | 策略引擎 + 可回滚补丁引擎 + 哈希链式仅追加审计日志 + 插件运行时。每一次副作用都要跨越一道带类型的能力边界。 | **100% 的副作用被把关 + 审计。**每个补丁**原子且可逆**。日志**防篡改** —— 改动任一条目，整条链就会断裂。 |
| **原生工具链** | **Zig** | *让它锋利* | 把 `scan`、`grep`、`diff`、`patch`、`pty` 做成小巧的原生二进制。无 GC、无运行时，输出结构化 JSON。 | 快速、分配开销极低的原语，启动只需约几毫秒。智能体每天要命中数千次的热路径，不必再为某个语言运行时买单。 |
| **智能体界面** | **LLM** | *让它有用* | 带类型化转录（typed transcript）的 ReAct 循环、压缩（compaction）+ 循环守卫（loop-guard）、带**能力衰减（capability attenuation，子集 ⊆ 父集）**的子智能体，以及 Codex 风格的目标 / 成功标准 + 审批模式。 | 智能体能*思考并行动* —— 而派生出的子智能体**永远无法超出其父智能体的权限**。有委派，无提权。 |

### 用大白话说清这些机制

- **能力把关（capability gating）。** 智能体从不直接触碰操作系统。读一个文件、写一个补丁、派生一个进程、访问网络 —— 每一项都是一种**能力**，必须由内核授予。未授予的副作用 ⇒ 这个副作用根本不会发生。这就是「模型承诺不会 `rm -rf`」和「模型压根没被交予执行它的能力」之间的区别。
- **防篡改审计日志。** 每一次被授予的副作用都会追加进一份日志，其中每条条目都嵌入了前一条的哈希（`hash(N) = H(entry_N ‖ hash(N-1))`）。整条链端到端可验证：你能证明事后没有任何条目被插入、删除或改写。不是「相信我」，而是「核对这道数学题」。
- **事务性、可回滚补丁。** 文件改动不会边写边祈祷落盘顺利。补丁引擎把一次改动暂存为一个事务，让你先预览，再原子地落地，并保留足够信息以便**干净地撤销它**。一次糟糕的智能体编辑，只需一条 `carina rollback` 即可挽回。
- **子智能体衰减（sub-agent attenuation）。** 当一个智能体进行委派时，子智能体继承的是父智能体能力的一个**子集** —— 绝不会是超集。一个只需要读权限的调研子智能体，不可能突然获得写权限。最小权限，从结构上强制执行，一路贯穿整棵树。
- **原生 Zig 工具。** 智能体高频依赖的那些原语（grep 一个仓库、diff 一处改动、驱动一个 pty）都是原生且结构化的 —— 为机器调用而设计，直接吐出 JSON，而不是从给人看的 stdout 里去刮取。

> **使命。** 未来十年的软件将在智能体参与的循环中写就。Carina 押下一个赌注：「拥有真实权限的智能体」与「处于掌控之中」并非一道非此即彼的取舍 —— 只要安全被正确地实现，它不过就是好的基础设施。

---

## 架构

```
┌───────────────────────────────────────────────────────────┐
│                      Agent Surface  (LLM)                  │
│   CLI · TUI · SDK · ReAct loop · sub-agents · approval     │
└───────────────────────────────┬───────────────────────────┘
                                │ JSON-RPC
┌───────────────────────────────▼───────────────────────────┐
│                   Control Plane  (Go)                      │
│   daemon · scheduler · session store · model router        │
│   "make it run"                                            │
└───────────────────────────────┬───────────────────────────┘
                                │ Capability API  (every effect crosses here)
┌───────────────────────────────▼───────────────────────────┐
│                 Capability Kernel  (Rust)                  │
│   policy engine · transactional patch · hash-chained audit │
│   plugin runtime · "make it safe"                          │
└───────────────────────────────┬───────────────────────────┘
                                │ Native tool calls
┌───────────────────────────────▼───────────────────────────┐
│                 Native Toolchain  (Zig)                    │
│   scan · grep · diff · patch · pty · "make it sharp"       │
└───────────────────────────────────────────────────────────┘
```

**核心不变量**

1. 智能体从不直接触碰系统资源。
2. 每一次副作用都要穿过能力内核。
3. 每一次被授予的副作用都会追加进哈希链式审计日志。
4. 每一个补丁都可预览、可验证、可回滚。
5. 每一个工具与插件都显式声明其能力。
6. 默认本地优先（local-first）；远程执行是一种扩展，而非硬性要求。
7. CLI 是客户端 —— daemon 才是运行时。

更深入的内容见 [`docs/architecture.md`](docs/architecture.md) 与 [`docs/security-model.md`](docs/security-model.md) —— 本 README 披露的是模型，而非内部实现细节。

---

## 安装

> **需要：** macOS 或 Linux（x86-64 / arm64）。Windows 已列入[路线图](#roadmap)。

**一条命令（推荐）：**

```bash
curl -fsSL https://get.nebutra.com/carina | sh
```

**Homebrew：**

```bash
brew install nebutra/tap/carina
```

**从源码构建** —— 需要 Go ≥ 1.25、Rust ≥ 1.85、Zig 0.15.x：

```bash
git clone https://github.com/Nebutra/carina && cd carina
make all        # builds Go control plane, Rust kernel crates, Zig tools
```

验证：

```bash
carina --version   # carina 0.1.0-alpha
```

---

## 快速上手 —— 你的第一次智能体运行

```bash
# 1. Start the runtime (control-plane daemon; sessions outlive your shell)
carina daemon &
#   ⇒ carina daemon listening on ~/.carina/daemon.sock

# 2. Point an API key at it (never hardcoded — env only)
export ANTHROPIC_API_KEY=sk-...

# 3. Run your first agent against a task
carina run "add a --json flag to the status command and update its test"
#   ⇒ session f3a9c1  created
#   ⇒ [react] plan → grep → edit → test
#   ⇒ [gate]  write  cmd/status.go              approved (capability: fs.write)
#   ⇒ [patch] staged 1 file · atomic · rollbackable  →  cr patch show f3a9c1
#   ⇒ [audit] entry 0007 chained  sha256:9b1e…  (prev 3c7a…)
#   ⇒ done · success criteria met · 1 patch applied

# 4. Inspect exactly what happened — and verify the chain
carina audit f3a9c1 --verify
#   ⇒ 7 entries · chain intact · no tampering detected ✓

# 5. Don't like it? Take it back, atomically.
carina rollback f3a9c1
#   ⇒ reverted 1 patch · workspace clean
```

每个动词都提供了短别名 `cr`（`cr run`、`cr audit`）。在脚本和文档中请优先使用完整的 `carina`。

---

## 核心能力

- 🔒 **能力把关** —— 每一次副作用都需要显式授权；未授权 ⇒ 绝不发生。
- 🧾 **哈希链式审计日志** —— 仅追加、防篡改、端到端可验证。
- ↩️ **事务性回滚** —— 补丁原子且可干净地逆转。
- 🧬 **子智能体衰减** —— 子能力 ⊆ 父能力，从结构上强制执行。
- 🔁 **ReAct 循环 + 循环守卫** —— 类型化转录、压缩、失控检测。
- ✋ **审批模式** —— Codex 风格的目标 / 成功标准；对指定的副作用类别要求人工签字放行。
- ⚡ **原生 Zig 工具链** —— scan / grep / diff / patch / pty，结构化 JSON，无 GC。
- 🛰️ **daemon 优先** —— 会话可远程调度，并在 CLI 之外存活。
- 🔌 **沙箱化插件运行时** —— 第三方工具在同一套能力契约下运行。
- 🙈 **无遥测** —— 什么都不会回传；日志归你所有。

---

## 安全与可审计性

对一个智能体运行时来说，**安全就是产品本身。** Carina 的保证是一条由三项属性构成的链条，每一项都可验证：

1. **已把关（Gated）** —— 能力内核是通往副作用的*唯一*路径。不存在让模型直接调用 `exec` 的后门；它向内核请求，由内核依据策略作出裁决。审批模式让你可以在任一副作用类别（写入、网络、进程派生）上插入一名人类。
2. **已审计（Audited）** —— 每一次被授予的副作用都成为仅追加日志中的一条条目，条条哈希链接到上一条。`carina audit <session> --verify` 会重新计算整条链并报告任何篡改。由于这种链接是密码学的，篡改历史的攻击者必须打破其后每一个哈希 —— 而这做不到。
3. **可逆（Reversible）** —— 补丁引擎把改动当作事务对待。事前预览，事后回滚，皆为原子操作。「智能体把我的仓库搞坏了」不再是一场灾难，而只是一次 `rollback`。

子智能体继承的是**衰减后**的能力集合（子集 ⊆ 父集），因此委派永远不会导致提权。插件在同一个沙箱内、依据与一方工具相同的显式能力契约运行。

威胁模型、日志格式与验证协议见 [`docs/security-model.md`](docs/security-model.md)。

**上报漏洞：** 请发邮件至 **security@nebutra.com**（参见 [`SECURITY.md`](SECURITY.md)）。请不要为安全上报开公开 issue。

---

## 前人成果与技术渊源

Carina 立足于业已验证的成熟技术，而非为标新立异而标新立异：**ReAct** 循环（推理 + 行动）、**Codex 风格**的目标 / 成功标准与审批模式、带衰减的**基于能力的安全（capability-based security）**（子集 ⊆ 父集），以及防篡改的**哈希链式日志**。它的贡献在于，把这些统一到一个运行时之下、置于同一道执行边界之内。

---

## 横向对比

| | Carina | Aider / Cline | Cursor / Windsurf | E2B / 沙箱 |
|---|---|---|---|---|
| 在受监管的 daemon 上运行智能体 | ✅ | ❌（受限于 CLI） | ❌（受限于编辑器） | 部分 |
| 每一次副作用都经能力把关 | ✅ | ❌ | ❌ | ✅（隔离，而非逐副作用） |
| 防篡改审计日志 | ✅ | ❌ | ❌ | ❌ |
| 编辑的事务性回滚 | ✅ | 仅靠 git | 仅靠 git | ❌ |
| 子智能体能力衰减 | ✅ | ❌ | ❌ | ❌ |

Carina **不是**编辑器，**也不是**托管产品。它是别人都能跑在其上的那个底座。

---

## 路线图

如实交代现状。Carina 处于 **alpha** 阶段 —— 执行核心已经货真价实；围绕它的生态尚在早期。

**已交付**
- [x] Go 控制平面：daemon、调度器、会话存储、JSON-RPC
- [x] Rust 能力内核：策略引擎 + 能力把关
- [x] 防篡改哈希链式审计日志 + `--verify`
- [x] 事务性、可回滚补丁引擎
- [x] Zig 原生工具链：scan / grep / diff / patch / pty
- [x] 带类型化转录、压缩、循环守卫的 ReAct 智能体循环
- [x] 子智能体能力衰减（子集 ⊆ 父集）
- [x] `carina` CLI（别名 `cr`）
- [x] Workflow 编排引擎 —— 声明式子智能体 DAG，并行 + 可恢复
- [x] 可持久化、可恢复的后台运行 —— 逐轮 checkpoint + 重启续跑、运行注册表（`task.list`/`task.result`）、并发上限、panic 隔离

**规划中**
- [ ] 在初始路由集之外接入更多模型提供方
- [ ] 沙箱配置档（按项目的能力预设与模板）
- [ ] 插件市场 + 签名插件分发
- [ ] 用于实时会话 + 审计流查看的 TUI 仪表盘
- [ ] 跨工作节点的远程 / 集群化执行
- [ ] Windows 支持
- [ ] TypeScript / Python / Go 之间对等的 SDK
- [ ] 为所有发布产物提供 SLSA 构建溯源
- [ ] 托管的一键安装脚本（`get.nebutra.com`）+ Homebrew tap + 公开的 `SECURITY.md`（security@nebutra.com）
- [ ] 后台 agent 体验：attach/tail（重放游标）、完成 webhook、git worktree 隔离、远程/沙箱运行

在 [GitHub Issues](https://github.com/Nebutra/carina/issues) 追踪进展 —— 那些缺口是贡献的机会，而非意外的坑。

---

## 参与贡献

各操作系统的开发构建指南与架构导览见 [`CONTRIBUTING.md`](CONTRIBUTING.md)。构建命令为 `make go` / `make rust` / `make zig` / `make all`。新增一项能力的 PR 必须同时补上它的审计日志覆盖 —— 这是本项目的铁律。

## 社区

- 讨论：[GitHub Discussions](https://github.com/Nebutra/carina/discussions)
- 聊天：[Discord](https://discord.gg/nebutra)
- 动态：[@nebutra](https://x.com/nebutra)

## 许可证

Apache-2.0 —— 参见 [`LICENSE`](LICENSE)。

<div align="center">

**Nebutra Carina** —— 你的智能体运行其上的安全龙骨。
由 [Nebutra](https://nebutra.com) 提供支持 · [Sailor](https://github.com/Nebutra/create-sailor) 的姊妹项目。

</div>