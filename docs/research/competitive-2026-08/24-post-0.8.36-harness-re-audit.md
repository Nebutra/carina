# Carina 智能体 Harness 再审计（v0.8.36）

> **取证日期**：2026-08-20  
> **产品锚点**：`go/product/version.go` → **0.8.36**（`586a7a7` / tag `v0.8.36`）  
> **角色**：Senior Agent Harness Architect + Product Auditor  
> **方法**：本仓库源码实测 + 本机竞品 DNA（`01-dna-*` / 22 Phase 1，树未再 clone）+ 操作者截图债（BRIEF 复读、冷 map、长文表、对此无上文、rewind fork）  
> **前序**：`22` 是 0.8.34 SSOT，已被 0.8.35–0.8.36 部分 supersede。`16` 的「无 P0」勿当现状。审美框以 `18` 为准；长文答案以 `23` 为准（A015/A016 已发）。  
> **本文角色**：0.8.36 全 harness 现状 SSOT。偷 job，不偷像素 / 内核。  
> **未做**：并排 PTY、同机 RSS、DeepSeek clone、活 Grok 会话（须重启 daemon）

---

## 0. Executive Summary

0.8.31–0.8.36 把「能回话」升级成了「同一会话能接着说、rewind 能分叉、表不再戳进滚动条」。S8 把 live constitution（无 F）从 ~1365 tok 压到 <800 tok；S7 把 constitution 拆成命名 A–D，Anthropic 分段 `cache_control`。还没过的日用标准是：**REQUESTED skills 仍进 Catalog（S10）**，以及 **打开仓库后 map 要等第一次 `code.map` 才建**。

「功能能跑」不是 pass。用户愿意每天打开 = 对准意图、上文还在、rewind 不丢页、少付 token 税、输入行不比答案吵。

### Top 5（未发、且用户会感到）

| # | 行动 | 优先级 | 用户一句话 |
|---|------|:------:|------------|
| 1 | **S10** `REQUESTED SKILLS` 出 Catalog、进 VolatileSuffix | **P0** | `$pdf` 不要打爆 prefix cache |
| 2 | **Index T1** 项目范围内闲时预热结构图 | **P1** | 「分析这个 repo」不要 0/702 才开工 |
| 3 | **A107** paste/`@` chip 用 live Theme，muted pill | **P1** | 输入行别比答案更氰 |
| 4 | **P1-C1** compact 后 build/plan 重读 AGENTS.md | **P1** | 长会话别丢项目法 |
| 5 | **A017** 对话栏标题最多一级 accent | **P1** | 长文别比 chrome 更吵 |

**0.8.34 之后已发（不再排 P0）：** S9 Intent-Meta；session-dialogue hydrate（0.8.35）；对话表拆箱+溢出截断+muted 列表（0.8.35）；rewind `last_task_id` + checkpoint 回退 + 失败退出选择态 + 子 session 继承上文（0.8.36）；**S8 一行 builtins / 协议 C≤5 / constitution 无 F <800 tok**；**S7 命名 A–D + Anthropic 分段 cache**。

**明确不做：** 默认 yolo · snapcompact · MiniLM 默认开 · 3D/Buddy · 整包 Grok pager · Cordis · MCP passthrough · git-only 回滚 · hashline 换事务 patch · Codex fuzzy 当权威 · ACP 当交互协议 · SaaS 多租户 day-one · 新 TUI 皮肤 · utterance 分类器 · 在 `/` 或 `$HOME` 自动索引。

---

## 1. Phase 0 — 模块地图（0.8.36）

```
TUI / CLI / VS Code / Web / SDK
        │  JSON-RPC
        ▼
Go daemon  loop · reasoner · promptcache · compact · session-dialogue · fork
        │  kernel.request（stdio，Call 全程持 mu）
        ▼
Rust kernel  Capability + audit + transactional patch
        ▼
Zig  scan / grep / diff / run / pty / patch-native
```

### 入口

| 面 | 路径 | 状态 |
|----|------|------|
| CLI | `apps/carina-cli` | SHIPPED |
| TUI | `crates/carina-tui` | SHIPPED。独立 input 线程；16 ms；默认 Fullscreen |
| Daemon | `apps/carina-daemon` + `go/daemon` | SHIPPED。会话权威 |
| Kernel | `crates/carina-kernel` | **MUST-PROTECT** |
| Worker / VS Code / Web / SDK | `apps/carina-worker`、`integrations/*`、`sdk/*` | Worker/SDK SHIPPED；VS Code/Web **UNPRODUCTIZED** 托管 |
| Gateway | `gateway_http.go`、`go/rpc/websocket.go` | **ISOLATED**（token+pin，不是租户 SaaS） |

### Runtime（相对 22 的变化）

| 模块 | 今天 | 分类 |
|------|------|------|
| `prompt_mode.go` | 只按 agent 名灌 AGENTS.md | SHIPPED 0.8.34 |
| `session_dialogue.go` | 新 run 灌先前 Operator + `done.summary`；最近一对 pinned | **SHIPPED 0.8.35** |
| `fork_boundary.go` | `last_task_id`/`last_run_id`；无 ckpt 回退；子 items 继承父对话 | **SHIPPED 0.8.36** |
| `markdown.rs` | 对话表 unboxed；wrap clip；列表 muted | **SHIPPED 0.8.35** |
| `ensureIndex` | 仍 **第一次** `code.*` 才建；&gt;256 后台 16 一批 | **GAP** 预热 |
| `compact` | 0–3 级；无 rebuild F | P1-C1 |
| `go/contextengine` | noop | 不要当 compressor 卖 |
| swarm / HMS / best-of-n | 孤岛 / 默认关 | ISOLATED / UNPRODUCTIZED |

### Live constitution（实测不变）

| 段 | ~tok | live? |
|----|-----:|:-----:|
| `productIdentity` | 114 | 是 |
| `intentFirst` | 211 | 是 |
| `toolsHelp` | **~421** | JSON ReAct 是；native HTTP 被替换；S8 一行 builtins |
| Orchestration | 0 | 已并进 tools 一行（spawn/workflow） |
| **合计** | **~783**（converse constitution 无 F） | S8 目标 &lt;800 → **PASS** |
| `productCapabilityBrief` | 314 | **否** |

### 巨石（SLOP）

`render.rs` 17869 · `app/mod.rs` 12483 · `i18n.rs` 8783 · `grok_reasoner.go` 3361 · `agent.go` 2198。随功能切，不为审计拆。

### 用户视角（源码 + 本轮截图）

已关：问候爬树、全宽用户条、BRIEF 进 constitution、对此无上文、表溢进滚动条、rewind 字段打空、每轮 ~1365 tok 说明书税（S8）。  
仍在：Grok `full()` uncached（诚实 `none`）；`$skill` 打 Catalog（S10）；map 冷启动；chip/slash 氰；compact 后不重读 AGENTS.md；活进程若不重启则看不见本刀。

---

## 2. Phase 1 — 六家（job，不抄产品）

与 22 相同，树未漂。DeepSeek 无 clone。

| 产品 | 架构一句话 | 可偷 3–5 job | 禁止 |
|------|------------|--------------|------|
| Jcode 0.64.2 | Server 拥有 session；split prompt | prefix 稳定；子 agent TLDR；intent 进 schema | MiniLM 默认；3D donut |
| Grok `SOURCE_REV` 8d69c91 | pager ≠ shell；compact 后再注入 | compact-core/host 分离；扩展不劫持 loop；副作用三态 | 整包 pager；ACP 当聊天协议 |
| Claude notes 2.1.88 | `string[]` + boundary；四级压缩后 rebuild | 轻→重 compact + 3-strike；Explore 不灌项目指令 | Buddy；terracotta 当身份 |
| Codex | SQ/EQ；approval ⟂ sandbox | 双轴同名；fragments+cap；Plan 硬掩码 | fuzzy 换精确 span |
| OMP 17.2.3 | hashline；compaction 一等 entry | skill 目录+`skill://`；compact 是 entry；成功 read 塌缩 | 默认 yolo；snapcompact；hashline 垄断 |
| DeepSeek | 无树 | Trajectory（文档级） | Cordis 核 |

Carina 必须保住：kernel、audit 链、事务精确 span、summary-only 子 agent、converse 默认、F 按 mode、TASK 出 cache、Intent-Meta、session-dialogue、rewind 边界。

---

## 3. Phase 2 — Scorecard

| 维度 | 最佳代表 | Carina 0.8.36 | 差距 | 用户价值 |
|------|----------|:-------------:|:----:|----------|
| A 功能/交互 | Codex fork；OMP 发现面 | **4.5** rewind 已能分叉；slash 全 | P2 审美 | rewind 刚从 P0 关掉 |
| B 性能/资源 | Jcode RAM；Grok inspect 复用 | **4.5** H1–H7 + S8 + S7 已发 | **P0=S10** | skill 目录打 cache |
| C 上下文 | Claude 四级+rebuild | **4.5** 对话跨 run 已 hydrate；F 不重建 | **P0=S10**；P1=C1 | 长会话保真 |
| D 范式 | Claude AgentTool | **4** swarm 孤岛 | P2 | |
| E 扩展 | OMP skill index | **4** implicit 默认关 | P2 | |
| F TUI | Codex cells；Grok 不装箱 | **4.5** 短聊+表拆箱已发 | **P1** chip/标题 | 长文 A017 仍开 |
| G SaaS | Codex app-server | **2** | **不排 P0** | 先本机 |
| 治理 | Carina | **5** | — | 护城河 |
| **用户可感** | — | **~7 / 10** | 结构 FAIL 在 S10 | 比 22 的 5.5 升在上文/rewind/表/说明书税/分段 cache |

---

## 4. Phase 3 — SWOT 过滤

**S** kernel 三件套 + Intent-Meta + 跨 run 对话 + rewind 真分叉 + 对话不装箱。  
**W** Catalog 仍跟 `$skill` 走；冷 index；TUI 巨石；Grok cache none。  
**O** S10 稳定 skill 目录；闲时 T1 map（有范围门）。  
**T** 为审计交差再造皮肤 / SaaS / 在 root 扫盘。

放弃：百主题、Buddy、shine 循环、1000-agent、齐功能 scrollback、Lovable/v0 租户、默认索引 `/`。

---

## 5. Phase 4 — 分类清单

**GAP：** S10；Index T1 预热（有范围：项目才建，`/`/`$HOME` 拒绝）。  
**WIP：** scrollback #11 partial；HMS off。  
**TODO：** Top 5 + A108 `/context` 人话、A103 `/changes`、P1-C1 compact 后重建 F、A017 标题层级。  
**BUG：** 无新功能性 P0。活 daemon 不重启是操作债。Grok doctor 是供应商。  
**LEGACY：** 全局 `daemon.sock`。  
**SLOP：** 三巨石 TUI + `grok_reasoner.go`。  
**ISOLATED：** swarm、Gateway、HMS、Grok ACP（适配器）。  
**UNPRODUCTIZED：** best-of-n、Marketplace、hosted Web、多租户 SaaS。

不要重开 ISSUE-001…018、A001–A016（已发者）、rewind 字段。

---

## 6. Phase 5 — 路线图

### P0-S8（已发）

- **价值：** Grok 每轮少 ~500 tok 说明书税。  
- **落地：** 一行 builtins；协议 C 五条；JSON 例子只留测试；spawn/workflow 并进 tools。  
- **验收：** `TestConstitutionWithoutWorkspaceStaysUnder800Tokens`；Fixture G 仍 `hi` → `done`。

### P0-S7（已发）

- **价值：** Anthropic 分段 cache。  
- **落地：** 命名 Mode/Identity/Protocol/Tools；`full()` 仍给 Grok；`cache_control` 上限 4（A–D 优先）。  
- **验收：** `TestConstitutionSectionsAreNamedAndOrdered`；Grok `promptCacheKindFor` 仍 `none`。

### P0-S10（0.5–1 人天）

- **价值：** 问候与 `$name` Catalog 字节相同。  
- **路径：** `buildDynamicSkillPrompt` 稳定目录进 Catalog；REQUESTED 进 suffix。Implicit 保持关。

### P1 Index T1（2–3 人天）

- **价值：** 「分析 repo」第一次 `code.map` 已有 PageRank。  
- **参考：** rust-analyzer/Cursor 先定 project root。  
- **路径：** session/runtime 起来、**首帧之后**、范围=git 根或清单文件；`/`/`$HOME` 不扫；硬顶源文件数；现有 16-file batch + 指纹。Embedding 默认关，勿写进 map coverage。  
- **验收：** 打开本仓库空闲 5s 后 `code.map` 不报 `0/N building`（指纹命中或 T1 完成）。  
- **风险：** 误扫家目录 → 三态范围门。不挡 TTFF。

### P1-C1 / A107 / A017

- C1：仅 build/plan compact 后重读 AGENTS.md（2 人天）。  
- A107：chip `self.theme` muted（1 人天）。  
- A017：对话栏标题最多一级 accent、无 underline（0.5 人天）。

### SaaS（对 Phase 5 提问的回答）

Daemon+SDK+Gateway **已经是可嵌入 backend**。租户隔离、每租户 egress、计费 **不是 day-one**。先 S10 和本机日用。

### 验收总闸

- `go test ./go/daemon -run 'FollowUp|SessionFork|ForkedSessionItems|Greeting|ConversationalRequest'`  
- Fixture G；4-turn 短聊 golden；unboxed table 宽不超过列。  
- 不把 rustfmt 脏文件塞进同一 PR。

---

## 7. 审美法律

`styles.md` + brand tokens。日用对话是产品。用户 pill、答案不装箱、live 一根 `┃`、对话表无 `┌┬┐`、列表 muted、leftover Reset。禁止新皮肤、body shimmer、`Theme::detected(None)`、品牌玫瑰上按钮。A114：void 上对比度不够则保持 elevated `#de859b`。

---

## 8. 风险

| 做错 | 防护 |
|------|------|
| S8 砍到模型不会 JSON | 一行仍含字段名；native 路径 + Fixture G；closing 仍要求 JSON |
| Index 预热扫 `/` | 未定范围直接拒绝 |
| 为 rewind 再堆 phrase 表 | 已用 session-dialogue + checkpoint，禁止 |
| 拆 kernel 锁 | 锁留下 |
| Gateway 说成 SaaS | 本文否决 tenancy |

**置信度：** Carina 源码高；竞品 DNA 沿用 22 高；DeepSeek 中；活 Grok 须重启 0.8.36 后才算验证 rewind。
