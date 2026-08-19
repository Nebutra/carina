# Post-0.8.24 Harness Audit Refresh

> **取证日期**：2026-08-19  
> **产品锚点**：`go/product/version.go` → **0.8.25**  
> **工作树**：干净 — G24-01/G24-02 + R-01 随 0.8.25 发版  
> **方法**：本仓库源码 + `crates/carina-tui/CAPABILITIES.md` + `docs/rpc-api.md` + `docs/product.md` + 本机竞品树抽查 + DeepSeek 公开 README（**本机无 clone**）  
> **前序**：`12-post-0.8.23-refresh.md` 已过时，**以本文为现状真相**  
> **未做**：并排 PTY、同机 RAM PSS、DeepSeek 本地行号级 DNA

---

## 0. Executive Summary

Carina 0.8.24 已经不是「内核强、壳弱」。ISSUE-001…018、V001–V011、`/queue`、0.8.22–23 harness honesty（#28–#39）、以及 0.8.24 的 **握手诚实 + Compact 一等 cell + Grok 过期缓存跳过 + Goal activation 进程内** 都已合入并安装。竞品树相对 2026-08-18 **无漂移**。

**功能清单债已还完。** 用户可感短板从「首轮办不了事」收成：**文档/实现不一致、巨石回潮、上游 503**。DeepSeek Harness（Cordis「一切皆插件」）**不得**作为 day-one 架构。可偷 Trajectory 不变量、Goal 激活语义、会话内 Schedule；不可偷插件核。SaaS 多租户 / ACP 当聊天协议与 `docs/product.md` 冲突，一票否决。

### Top 5 优先行动

| # | 行动 | 用户价值 | 状态 |
|---|------|----------|------|
| 1 | Isolated CLI 握手 + 503 文案 | 首轮不再被误判 safety/protocol | **SHIPPED** `670e5c5` / 0.8.24 |
| 2 | `provider_attempts` 投影到失败格 | 4 次 503 显示尝试 4 次 | **SHIPPED** 0.8.24 |
| 3 | Compact 一等 transcript cell | 长会话压缩可看见、可回放 | **SHIPPED** 0.8.24 |
| 4 | Grok 过期 `models_cache` 跳过 + Goal disarm | `hi` 不 23ms 死；重启不偷跑 | **SHIPPED** 0.8.24 |
| 5 | **Esc/interrupt 暂停 session Goal** | 文档已承诺；实现只暂停 execution | **SHIPPED** G24-01；**WT G24-02** 硬取消 |

**明确不做（仍有效）**：默认 yolo · snapcompact 默认 · MiniLM-on · 3D/Buddy · 整包 Grok pager · Cordis 一切皆插件 · MCP passthrough · git-only rollback · hashline/Codex fuzzy 换掉 `carina-patch` · ACP 当交互协议 · SaaS 多租户 day-one。

---

## 1. Phase 0 — Carina 模块现状地图（0.8.24）

```text
Surfaces: carina-tui · CLI · VS Code · Web Operator · SDK(TS/Py/Go) · Gateway /v1
    ↓ JSON-RPC（protocol/jsonrpc/methods.json）
Go control: agent loop · reasoner · transcript.compact · MCP · HITL · subagent · workflow
    ↓ Capability API
Rust kernel: policy · patch · audit · WASM plugin · index（永不本地算 embedding）
    ↓
Zig: scan · grep · diff · patch-native · run · pty
```

| 模块 | 路径 | 分类 |
|------|------|------|
| Agent loop | `go/daemon/agent.go`（`maxAgentTurns=64`，`maxRequeries=3`） | **SHIPPED** |
| Reasoner / 路由 | `reasoner.go` + grok/claude/codex CLI 隔离 | **SHIPPED**（握手 + `provider_attempts`） |
| contextengine | `go/contextengine` auto→noop | **SHIPPED** 诚实适配器（不是压缩机） |
| Product compact | `transcript.go` elide→collapse→summarizer + 3-fail breaker | **SHIPPED** |
| Compact cell | `items.go` `context_compacted` → TUI `compact:{event_id}` | **SHIPPED** |
| `/context` ledger | `context_summary.go` = `tr.render()`；非 Anthropic `cache=none` | **SHIPPED** |
| HITL presets | `hitl_preset.go` 标签，不改 profile | **SHIPPED** |
| `/agents` `/plugins` | `agent.view` / `extension.list`；永不 `plugin.run` | **SHIPPED** |
| Goal 记录 | `goals.go`：objective/status/budget 落盘；`auto_continue` 进程内 | **SHIPPED** |
| Goal × interrupt | `pauseForSoftInterrupt` → `pauseActiveGoal` | **SHIPPED**（WT） |
| `web.fetch` | `web_fetch.go` 公网 HTTPS + host 审批 | **SHIPPED** |
| TUI density V001–V011 | `CAPABILITIES.md` 全 **wired** | **SHIPPED** |
| `/queue` inspect | `execution.queue.list/drop` | **SHIPPED** |
| residual / density 铁门 | `make residual-ux-gate` / `visual-density-gate` | **WIP**（目标存在，未进 `release-check` / CI） |
| Gateway | `gateway_pin.go` 单 workspace，**不是 ACP/SaaS** | **SHIPPED** / **ISOLATED** |
| HMS / best-of-n / swarm_channel | `memory_hms.go` / `bestofn.go` / `swarm_channel.go` | **UNPRODUCTIZED** / **ISOLATED** |
| MiniLM | 未捆绑；`transcript.go` 锁 off | **MUST-PROTECT off** |
| Nebutra Cloud | `go/nebutra` sync=off | **UNPRODUCTIZED** |
| TUI 巨石 | `app/mod.rs` 12225 · `render.rs` 16784 · `i18n.rs` 8544 | **SLOP**（#33 已拆 composer） |
| 旧全局 sock | `~/.carina/daemon.sock` | **LEGACY** |

用户视角短板：长会话压缩已可见；重启不会偷跑 Goal。Esc 停的是 **run**，不是 session Goal——若 `auto_continue` 在本进程仍武装，失败重试仍可能再交任务。Mox 503 仍是上游环境。TUI 必须重开才能吃到已安装的 0.8.24。

---

## 2. Phase 1 — 六家精华（2026-08-19 复验）

本机根：`…/assets/references/competitors/{jcode,grok-build,codex,oh-my-pi}` + `…/claude-code-notes`。DeepSeek **未 clone**；公开 README 仍写 Everything is a Plugin / Cordis / developer preview。

| 产品 | 版本指纹 | 架构一句话 | 可偷（原则） | 禁止抄 |
|------|----------|------------|--------------|--------|
| **Jcode** | `Cargo.toml` **0.64.2**（未漂） | 单 server 多 client；soft interrupt；intent 强制；80/95 compact | intent 行、图像 flat token、soft interrupt 协议 | MiniLM 默认开（`memory_embedding_backend` 默认 local all-MiniLM-L6-v2）、1000-agent 叙事 |
| **Grok Build** | `SOURCE_REV` **`8d69c91f02bcacf01e98d5aebbf2f92547c45738`**（未漂） | pager∥shell；ACP 仅 IDE/stdio；hunk + plan 状态机 | 副作用三态、prompt queue、plan 硬闸 | 整包 pager、ACP 当主协议、默认 yolo |
| **Claude notes** | badge **v2.1.88** / 56 篇（未漂） | QueryEngine + 四级 compact + 3-strike | collapse 层、摘要 rebuild 上限、slash 三型 | Buddy/Ink 整栈、MCP passthrough、git-only 回滚 |
| **Codex** | workspace `version = "0.0.0"`；`AskForApproval` ⊥ sandbox 仍在 | SQ/EQ；审批轴独立于沙箱轴 | 双轴术语、fragment 纪律、5 行工具摘要 | fuzzy apply_patch 换掉事务 patch、pets |
| **OMP** | packages **17.2.3**（未漂） | TS 壳 + Rust natives；glyph 注册表 | read 分组、compaction 一等 entry、glyph 三档 | **默认 yolo**、snapcompact 默认、hashline 垄断 |
| **DeepSeek Harness** | 公开 README；本机无树 | Cordis 一切皆插件；Web UI `npx @deepseek-ai/dsh web` | Trajectory 不变量、Goal disarm、会话内 schedule | **Cordis 核**（会拆掉 capability kernel） |

复杂度：偷原则 S–M；换核 XL 且伤定位。

---

## 3. Phase 2 — GAP Scorecard（0.8.24）

量尺同 `02-comparison-matrix.md`。竞品列沿用 08-02（树未漂）。DeepSeek 为公开文档推断。

| 维度 | 最佳代表 | Carina 0.8.0 | **Carina 0.8.24** | 差距 | 用户价值判断 |
|------|----------|:------------:|:-----------------:|:----:|--------------|
| 功能覆盖 | Jcode/Grok/CC/Codex/OMP 5 | 4 | **4** | P2 | Browser/overnight 非目标；不必追均分 |
| 智能体范式 | Jcode/CC 5 | 4 | **4** | P2 | Loop/subagent/DAG 够用；swarm 不产品化 |
| TUI/审美 | OMP/Jcode/Grok 5 | 4 | **4** | P2 | V001–V011 已合；再抄皮肤无感 |
| 上下文工程 | Jcode/CC 5 | 4 | **4** | P2 | ledger + compact cell 已诚实；不再是 P1 |
| 扩展性 | Grok/CC/OMP/DSH 5 | 4 | **4** | P2 | `/plugins` 只读库存；marketplace wow 非优先 |
| 资源效率 | Jcode 5 | 3 | **3** | P2 | 诚实 RSS；禁止编造 PSS |
| 治理/安全 | Carina/Codex 5 | **5** | **5** | — | **必须保护** |
| 用户可感 | 终端产品 5 | 4 | **4** | **P1** | 首轮握手已修；Goal×Esc 文档说谎会打回信任 |
| 产品化/集成 | Grok/CC/Codex 5 | 4 | **4** | P2 | SDK/Gateway 已有；SaaS 非 day-one |
| DeepSeek 治理 | — | — | **2（推断）** | — | 无特权核，与 Carina 正交 |

---

## 4. Phase 3 — SWOT + 过滤

**S**：kernel+audit+事务 patch；0.8 日用壳；honesty 程序把 noop/cache/HITL/compact/goal-activation 说真话。  
**W**：TUI 巨石；Goal×interrupt 文档超前；铁门未挂进 release-check。  
**O**：把 Esc 与 Goal 对齐（DSH 激活语义的最后一毫米）；R-01 进 companion。  
**T**：抄 Cordis/yolo/pager/SaaS 稀释定位；未重开 TUI 会让已安装的 0.8.24 像没发。

**看起来很酷、多数用户无感（降级/放弃）**：3D idle、Buddy、百主题、1000-agent swarm、Creator 热挂插件、ACP 当聊天协议、SaaS 多租户控制台、对标 Lovable/v0 的托管 Chat。

---

## 5. Phase 4 — 分类清单

### GAP（真缺、用户会感到）

| ID | 项 | P | 证据 |
|----|----|---|------|
| **G24-01** | Esc / `execution.interrupt` 不暂停 session Goal | **SHIPPED** `4a8acb1` |
| **G24-02** | Ctrl-C / `execution.cancel` 把 cancelled 当失败续跑 | **SHIPPED**（WT：cancel → `pauseActiveGoal`；`reconcileGoalTask` 忽略 cancelled） |

### WIP

- Native scrollback **partial**（`CAPABILITIES.md` #11）
- Semantic memory / HMS：默认 off，BYOK 才有

### TODO

- 可选：DeepSeek clone 后再写 `01-dna-deepseek.md`

### BUG

- 无未修运行时产品 bug 挂账；Mox 503 是上游环境

### LEGACY

- `~/.carina/daemon.sock` 全局布局（显式 mode）

### SLOP

- `app/mod.rs` 12225 行、`render.rs` 16784 行、`i18n.rs` 8544 行
- `go/worker` 头注释仍写「Remote / CI / sandbox workers land in Phase 3」

### ISOLATED

- Grok ACP 私有线、Gateway pin、Nebutra sync、HMS、swarm_channel、xai-ratatui forks

### UNPRODUCTIZED

- best-of-n、swarm 通道、VS Code Marketplace、hosted Web Operator、HMS

**已闭合（不要当缺口重开）**：ISSUE-001…018、V001–V011、R-02 `/queue`、#28–#39 honesty、G23-01…G23-04（握手 / 重试计数 / compact cell / Goal disarm）。

---

## 6. Phase 5 — 路线图

### P0 · 0.8.24 已发 · **SHIPPED**

握手、`provider_attempts`、Compact cell、stale Grok cache、`auto_continue` 进程内。TUI 需重开。

### P1 · G24-01 Esc 暂停 Goal · **SHIPPED** `4a8acb1`

### P1 · G24-02 硬取消暂停 Goal · **SHIPPED**（工作树）

- **价值**：Ctrl-C 之后 Goal 不得把 cancelled 当失败并再交任务。  
- **路径**：`handleTaskCancel` 成功后 `pauseActiveGoal(..., "operator_cancelled")`；`reconcileGoalTask` 直接忽略 `cancelled`。

### P1 · R-01 铁门进 companion · **SHIPPED**（工作树）

- `release-preflight` 具名门 `residual_ux` / `visual_density`；CI 在 kernel 构建后跑 `make residual-ux-gate`（visual-density 已有）。不重开 ISSUE-001…018。

### P2 · 巨石拆分 / RAM 文化

- 只随功能顺手拆；禁止为审美重写 pager。

### 明确不做（Wave 6）

- ACP 当交互协议 / SaaS 多租户 daemon。Gateway 保持单 workspace pin。

---

## 7. 审美与 TUI 规范（已有法律，不新发明皮肤）

1. `docs/brand/AGENTS.md` + `design-tokens.json` 权威。  
2. `crates/carina-tui/styles.md`：transcript ≤3 饱和色；根帧 `Color::Reset`。  
3. `VOICE.md`：结果语言；截断必有逃生口；用户可见字符串走 `MessageId`。  
4. `theme.rs` / `glyphs.rs`：token + 宽锁定；`auto|unicode|nerd|ascii`。  
5. 失败/diff/审批永不被 collapse 藏住。  
6. 新 cell 必须 live=replay。  
7. 偷原则不偷像素：无 π、无 GrokNight 整抄、无 Buddy。

---

## 8. 风险与 Trade-off

| 风险 | 防护 |
|------|------|
| 为对等终端聊天产品堆装饰 | 只做 Goal×Esc 诚实 + 铁门挂载 |
| 默认 yolo 抢转化 | presets 是标签；`/always-approve` 显式 |
| Cordis/ACP/SaaS 看起来「更现代」 | 与 `docs/product.md` 定位冲突；一票否决 |
| 已安装未重开 TUI | 0.8.24 对旧 epoch 不可见 |
| TUI 巨石继续胀 | 铁门 + 顺手拆，不开 XL 重写 |
| interrupt 时 Goal 审计失败 | fail-closed：run 已 paused 则 Goal 必须 paused 或回滚并表面错误 |

**置信度**：Carina 路径高（G24-01 已对照 `handleTaskInterrupt` / TUI `Esc`）；竞品抽查高（版本未漂）；DeepSeek 中（无本地树，公开 README 已复验）。
