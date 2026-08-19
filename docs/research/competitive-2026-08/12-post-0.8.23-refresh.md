# Post-0.8.23 Harness Audit Refresh

> **取证日期**：2026-08-19  
> **产品锚点**：`go/product/version.go` → **0.8.23**（`670e5c5` 已上 main）  
> **工作树**：未提交 — Compact 一等 cell + Grok 过期 `models_cache` 跳过隔离失败  
> **方法**：本仓库源码 + `crates/carina-tui/CAPABILITIES.md` + GitHub ISSUE-019…040 closed + 本机竞品树抽查 + DeepSeek 公开 README（**本机无 clone**）  
> **前序**：`07-post-0.8-gap-refresh.md`（0.8.0）已过时，**以本文为现状真相**  
> **未做**：并排 PTY、同机 RAM PSS、DeepSeek 本地行号级 DNA

---

## 0. Executive Summary

Carina 0.8.23 已经不是「内核强、壳弱」。ISSUE-001…018、视觉密度 V001–V011、queue inspect、以及 0.8.22–23 的 **harness honesty**（#28–#39 / epic #40）都已合入。竞品树相对 2026-08-02 **几乎无漂移**（Jcode 仍 0.64.2；Grok `SOURCE_REV` 仍 `8d69c91f…`）。

**功能清单债已还完。** 握手/503 计数在 `670e5c5`。工作树里还有三刀未提交：Compact 一等 cell、Grok 过期模型缓存不再挡会话、Goal activation 进程内（重启不续跑）。剩下的用户可感债是：**TUI 巨石回潮、上游 503、失败格中英混排（隔离文案已补）**。

DeepSeek Harness（Cordis「一切皆插件」）**不得**作为 day-one 架构。可偷 Trajectory 不变量、Goal 激活语义、会话内 Schedule；不可偷插件核。

### Top 5 优先行动

| # | 行动 | 用户价值 | 状态 |
|---|------|----------|------|
| 1 | Isolated CLI 握手 + 503 文案 | 首轮不再被误判 safety/protocol | **SHIPPED** `670e5c5` |
| 2 | `provider_attempts` 投影到失败格 | 4 次 503 显示尝试 4 次 | **SHIPPED** `670e5c5` |
| 3 | Compact 一等 transcript cell | 长会话压缩可看见、可回放 | **WT 未提交** |
| 4 | Grok 过期 `models_cache` 跳过 | `hi` 不再 23ms 死于隔离准备 | **WT 未提交** |
| 5 | **Goal/续跑 disarm + R-01 铁门** | 重启不偷跑；防巨石回潮 | **WT：Goal activation 进程内** |

**明确不做（仍有效）**：默认 yolo · snapcompact 默认 · MiniLM-on · 3D/Buddy · 整包 Grok pager · Cordis 一切皆插件 · MCP passthrough · git-only rollback · hashline/Codex fuzzy 换掉 `carina-patch` · ACP 当交互协议 · SaaS 多租户 day-one。

---

## 1. Phase 0 — Carina 模块现状地图（0.8.23）

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
| Reasoner / 路由 | `reasoner.go` + grok/claude/codex CLI 隔离 | **SHIPPED**（`670e5c5` 握手 + `provider_attempts`） |
| contextengine | `go/contextengine` auto→noop | **SHIPPED** 诚实适配器（不是压缩机） |
| Product compact | `transcript.go` elide→collapse→summarizer + 3-fail breaker | **SHIPPED** |
| `/context` ledger | `context_summary.go` = `tr.render()`；非 Anthropic `cache=none` | **SHIPPED**（#28/#34） |
| HITL presets | `hitl_preset.go` 标签，不改 profile | **SHIPPED**（#37） |
| `/agents` `/plugins` | `agent.view` / `extension.list`；永不 `plugin.run` | **SHIPPED**（#35/#36） |
| `web.fetch` | `web_fetch.go` 公网 HTTPS + host 审批 | **SHIPPED**（0.8.22） |
| TUI density V001–V011 | `CAPABILITIES.md` 全 **wired** | **SHIPPED** |
| `/queue` inspect | `execution.queue.list/drop` | **SHIPPED**（原 R-02） |
| Gateway | `gateway_pin.go` 单 workspace，**不是 ACP/SaaS** | **SHIPPED** / **ISOLATED** |
| HMS / best-of-n / swarm_channel | `memory_hms.go` / `bestofn.go` / `swarm_channel.go` | **UNPRODUCTIZED** / **ISOLATED** |
| MiniLM | 未捆绑；`transcript.go` 锁 off | **MUST-PROTECT off** |
| Nebutra Cloud | `go/nebutra` sync=off | **UNPRODUCTIZED** |
| TUI 巨石 | `app/mod.rs` ~12k · `render.rs` ~16k | **SLOP**（#33 已拆 composer） |
| 旧全局 sock | `~/.carina/daemon.sock` | **LEGACY** |

用户视角短板：长会话压缩无独立 cell（工作树已投影）；Goal 续跑已 disarm；TUI 需重开才能吃到 `670e5c5`。Mox 503 仍是上游环境。

---

## 2. Phase 1 — 六家精华（2026-08-18 抽查）

本机根：`…/assets/references/competitors/{jcode,grok-build,codex,oh-my-pi}` + `…/claude-code-notes`。DeepSeek **未 clone**。

| 产品 | 架构一句话 | 可偷（原则） | 禁止抄 |
|------|------------|--------------|--------|
| **Jcode 0.64.2** | 单 server 多 client；soft interrupt；intent 强制；80/95 compact | intent 行、图像 flat token、soft interrupt 协议 | MiniLM 默认开、3D idle（0.64.2 已默认关）、1000-agent 叙事 |
| **Grok `8d69c91f`** | pager∥shell；ACP 仅 IDE/stdio；hunk + plan 状态机 | 副作用三态、prompt queue、plan 硬闸 | 整包 pager、ACP 当主协议、默认 yolo |
| **Claude notes 2.1.88** | QueryEngine + 四级 compact + 3-strike | collapse 层、摘要 rebuild 上限、slash 三型 | Buddy/Ink 整栈、MCP passthrough、git-only 回滚 |
| **Codex** | SQ/EQ；`AskForApproval` ⊥ `SandboxPolicy` | 双轴术语、fragment 纪律、5 行工具摘要 | fuzzy apply_patch 换掉事务 patch、pets |
| **OMP 17.2.3** | TS 壳 + Rust natives；glyph 注册表 | read 分组、compaction 一等 entry、glyph 三档 | **默认 yolo**、snapcompact 默认、hashline 垄断 |
| **DeepSeek Harness** | Cordis 一切皆插件；SessionEvent 投影 Trajectory | Trajectory 不变量、Goal disarm、会话内 schedule | **Cordis 核**（会拆掉 capability kernel） |

复杂度：偷原则 S–M；换核 XL 且伤定位。

---

## 3. Phase 2 — GAP Scorecard（0.8.23）

量尺同 `02-comparison-matrix.md`。竞品列沿用 08-02（树未漂）。DeepSeek 为公开文档推断。

| 维度 | 最佳代表 | Carina 0.8.0 | **Carina 0.8.23** | 差距 | 用户价值判断 |
|------|----------|:------------:|:-----------------:|:----:|--------------|
| 功能覆盖 | Jcode/Grok/CC/Codex/OMP 5 | 4 | **4** | P2 | Browser/overnight 非目标；不必追均分 |
| 智能体范式 | Jcode/CC 5 | 4 | **4** | P2 | Loop/subagent/DAG 够用；swarm 不产品化 |
| TUI/审美 | OMP/Jcode/Grok 5 | 4 | **4** | P2 | V001–V011 已合；再抄皮肤无感 |
| 上下文工程 | Jcode/CC 5 | 4 | **4** | P1 | 诚实度已补；缺 compact 一等 cell |
| 扩展性 | Grok/CC/OMP/DSH 5 | 4 | **4** | P2 | `/plugins` 只读库存；marketplace wow 非优先 |
| 资源效率 | Jcode 5 | 3 | **3** | P2 | 诚实 RSS；禁止编造 PSS |
| 治理/安全 | Carina/Codex 5 | **5** | **5** | — | **必须保护** |
| 用户可感 | 终端产品 5 | 4 | **4−** | **P0** | 首轮失败会把 4 打回 3 |
| 产品化/集成 | Grok/CC/Codex 5 | 4 | **4** | P2 | SDK/Gateway 已有；SaaS 非 day-one |
| DeepSeek 治理 | — | — | **2（推断）** | — | 无特权核，与 Carina 正交 |

---

## 4. Phase 3 — SWOT + 过滤

**S**：kernel+audit+事务 patch；0.8 日用壳；honesty 程序把 noop/cache/HITL 说真话。  
**W**：隔离 CLI 握手脆；TUI 巨石；无 Jcode RAM 叙事。  
**O**：Trajectory 检视、compact cell、Goal disarm。  
**T**：抄 Cordis/yolo/pager 稀释定位；未安装的握手修复让用户以为「产品办不了事」。

**看起来很酷、多数用户无感（降级/放弃）**：3D idle、Buddy、百主题、1000-agent swarm、Creator 热挂插件、ACP 当聊天协议、SaaS 多租户控制台。

---

## 5. Phase 4 — 分类清单

### GAP（真缺、用户会感到）

| ID | 项 | P |
|----|----|---|
| G23-03 | Compact 不是一等 transcript cell | **SHIPPED**（`ContextCompacted` → `compact:{event_id}` live=replay） |
| G23-04 | Goal/续跑是否 unload 后仍 armed | **SHIPPED**（`auto_continue` 不落盘；启动 disarm，不 reconcile 续跑） |

### WIP

- Native scrollback **partial**（`CAPABILITIES.md` #11）
- Semantic memory / HMS：默认 off，BYOK 才有

### TODO

- R-01 回归铁门进 release companion
- 可选：DeepSeek clone 后再写 `01-dna-deepseek.md`

### BUG

- 无未修产品 bug 挂账；Mox 503 是上游环境

### LEGACY

- `~/.carina/daemon.sock` 全局布局（显式 mode）

### SLOP

- `app/mod.rs` 12225 行、`render.rs` 16778 行
- `go/worker` 头注释「Phase 3 remote」过期

### ISOLATED

- Grok ACP 私有线、Gateway pin、Nebutra sync、HMS、swarm_channel、xai-ratatui forks

### UNPRODUCTIZED

- best-of-n、swarm 通道、VS Code Marketplace、hosted Web Operator、HMS

**已闭合（不要当缺口重开）**：ISSUE-001…018、V001–V011、R-02 `/queue`、#28–#39 honesty、G23-01/G23-02（`670e5c5`）。

---

## 6. Phase 5 — 路线图

### P0-A / P0-B · **SHIPPED** `670e5c5`

握手 + `provider_attempts`。TUI 需重开。

### P0 · Compact 一等 cell（2–4 人天）

- **价值**：长会话看见「压缩了什么」。  
- **参考**：OMP `type:compaction`；Claude collapse；Carina `ContextCompacted` receipt。  
- **路径**：投影已有 receipt → SemanticCellKind，禁止假装 contextengine 压缩。  
- **验收**：live=replay；`engine=noop` 不出现假 savings。

### P1 · Goal disarm · **SHIPPED**（工作树）

- **价值**：重启不会偷偷续跑。  
- **参考**：DSH goal activation 不持久化。  
- **路径**：`auto_continue` 只活在当前 daemon；`goals.json` 不写该位；启动 `disarmPersistedGoalActivation`，不再 `recoverAutoGoals` 扫 terminal 任务。

### P1 · R-01 铁门（持续 M）

- golden 80/120/160+CJK + ScreenMode PTY + steer 长工具。不重开 ISSUE-001…018。

### P2 · 巨石拆分 / RAM 文化

- 只随功能顺手拆；禁止为审美重写 pager。

---

## 7. 审美与 TUI 规范（已有法律，不新发明皮肤）

1. `docs/brand/AGENTS.md` + `design-tokens.json` 权威。  
2. `crates/carina-tui/styles.md`：transcript ≤3 饱和色。  
3. `VOICE.md`：结果语言；截断必有逃生口。  
4. `theme.rs` / `glyphs.rs`：token + 宽锁定；`auto|unicode|nerd|ascii`。  
5. 失败/diff/审批永不被 collapse 藏住。  
6. 新 cell 必须 live=replay。  
7. 偷原则不偷像素：无 π、无 GrokNight 整抄、无 Buddy。

---

## 8. 风险与 Trade-off

| 风险 | 防护 |
|------|------|
| 为对等终端聊天产品堆装饰 | 只做握手/诚实/compact cell |
| 默认 yolo 抢转化 | presets 是标签；`/always-approve` 显式 |
| Cordis/ACP/SaaS 看起来「更现代」 | 与 `docs/product.md` 定位冲突；一票否决 |
| 未安装的修复让审计变成空文 | P0-A 必须先于新 epic |
| TUI 巨石继续胀 | 铁门 + 顺手拆，不开 XL 重写 |

**置信度**：Carina 路径高；竞品抽查高（版本未漂）；DeepSeek 中（无本地树）；Goal disarm 已对照 `goals.go`（高）。
