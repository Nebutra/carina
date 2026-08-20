# Post-0.8.27 Harness Audit Refresh

> **取证日期**：2026-08-20  
> **产品锚点**：`go/product/version.go` → **0.8.29**  
> **工作树**：干净 — 失败格单语 + patch 头注释随 0.8.29 发版  
> **方法**：本仓库源码 + `CAPABILITIES.md` + 本机竞品树（未漂）+ DeepSeek 公开 README（无 clone）  
> **前序**：`15-post-0.8.26-refresh.md` 过时，**以本文为现状真相**  
> **未做**：并排 PTY、同机 RAM PSS、DeepSeek 行号级 DNA

---

## 0. Executive Summary

Carina **0.8.27** 已把诚实程序和防回潮卫生发完：握手、compact cell、Goal 全链路、TUI GoalPaused、R-01 铁门、paste-chip 宽度契约、过期 Phase 注释。竞品树相对 08-18 **未漂**。

**没有 P0，没有用户可感 P1。** 再开 epic 或抄皮肤无感。SaaS / ACP 当聊天仍否决。

### Top 5

| # | 行动 | 状态 |
|---|------|------|
| 1 | 握手 + `provider_attempts` | **SHIPPED** 0.8.24 |
| 2 | Compact cell + Goal disarm | **SHIPPED** 0.8.24 |
| 3 | Esc/Ctrl-C 暂停 Goal | **SHIPPED** 0.8.25 |
| 4 | TUI 说出 Goal 暂停 | **SHIPPED** 0.8.26 |
| 5 | paste-chip 契约 + 过期 Phase 注释 | **SHIPPED** 0.8.27；`carina-patch` 头注释 WT |

**明确不做**：yolo、snapcompact、MiniLM-on、3D/Buddy、整包 pager、Cordis、MCP passthrough、git-only 回滚、hashline 换事务 patch、ACP 当协议、SaaS、齐功能 scrollback。

---

## 1. Phase 0 — 模块地图（0.8.27）

TUI/CLI/VS Code/Web/SDK/Gateway → JSON-RPC → Go loop/reasoner/compact/goal → Rust kernel → Zig。

| 分类 | 模块 |
|------|------|
| **SHIPPED** | loop、隔离 reasoner、compact cell、ledger、HITL、Goal 全链路、R-01、paste-chip 测试 |
| **WIP** | scrollback #11 **保持 partial**；HMS 默认 off |
| **SLOP** | 巨石 12243/16784/8544；crate 过期 Phase 句（WT 已改 patch） |
| **LEGACY** | 全局 `daemon.sock` |
| **ISOLATED** | Grok ACP、Gateway pin、HMS、swarm |
| **UNPRODUCTIZED** | best-of-n、Marketplace、hosted Web |
| **MUST-PROTECT off** | MiniLM |

用户视角：能办事、停手诚实、压缩可见。须重开 TUI。503 上游。

---

## 2. 六家（未漂）

| 产品 | 指纹 | 可偷 | 禁止 |
|------|------|------|------|
| Jcode **0.64.2** | 未漂 | intent、soft interrupt | MiniLM 默认 ONNX |
| Grok **`8d69c91f…`** | 未漂 | 副作用三态、plan 硬闸 | 整包 pager、ACP 当主协议 |
| Claude notes **2.1.88** | 未漂 | collapse、3-strike | Buddy、MCP passthrough |
| Codex | 双轴仍在 | 术语、5 行摘要 | fuzzy 换事务 patch |
| OMP **17.2.3** | 未漂 | compaction 一等 entry | yolo、snapcompact、hashline |
| DeepSeek | 无 clone | Trajectory | **Cordis 核** |

---

## 3. Scorecard

| 维度 | 0.8.27 | 差距 |
|------|:------:|:----:|
| 功能 / 范式 / TUI / 上下文 / 扩展 / 集成 | **4** | P2 |
| 资源效率 | **3** | P2 诚实 RSS |
| 治理 | **5** | — |
| 用户可感 | **4** | **无新 P1** |

---

## 4. SWOT

**S** kernel + honesty 闭环。  
**W** 巨石。  
**O** 停开 epic。  
**T** 为审计交差再造功能。

放弃：3D、Buddy、百主题、1000-agent、ACP 当聊天、Lovable/v0 多租户。

---

## 5. 分类清单

**GAP**：无。  
**WIP**：#11 保持 partial；HMS off。  
**TODO**：无产品项。  
**BUG**：无；503 上游。  
**LEGACY**：全局 sock。  
**SLOP**：巨石；过期 Phase 句。  
**ISOLATED / UNPRODUCTIZED**：同前。

不要重开 001–018、V001–V011、#28–#39、G23–G25、R-01。

---

## 6. 路线图

**P0 / P1：无。**

**P2**：只随功能拆巨石；crate 头注释说真话（本切片）。不要拆 pager。

**否决**：ACP 当 UI、SaaS、齐功能 scrollback、新皮肤。

---

## 7. 审美法律

品牌 tokens；根帧 `Color::Reset`；transcript ≤3 饱和色；`MessageId`；失败/diff/审批不 collapse；live=replay；无 π、无 GrokNight、无 Buddy。

---

## 8. 风险

为交差再开 epic → 本文明确无下一把产品刀。Cordis/ACP/SaaS 一票否决。

**置信度**：Carina 高；竞品未漂；DeepSeek 中。
