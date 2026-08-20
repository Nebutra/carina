# Post-0.8.25 Harness Audit Refresh

> **取证日期**：2026-08-19  
> **产品锚点**：`go/product/version.go` → **0.8.26**  
> **工作树**：干净 — G25-01 随 0.8.26 发版  
> **方法**：本仓库源码 + `CAPABILITIES.md` + `docs/rpc-api.md` + `docs/product.md` + 本机竞品树抽查 + DeepSeek 公开 README（**本机无 clone**）  
> **前序**：`13-post-0.8.24-refresh.md` 已过时，**以本文为现状真相**  
> **未做**：并排 PTY、同机 RAM PSS、DeepSeek 本地行号级 DNA

---

## 0. Executive Summary

Carina **0.8.25** 把 Goal×operator-stop 契约补真：Esc 落地暂停 Goal，Ctrl-C 暂停 Goal 且 `cancelled` 不再当失败续跑；R-01 铁门具名进入 `release-preflight` / CI。功能清单债、握手债、续跑偷跑债都已还。竞品树相对 08-18 **仍无漂移**。

**现在没有 P0。** 用户可感短板收成：Goal 暂停在 daemon 是真的，TUI 默认不说；巨石回潮；上游 503。SaaS / ACP 当聊天协议仍否决。

### Top 5 优先行动

| # | 行动 | 用户价值 | 状态 |
|---|------|----------|------|
| 1 | Isolated CLI 握手 + `provider_attempts` | 首轮能办事、503 计数诚实 | **SHIPPED** 0.8.24 |
| 2 | Compact 一等 cell + stale Grok cache + Goal disarm | 压缩可见；`hi` 不死；重启不偷跑 | **SHIPPED** 0.8.24 |
| 3 | Esc 暂停 session Goal | 文档与 interrupt 落地一致 | **SHIPPED** 0.8.25 `4a8acb1` |
| 4 | Ctrl-C 暂停 Goal；`cancelled` 不续跑 | 硬取消不再偷跑 | **SHIPPED** 0.8.25 `69a8e7d` |
| 5 | **Goal 暂停投影到 TUI notice/overlay** | Esc/Ctrl-C 后用户看见「已暂停目标」 | **SHIPPED** 0.8.26 |

**明确不做**：默认 yolo · snapcompact 默认 · MiniLM-on · 3D/Buddy · 整包 Grok pager · Cordis 核 · MCP passthrough · git-only 回滚 · hashline/fuzzy 换掉 `carina-patch` · ACP 当交互协议 · SaaS 多租户 day-one。

---

## 1. Phase 0 — 模块地图（0.8.25）

```text
TUI / CLI / VS Code / Web / SDK / Gateway
    → JSON-RPC
Go：loop · reasoner · compact · MCP · HITL · subagent · workflow · goal
    → Capability API
Rust kernel：policy · patch · audit · WASM · index（不本地算 embedding）
    → Zig tools
```

| 模块 | 路径 | 分类 |
|------|------|------|
| Agent loop / 隔离 reasoner / compact cell / ledger / HITL 标签 | | **SHIPPED** |
| Goal 记录 + `auto_continue` 进程内 | `goals.go` | **SHIPPED** |
| Goal × Esc | `pauseForSoftInterrupt` → `pauseActiveGoal(..., "soft_interrupt")` | **SHIPPED** |
| Goal × Ctrl-C | `handleTaskCancel` → `pauseActiveGoal(..., "operator_cancelled")`；reconcile 忽略 `cancelled` | **SHIPPED** |
| Goal × TUI 文案 | live `GoalChanged` paused → `GoalPaused` notice；开着的 overlay `goal_get` | **SHIPPED**（WT） |
| residual / density 铁门 | `run_gate residual_ux` / `visual_density`；CI residual-ux | **SHIPPED** |
| Native scrollback | `CAPABILITIES.md` #11 | **WIP** partial |
| Gateway pin / Grok ACP / HMS / swarm | | **ISOLATED** |
| best-of-n、Marketplace、hosted Web、Nebutra sync=off | | **UNPRODUCTIZED** |
| MiniLM | | **MUST-PROTECT off** |
| TUI 巨石 12225 / 16784 / 8544 | | **SLOP** |
| `~/.carina/daemon.sock` | | **LEGACY** |
| `go/worker`「Phase 3 remote」注释 | | **SLOP** |

用户视角：压缩看得见；重启/Esc/Ctrl-C 不偷跑。Esc 后 notice 仍只说「安全边界暂停」，不说 Goal。须重开 TUI 才能吃到 0.8.25。Mox 503 是上游。

---

## 2. Phase 1 — 六家（2026-08-19 再验，未漂）

| 产品 | 指纹 | 可偷 | 禁止 |
|------|------|------|------|
| Jcode **0.64.2** | 未漂 | intent、soft interrupt 协议 | MiniLM 默认 local ONNX |
| Grok **`8d69c91f…`** | `SOURCE_REV` 未漂 | 副作用三态、plan 硬闸 | 整包 pager、ACP 当主协议 |
| Claude notes **2.1.88** | 56 篇未漂 | collapse、3-strike | Buddy、MCP passthrough |
| Codex | `AskForApproval` ⊥ sandbox | 双轴术语、5 行工具摘要 | fuzzy patch 换事务 patch |
| OMP **17.2.3** | 未漂 | compaction 一等 entry、glyph 三档 | 默认 yolo、snapcompact、hashline 垄断 |
| DeepSeek | 公开 README；无 clone | Trajectory、Goal 激活语义 | **Cordis 核** |

---

## 3. Scorecard（0.8.25）

量尺同 `02-comparison-matrix.md`。竞品列 08-02（树未漂）。

| 维度 | 最佳 | 0.8.0 | **0.8.25** | 差距 |
|------|------|:-----:|:----------:|:----:|
| 功能覆盖 | 5 | 4 | **4** | P2 |
| 智能体范式 | 5 | 4 | **4** | P2 |
| TUI/审美 | 5 | 4 | **4** | P2 |
| 上下文工程 | 5 | 4 | **4** | P2 |
| 扩展性 | 5 | 4 | **4** | P2 |
| 资源效率 | Jcode 5 | 3 | **3** | P2 |
| 治理/安全 | **5** | 5 | **5** | — |
| 用户可感 | 5 | 4 | **4** | **P1**（daemon 已真；TUI 默认不说 Goal 暂停） |
| 产品化/集成 | 5 | 4 | **4** | P2；SaaS 非目标 |

---

## 4. SWOT + 过滤

**S** kernel+audit+事务 patch；honesty 覆盖 noop/cache/HITL/compact/activation/operator-stop。  
**W** TUI 巨石；GoalChanged 无 TUI 投影。  
**O** Esc/Ctrl-C 落地后用已有 `GoalPaused` copy + 刷新 overlay。  
**T** 抄 Cordis/yolo/pager/SaaS；未重开 TUI 像没发版。

放弃：3D、Buddy、百主题、1000-agent、ACP 当聊天、Lovable/v0 多租户 Chat。

---

## 5. 分类清单

### GAP

| ID | 项 | P | 证据 |
|----|----|---|------|
| **G25-01** | Goal 暂停后 TUI 不说 | **SHIPPED**（WT：live `GoalChanged` → `GoalPaused`；不进 transcript） |

已闭合：G23-01…04、G24-01、G24-02、R-01、ISSUE-001…018、V001–V011、#28–#39。

### WIP
- Native scrollback partial（#11）
- HMS 默认 off

### TODO
- G25-01
- 可选 DeepSeek clone → `01-dna-deepseek.md`

### BUG
- 无运行时产品 bug；Mox 503 上游

### LEGACY
- 全局 `daemon.sock`

### SLOP
- `app/mod.rs` 12225、`render.rs` 16784、`i18n.rs` 8544
- `go/worker` / `go/scheduler` 过期 Phase 3 注释（工作树已改）

### ISOLATED
- Grok ACP、Gateway pin、HMS、swarm

### UNPRODUCTIZED
- best-of-n、Marketplace、hosted Web

---

## 6. 路线图

### P1 · G25-01 TUI 说出 Goal 暂停 · **SHIPPED**（工作树）

- **价值**：Esc/Ctrl-C 之后用户看见「已暂停目标」，不必猜 `/goal`。  
- **参考**：已有 `GoalPaused` i18n；`GoalChanged` 事件已在 audit。OMP/CC 把状态变化写进 notice，不新发明 cell。  
- **路径**：live 消费 `GoalChanged` 且 `status=paused` 且 action 来自 interrupt/cancel → `Notice::localized(GoalPaused)`；若 `Overlay::Goal` 开着则 `goal_get` 刷新。不改 3 饱和色，不新 SemanticCellKind。  
- **不要**：TUI 再打 `goal.pause`（双写）；不要把 Goal 做成第四色 cell。  
- **验收**：有 active Goal 的 session，soft interrupt 落地后 notice 含 GoalPaused 文案；开着的 Goal overlay 显示 `paused`；无 Goal 时仍只有 SoftInterruptRequested。  
- **回滚**：只撤 event→notice 挂钩。

### P2 · 巨石 / native scrollback / RAM
- 只随功能拆；#11 保持 partial 直到有用户可感 viewport 债；禁止重写 pager。

### 否决 Wave 6
- ACP 当 UI、SaaS 多租户。Gateway 单 workspace pin。

---

## 7. 审美法律

`docs/brand/AGENTS.md` + tokens；`styles.md` 根帧 `Color::Reset`、transcript ≤3 饱和色；`VOICE.md` 走 `MessageId`；失败/diff/审批永不 collapse；新 cell live=replay；无 π、无 GrokNight、无 Buddy。

---

## 8. 风险

| 风险 | 防护 |
|------|------|
| 为聊天产品堆装饰 | 只投影已有 GoalPaused copy |
| 默认 yolo | presets 是标签 |
| Cordis/ACP/SaaS | `docs/product.md` 一票否决 |
| 未重开 TUI | 0.8.25 对旧 epoch 不可见 |
| 巨石继续胀 | 铁门已具名；不开 XL 重写 |

**置信度**：Carina 路径高（已对照 `pauseActiveGoal` / TUI slash vs Esc copy）；竞品抽查高（版本未漂）；DeepSeek 中（无本地树）。
