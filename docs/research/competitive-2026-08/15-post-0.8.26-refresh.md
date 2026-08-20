# Post-0.8.26 Harness Audit Refresh

> **取证日期**：2026-08-20  
> **产品锚点**：`go/product/version.go` → **0.8.26**（`c2bb3c7` / tag `v0.8.26`）  
> **工作树**：未提交 — paste-chip 宽度 `#[test]`；worker/scheduler/model-router/rpc 过期 Phase 注释  
> **方法**：本仓库源码 + `CAPABILITIES.md` + `docs/rpc-api.md` + `docs/product.md` + 本机竞品树抽查（未漂）+ DeepSeek 公开 README（**本机无 clone**）  
> **前序**：`14-post-0.8.25-refresh.md` 已过时，**以本文为现状真相**  
> **未做**：并排 PTY、同机 RAM PSS、DeepSeek 本地行号级 DNA

---

## 0. Executive Summary

Carina **0.8.26** 把诚实程序收口：握手、`provider_attempts`、compact cell、stale Grok cache、Goal 进程内 disarm、Esc/Ctrl-C 暂停 Goal、TUI 说出「已暂停目标」、R-01 具名铁门，均已发版。竞品树相对 08-18 **仍无漂移**。

**没有 P0，没有新的用户可感 P1。** 再开功能清单或抄皮肤，用户感知为零。剩下的是 P2 防回潮（巨石、未跑的测试、过期注释）和环境 503。SaaS / ACP 当聊天协议仍否决。

### Top 5 优先行动

| # | 行动 | 用户价值 | 状态 |
|---|------|----------|------|
| 1 | 握手 + `provider_attempts` | 首轮能办事、503 计数诚实 | **SHIPPED** 0.8.24 |
| 2 | Compact cell + stale cache + Goal disarm | 压缩可见；重启不偷跑 | **SHIPPED** 0.8.24 |
| 3 | Esc/Ctrl-C 暂停 Goal；cancelled 不续跑 | 停手不偷跑 | **SHIPPED** 0.8.25 |
| 4 | TUI 说出 Goal 暂停 | notice「已暂停目标」 | **SHIPPED** 0.8.26 |
| 5 | **停开新 epic**；只顺手钉契约 | 防回潮，不堆功能 | **下一刀 = 不要刀**（WT：paste-chip `#[test]`） |

**明确不做**：默认 yolo · snapcompact 默认 · MiniLM-on · 3D/Buddy · 整包 Grok pager · Cordis 核 · MCP passthrough · git-only 回滚 · hashline/fuzzy 换掉 `carina-patch` · ACP 当交互协议 · SaaS 多租户 day-one · 为齐功能做完 native scrollback。

---

## 1. Phase 0 — 模块地图（0.8.26）

```text
TUI / CLI / VS Code / Web / SDK / Gateway
    → JSON-RPC
Go：loop · reasoner · compact · MCP · HITL · subagent · workflow · goal
    → Capability API
Rust kernel：policy · patch · audit · WASM · index（不本地算 embedding）
    → Zig tools
```

| 模块 | 分类 |
|------|------|
| loop / 隔离 reasoner / compact cell / ledger / HITL 标签 / `/agents` `/plugins` | **SHIPPED** |
| Goal 落盘 + `auto_continue` 进程内；Esc/Ctrl-C 暂停；TUI `GoalPaused` | **SHIPPED** |
| residual_ux / visual_density 具名铁门 | **SHIPPED** |
| Native scrollback | **WIP** partial（#11）；默认 Fullscreen viewport，不是缺口 |
| HMS | **UNPRODUCTIZED** / 默认 off |
| Gateway pin / Grok ACP / swarm | **ISOLATED** |
| best-of-n、Marketplace、hosted Web、Nebutra sync=off | **UNPRODUCTIZED** |
| MiniLM | **MUST-PROTECT off** |
| TUI 巨石 12243 / 16784 / 8544 | **SLOP** |
| 全局 `daemon.sock` | **LEGACY** |
| paste-chip 宽度测试未挂 `#[test]` | **SLOP→WT 已钉** |
| worker/scheduler「Phase 3」注释 | **SLOP→WT 已改** |

用户视角：产品能办事、停手诚实、压缩可见。须重开 TUI 吃 0.8.26。Mox 503 是上游。再抄竞品皮肤无感。

---

## 2. Phase 1 — 六家（再验，未漂）

| 产品 | 指纹 | 可偷 | 禁止 |
|------|------|------|------|
| Jcode **0.64.2** | 未漂 | intent、soft interrupt | MiniLM 默认 ONNX |
| Grok **`8d69c91f…`** | 未漂 | 副作用三态、plan 硬闸 | 整包 pager、ACP 当主协议 |
| Claude notes **2.1.88** | 未漂 | collapse、3-strike | Buddy、MCP passthrough |
| Codex | 双轴仍在 | 术语、5 行工具摘要 | fuzzy 换事务 patch |
| OMP **17.2.3** | 未漂 | compaction 一等 entry、glyph 三档 | 默认 yolo、snapcompact、hashline |
| DeepSeek | 无 clone | Trajectory、Goal 激活语义 | **Cordis 核** |

复杂度：再偷原则无新用户价值；换核 XL 且伤定位。

---

## 3. Scorecard（0.8.26）

量尺同 `02-comparison-matrix.md`。竞品列 08-02。

| 维度 | 最佳 | 0.8.0 | **0.8.26** | 差距 |
|------|------|:-----:|:----------:|:----:|
| 功能覆盖 | 5 | 4 | **4** | P2 非目标不追 |
| 智能体范式 | 5 | 4 | **4** | P2 swarm 不产品化 |
| TUI/审美 | 5 | 4 | **4** | P2 再抄皮肤无感 |
| 上下文工程 | 5 | 4 | **4** | P2 已诚实 |
| 扩展性 | 5 | 4 | **4** | P2 marketplace 非优先 |
| 资源效率 | Jcode 5 | 3 | **3** | P2 诚实 RSS，不编 PSS |
| 治理/安全 | **5** | 5 | **5** | — 必须保护 |
| 用户可感 | 5 | 4 | **4** | — 无新 P1 |
| 产品化/集成 | 5 | 4 | **4** | P2 SaaS 非 day-one |

---

## 4. SWOT + 过滤

**S** kernel+audit+事务 patch；honesty 程序已闭环。  
**W** TUI 巨石；资源叙事弱于 Jcode。  
**O** 停开 epic，只钉回潮。  
**T** 为交差再造功能；Cordis/ACP/SaaS 稀释定位。

放弃：3D、Buddy、百主题、1000-agent、ACP 当聊天、Lovable/v0 多租户 Chat、为 #11 齐功能做完 scrollback。

---

## 5. 分类清单

### GAP
无未修用户可感缺口。G23–G25、R-01 已闭合。

### WIP
- Native scrollback partial（#11）— **保持**，直到有真实 viewport 债
- HMS 默认 off

### TODO
- 工作树：paste-chip `#[test]` + 过期 Phase 3 注释（可随手 commit）
- 可选 DeepSeek clone

### BUG
- 无运行时产品 bug；Mox 503 上游

### LEGACY
- 全局 `daemon.sock`

### SLOP
- TUI 三巨石（只随功能拆）
- 过期 Phase 3 注释（WT 已改）

### ISOLATED
- Grok ACP、Gateway pin、HMS、swarm

### UNPRODUCTIZED
- best-of-n、Marketplace、hosted Web

**不要重开**：ISSUE-001…018、V001–V011、#28–#39、G23–G25、R-01。

---

## 6. 路线图

### P0 / P1
无。

### P2 · 防回潮（0.2 人天，WT 已做）
- **价值**：大粘贴 chip 宽度契约真正跑进测试；注释不再骗人。  
- **路径**：`paste_chip_layout_uses_display_width_not_backing_text` 补 `#[test]`，用产品 `paste_chip_line`；worker/scheduler 头注释改成现状。  
- **验收**：`cargo test -p carina-tui --lib paste_chip_layout` 绿；residual-ux 不再 unused warning。  
- **不要**：拆 `render.rs`、重写 pager、打开 HMS/MiniLM。

### 明确不做
- ACP 当 UI、SaaS 多租户、native scrollback 齐功能、新 TUI 皮肤。

---

## 7. 审美法律

`docs/brand/AGENTS.md` + tokens；`styles.md` 根帧 `Color::Reset`、transcript ≤3 饱和色；`VOICE.md` 走 `MessageId`；失败/diff/审批不 collapse；新 cell live=replay；无 π、无 GrokNight、无 Buddy。

---

## 8. 风险

| 风险 | 防护 |
|------|------|
| 为审计交差再开 epic | 本文明确「下一刀 = 不要刀」 |
| 默认 yolo / MiniLM-on | 锁 off |
| Cordis/ACP/SaaS | `docs/product.md` 否决 |
| 巨石继续胀 | 铁门具名；只随功能拆 |

**置信度**：Carina 路径高；竞品抽查高（未漂）；DeepSeek 中（无本地树）。
