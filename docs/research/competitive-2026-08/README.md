# Competitive Research 2026-08 — Artifact Index

> 取证日期：2026-08-02  
> **现状刷新：2026-08-19 @ Carina v0.8.25** — 见 **[14-post-0.8.25-refresh.md](./14-post-0.8.25-refresh.md)**  
> 范围：Carina vs Jcode / Grok Build / Claude Code (notes) / Codex / Oh My Pi (OMP) / DeepSeek Harness（公开文档，本机无 clone）  
> 用途：竞品逆向 → GAP → PRD ISSUE → 闭环路线图 → **post-0.8.25 诚实刷新**

---

## 执行入口（先读）

| 文件 | 角色 |
|------|------|
| **[14-post-0.8.25-refresh.md](./14-post-0.8.25-refresh.md)** | **现状真相**：0.8.25 模块图 / G25-01 / 下一刀 |
| [13-post-0.8.24-refresh.md](./13-post-0.8.24-refresh.md) | 历史：0.8.24→0.8.25 Goal×stop + R-01 |
| [12-post-0.8.23-refresh.md](./12-post-0.8.23-refresh.md) | 历史：0.8.23→0.8.24 |
| [07-post-0.8-gap-refresh.md](./07-post-0.8-gap-refresh.md) | 历史：0.8.0 SHIPPED 表（R-02/V00x 已被 12/13 supersede） |
| **[00-MASTER-REPORT.md](./00-MASTER-REPORT.md)** | 08-02 主方案（战略/non-goals 仍有效；分数以 12 为准） |
| **[05-implementation-roadmap.md](./05-implementation-roadmap.md)** | Phase A/B/C 历史路线；执行态见 12 §6 |

---

## 分阶段工件

| # | 文件 | 阶段 | 内容 |
|---|------|------|------|
| 0a | [00-access-map.md](./00-access-map.md) | Phase 0 | 本机竞品根路径与入口模块导航 |
| 0b | [00-carina-baseline.md](./00-carina-baseline.md) | Phase 0 | Carina 源码基线（定位/栈/功能/TUI/痛点） |
| 1a | [01-dna-jcode.md](./01-dna-jcode.md) | Phase 1 | Jcode DNA 卡 |
| 1b | [01-dna-grok-build.md](./01-dna-grok-build.md) | Phase 1 | Grok Build DNA 卡 |
| 1c | [01-dna-claude-code.md](./01-dna-claude-code.md) | Phase 1 | Claude Code DNA 卡（逆向笔记） |
| 1d | [01-dna-codex.md](./01-dna-codex.md) | Phase 1 | OpenAI Codex DNA 卡 |
| 1e | [01-dna-omp.md](./01-dna-omp.md) | Phase 1 | Oh My Pi DNA 卡 |
| 2 | [02-comparison-matrix.md](./02-comparison-matrix.md) | Phase 2 | 九维评分矩阵 + 能力 Y/P/N + 原则/反模式 |
| 3 | [03-gap-swot.md](./03-gap-swot.md) | Phase 3 | Real GAP top-10、SWOT、REJECTED、护城河 |
| 4 | [04-prd-issues.md](./04-prd-issues.md) | Phase 4 | ISSUE-001…018 全量 PRD（P0–P2） |
| 5 | [05-implementation-roadmap.md](./05-implementation-roadmap.md) | Phase 5 | 分阶段闭环计划 |
| M | [00-MASTER-REPORT.md](./00-MASTER-REPORT.md) | Synthesis | 主报告（索引 + 内嵌 Top P0） |
| 7 | [07-post-0.8-gap-refresh.md](./07-post-0.8-gap-refresh.md) | Post-ship | **0.8 GAP 刷新 + SWOT + 残余清单** |
| 8 | [08-screen-mode-residual-slice.md](./08-screen-mode-residual-slice.md) | Residual | ScreenMode 回归铁门 + 操作者可见性 |
| 9 | [09-steer-queue-residual-slice.md](./09-steer-queue-residual-slice.md) | Residual | Steer/interrupt 铁门 + queue inspect 面板 |
| 10 | [10-visual-density-program.md](./10-visual-density-program.md) | **UX** | TUI 视觉密度总规划 vs OMP/Jcode/Grok |
| 11 | [11-visual-density-trellis-index.md](./11-visual-density-trellis-index.md) | **UX** | V001–V012 Trellis 索引 |
| 12 | [12-post-0.8.23-refresh.md](./12-post-0.8.23-refresh.md) | 0.8.23 | 历史：握手/compact/Goal disarm |
| 13 | [13-post-0.8.24-refresh.md](./13-post-0.8.24-refresh.md) | 0.8.24 | 历史：Goal disarm / Esc |
| 14 | [14-post-0.8.25-refresh.md](./14-post-0.8.25-refresh.md) | **0.8.25** | 发版后复验 + G25-01 TUI Goal 暂停文案 |

---

## 竞品源码根（access-map 摘要）

| 产品 | Root（本机） |
|------|----------------|
| Carina | `/Users/tseka_luk/workspace/code/personal/nebutra/carina` |
| Jcode | `/Users/tseka_luk/workspace/assets/references/competitors/jcode` |
| Grok Build | `/Users/tseka_luk/workspace/assets/references/competitors/grok-build` |
| Codex | `/Users/tseka_luk/workspace/assets/references/competitors/codex` |
| OMP | `/Users/tseka_luk/workspace/assets/references/competitors/oh-my-pi` |
| Claude Code | `/Users/tseka_luk/workspace/assets/references/claude-code-notes`（笔记，非 dump） |

路径迁移时先更新 `00-access-map.md`。

---

## Trellis 对齐（2026-08-03）

**ISSUE-001…018 全部 `completed`**，归档于 `.trellis/tasks/archive/2026-08/`。  
全量索引：[`06-trellis-issue-index.md`](./06-trellis-issue-index.md)

| 波次 | 内容 |
|------|------|
| 已交付 | 001–018 竞争 backlog（0.7–0.8 产品合入） |
| 残余 | R-01 回归铁门 · R-02 queue inspect · R-03 子 agent 密度 · R-04…R-06 见 `07` |
| 切片 | ScreenMode → `08-screen-mode-residual-slice.md` · Steer → `09-steer-queue-residual-slice.md` |

**禁止**在无回归证据时把 archive 任务改回 `in_progress`。新工作开 residual 任务。

---

## 质量铁律（全目录）

1. 关键主张附 **path → 做什么**  
2. 机制写 **WHAT / WHY / TRADE-OFF**  
3. 区分能力跃迁 vs 过度工程  
4. 审美细节可编码（色/密度/glyph/键位）  
5. 不确定标 **假设/推断 + 置信度**  
6. 正文中文；专有名词与路径保持 English  
7. 不发明路径；不以 stale gap 表为现状  

---

## 建议阅读顺序

1. **`07-post-0.8-gap-refresh.md`**（当前 Carina 分数与 SHIPPED 表）  
2. `00-MASTER-REPORT.md` §0（战略三角 / non-goals 仍有效）  
3. `02-comparison-matrix.md`（含 0.8 Carina 列）  
4. 残余切片 `08` / `09` 若开工 ScreenMode/steer  polish  
5. DNA `01-*` 仅在重审竞品时下钻  

---

*Index generated 2026-08-02 · refreshed 2026-08-03.*
