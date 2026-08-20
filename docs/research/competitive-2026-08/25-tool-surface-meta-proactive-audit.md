# Carina 工具面 / Meta / Proactive 专项审计（v0.8.37）

> **取证日期**：2026-08-20  
> **产品锚点**：`go/product/version.go` → **0.8.37**（`df8a97d` / tag `v0.8.37`）  
> **角色**：Agent Tool-Surface Architect + Meta-System Auditor + Proactive Agency Designer  
> **方法**：本仓库源码（`go/daemon` dispatch / MCP / spawn / workflow / edit / transcript）+ 本机竞品树（Claude notes 2.1.88、OMP `tools/index.ts`、Grok `xai-grok-tools` SOURCE_REV、jcode 0.64.2、Codex `codex-rs/core/src/tools`）。DeepSeek **无 clone**，只作文档级。  
> **前序**：Harness SSOT 仍是 **[24](./24-post-0.8.36-harness-re-audit.md)**（S7/S8 已随 0.8.37 发）。本文只答工具面 / 消费 / 通道 / meta / 奏折，不重开皮肤 / SaaS / ACP-as-chat。  
> **未做**：并排 PTY 工具时延、活 MCP 市场探测、DeepSeek 行号级 plugin 总线。  
> **收敛（working tree，未发版）：** T-S1 `web.search`、T-S2 真 `todo`/`update_plan`、T-S3 search/list extract、S10 REQUESTED out of Catalog **已落地**。日用剩余 P0 = **无**。奏折 / git 一等 / browser 仍 P1。

**已锁定、本文不建议推翻：** kernel 能力闸 + 哈希链 audit + 事务精确 span；子 agent summary-only；converse 默认；F 按 mode；TASK 出 cache；Intent-Meta；rewind fork；不默认 yolo / MiniLM / snapcompact / MCP passthrough / git-only 回滚 / hashline 垄断 / ACP 当交互协议。

---

## 1. Executive Summary

Carina 的工具面已经能把「读改跑」做成**可审计的闭环**，不是一张空名单。P0 工具面缺口（开放世界入口、任务清单、search/list 灌 token、REQUESTED 打 cache）已收。仍缺的是不吵主对话的旁路准备（奏折，P1）。

| 问题 | 结论 |
|------|------|
| **完整吗？** | **仓库内读写改跑：是。** 开放世界：`web.search`+`web.fetch`（host 批准）。任务外显：真 `todo`。跨文件智能：半完整（`code.*` 真索引，冷启动）。Git-PR / 奏折：**否（P1）。** |
| **SOTA 吗？** | **治理 SOTA**（kernel + 精确 span + fail-closed run）。日用 P0 工具面已对齐 job（search/todo/fetch 成对）。编排 **强于 Codex 轻量面、弱于 Claude AgentTool / OMP task+hub**。 |
| **实用吗？** | **对「改这个 repo」和「先搜再改 / 给个 todo」实用。** 「你先准备着别吵我」仍不实用（奏折 P1）。 |

### Top 5

| # | 行动 | 类 | 优先级 | 用户一句话 |
|---|------|:--:|:------:|------------|
| 1 | **`web.search`**（host 批准，结果当 untrusted data） | A | **P0 landed** | 别让我先自己找 URL 再 `web.fetch` |
| 2 | **真 `todo` / `update_plan`**（dispatch + schema；plan 模式允许） | A | **P0 landed** | plan 模式不要假装有清单 |
| 3 | **search/list 结构化摘要**（路径 + 一行 why） | B | **P0 landed** | 别用 grep 原文塞满下一轮 |
| 4 | **S10 REQUESTED skills 出 Catalog** | B | **P0 landed** | `$pdf` 不要打爆 prefix |
| 5 | **Proactive 奏折通道**（只读准备 + 侧栏提案，默认不进主 transcript） | C | **P1** | 有准备，但不插嘴 |

**不要做的（出现即 FAIL）：** 无门控自改工具面；奏折写入主对话全文；用 `run git` 换掉事务 patch；MCP 直通绕过 kernel；把 `todo` 做成 utterance 分类器；默认开 L4 自演进。

---

## 2. Phase 0 — 参考标尺（偷 job，不偷名单）

六家都把工具分成 **always-on 内核 / 可发现扩展 / 延迟 schema / 人机闸**。没有一家把「32 个 builtins」当完成定义。

| 产品 | 分层 | 扩展总线 | 并行/返回 | Background / proactive | Todo/plan | Git / web / browser | Meta 到哪一级 |
|------|------|----------|-----------|------------------------|-----------|---------------------|---------------|
| **Claude Code** 2.1.88 notes | File / Bash / Agent / MCP / deferred ToolSearch | MCP + plugins + skills 同一 Command 管线 | 流中并行 `tool_use`；大结果落盘引用 | AgentTool fork/swarm；非奏折 | Enter/ExitPlan；AskUserQuestion | Git 跟踪在 shared infra；Web* 一等；无独立 browser 笔记 | L2：deferred 激活 + MCP。无生产 L3 自改工具面 |
| **OMP** `tools/index.ts` | ~32 builtins：read/write/edit/bash/glob/grep/lsp/task/browser/web.search/todo/checkpoint/gh/computer/eval/memory*/manage_skill… | custom tools + skills + MCP + `ManageSkillTool` | in-process rg；task 扇出；yield 结构化收口 | AsyncJob + hub wait/cancel；**不是**静默奏折 | `TodoTool` 一等 | `GithubTool`；`WebSearchTool`；`BrowserTool`/`ComputerTool` | L2–L3 边缘：manage_skill、learn、checkpoint。默认 yolo 子 agent **禁止照抄** |
| **Grok Build** `xai-grok-tools` | bash / read / search_replace / grep / list / web_fetch / **web_search** / todo / task / wait_tasks / kill_task / scheduler / plan / lsp / memory / workflow / image_* | Skills / plugins / hooks / MCP | 子 agent 独立 context；task coordinator | **scheduler + monitor + wait_tasks** 是后台面；非奏折 | `todo/`、`enter_plan_mode` | web_search+fetch 成对；无独立 git tool crate（shell） | L2：skill discovery reminders。调度器是产品级后台，不是 meta 自改 |
| **jcode** 0.64.2 | 「30+ tools」+ MCP pool 在 server | MCP pool 共享；swarm DAG | 低 RAM；schema cache | overnight / ambient gardening（部分 design） | swarm plan graph | 协作通知；web 走 MCP/工具池 | L2：server 拥有 MCP/embedder。MiniLM 默认开 **禁止照抄** |
| **Codex** | 轻量：shell + `apply_patch` + unified_exec；可选 web_search | MCP server 面 + plugins | ToolOrchestrator 审批/sandbox/重试合一 | 无奏折 | Thread Goal + Plan 硬非变异 | apply_patch 一等；web_search **可关** | L1–L2。纪律在 sandbox×approval 正交，不在工具数量 |
| **DeepSeek Harness** | 无本机树 | 文档级 everything-is-plugin | — | — | — | — | 只借「插件可组合」job，不借 Cordis 核 |

### 标尺（全部为真才算工具面 pass）

1. **Essential 短、Discoverable 晚加载。** 内核读写改跑 + 一条开放世界入口；MCP/skill 用 index 不是全 schema。  
2. **专用工具优先于 `run`。** fetch/search/edit 有闸；shell 是逃逸舱。  
3. **返回有预算。** 大结果指针化；子 agent 只回 summary。  
4. **任务状态对模型可见。** todo/plan 是 tool，不是 UI 装饰。  
5. **后台 ≠ 奏折。** wait/cancel 是通道 C；提案不进主 transcript 才是 proactive。  
6. **Meta 默认停在门控。** 注册/自测/回滚可以做；无批准改 production 工具面不行。

Carina 对照（收敛后）：1 过（S8 短 builtins，S10 Catalog 稳）；2 强（edit/patch/web.search+fetch）；3 过（search/list extract + snip + summary-only）；4 过（真 todo dispatch）；5 **FAIL**（有 background run registry，无奏折 — P1）；6 L1–L2。

---

## 3. Phase 1 — 现有工具逐项盘点

源码权威：`go/daemon/agent.go` `dispatchActionOutcome`、`tool_schema.go`、`edit.go`、`subagent.go`、`workflow.go`、`mcp_find.go`、`transcript.go`。

用户清单含 `web.search` / `todo` / `update_plan`。Dispatch 里还有 **未写进 constitution D** 的：`mcp`、`mcp_find`、`best_of_n`（opt-in）、`swarm_publish`/`swarm_receive`（孤岛）。`todo`/`update_plan` **有 dispatch + schema**；plan 模式允许，explore 不在 allow-list。

评级：S 护城河 / A 日用够 / B 能跑但糙 / C 空心或缺。

| 工具 | 输入契约 | 输出契约 | 副作用 | 权限/沙箱 | 可并行? | 失败/超时 | 炸 context? | 参考同类 | 差距 | 评级 |
|------|----------|----------|--------|-----------|---------|-----------|-------------|----------|------|------|
| **list** | 无（workspace 根） | 目录收口 + 样本文件（path + size + lang） | 无 | FileRead | **是**（batch） | 拒绝/IO | 低 | Claude Glob；OMP Glob | T-S3 已落地 | A |
| **read** | path 或 `skill://` | 文件全文；图走 artifact+placeholder | 记 read provenance | FileRead | **是** | IO/DENIED | **高**（大文件靠 2k snip） | FileRead | snip 有；无行范围默认 | A |
| **read skill://** | skill 名 | `<carina_skill>` 框；**不改 ToolNames** | FileRead 审计 kind=skill | safe-mode 关技能 | 否（走 read） | unknown/disabled | 中（skill 体） | OMP skill:// | S10：REQUESTED 出 Catalog | A |
| **search** | pattern | 按文件聚合 path + hits + 一行 why | 无 | FileRead + zig grep | **是** | no matches | 低 | Grep | T-S3 已落地 | A |
| **web.search** | query | 公开检索 hits；untrusted | 出网 | host approval；HTTPS/SSRF fail-closed | 否 | 拒绝/挑战页 | 低 | Grok web_search | T-S1 已落地 | A |
| **web.fetch** | https URL | 文本/JSON；当 untrusted | 出网 | host approval；禁 curl 替代 | 否 | 超时/拒绝 | 中 | Grok web_fetch | 与 web.search 成对 | A |
| **run** | argv | stdout/err | 进程 | CommandExec + OS sandbox fail-closed | 否 | DENIED/timeout | 中（snip） | Bash | 仍是 git/测试/构建的唯一通道 | A |
| **patch** | path+完整内容 | 事务 apply | 写盘+可回滚 | PatchApply；**必须先 read**（新文件例外） | 否 | provenance/政策 | 低 | FileWrite | 与 edit 分工清楚 | S |
| **edit** | path+old+new | 同一事务 apply | 写盘 | 先 read；`old` 必须非空且 **恰好一次**（`materializeEdit`） | 否 | not unique / not found | 低 | FileEdit / Codex apply_patch | 无 hashline；精确 span 是护城河 | S |
| **memory** | target+action+content | 摘要 | 持久记忆 | MemoryWrite | 否 | invalid write | 低（预算在 store） | Grok memory；OMP memory-* | 注入时机仍是 run 开始冻结；converse 不灌 AGENTS | A |
| **ask_user** | prompt；2–6 options 或自由答 | 等 RPC 回答 | 任务 `waiting_input` | 无写盘 | 否 | 超时 | 低 | AskUserQuestion | **阻塞该 run**；无「后台继续」 | B |
| **code.search** | query | 排名命中 | 可能 **第一次建索引** | FileRead 每路径 | **否**（kernel mu + 懒建） | 空索引 | 中 | OMP lsp/search | 冷 0/N | B |
| **code.symbols** | name | def+refs | 同上 | 同上 | 否 | 无符号 | 低 | LSP documentSymbol | 真索引，非空壳 | A |
| **code.map** | 无 | 紧凑排名图 | 同上 | 同上 | 否 | building | **用户体感** | — | **P1 Index T1** 仍开 | B |
| **code.def / refs** | name | LSP 精确（若有）否则索引 | 握手失败降级 | 同上 | 否 | lsp-handshake | 低 | OMP lsp | 诚实降级 | A |
| **code.impact** | name | 有界传递依赖 | 同上 | 同上 | 否 | 无名 | 低 | — | 有；未进并行 batch 是对的 | A |
| **spawn** | agent+task 或 tasks[] | **仅 child `done.summary`** | 子 session；可 worktree | SubagentSpawn；child ⊆ parent；depth≤4 | 扇出并行 | DENIED/cancel | 低（summary-only） | AgentTool / OMP task | 父看不到子进度；无 wait 工具 | A |
| **workflow** | 名 + 可选 input | 每步 summary 截 400 | DAG；顶层 only | PluginLoad | 独立步并行 | cancel/error | 中 | Grok workflow | 部分成功语义在 DAG 实现；模型看不到 run 句柄 | A |
| **done** | summary | 结束 run | 无 | 永不被 allow-list 挡住 | — | — | 低 | end_turn | **无产物清单/验收字段**（structured output 是另闸） | B |

### 特别检查（强制）

| 检查 | 源码事实 | 判定 |
|------|----------|------|
| edit 先 read + span 唯一 | `checkWriteProvenance`；`bytes.Count==1` | **PASS** |
| patch vs edit | 全文 vs 唯一 span；都进 `proposeAndApplyPatch` | **PASS** |
| run fail-closed | 缺 sandbox helper 失败；kernel CommandExec | **PASS**（路径要保持，禁止用 run 冒充 patch） |
| skill:// prompt-only | `readSkillURI` 只返回 framed 文本，不改 RestrictedTools | **PASS** |
| code.* 是否壳 | `ensureIndex` + kernel.index + LSP write-through（v3/v4 测试） | **真能力，懒建** |
| spawn 回传 | 注释与实现对齐：isolated + single-channel summary | **PASS** |
| workflow 取消 | `context.Canceled` → cancelled | **PASS**；缺模型侧 `workflow.status` 工具 |
| memory 治理 | MemoryWrite 决策 + frozen snapshot | **PASS**；检索仍非每轮 N→N+1 |
| ask_user 阻断 | `waiting_input` + channel 等回答 | **会挡该 run**；不是全局死锁 |
| done 验收 | 无内建 checklist | **GAP B** |
| todo / update_plan | dispatch + schema；plan 允许、explore 否 | **PASS**（T-S2） |

---

## 4. Phase 2 — 六类任务走查

卡点分类：A 缺工具 / B 消费差 / C 缺通道 / D 缺 meta。

### 1) 单文件 bug fix

- **理想：** map/search → read 范围 → edit 唯一 span → run 测试 → done。  
- **Carina：** 能走通。edit 护城河在这里发光。  
- **卡点：** 测试只能 `run`（可接受）。list 过大时 B。  
- **体感：** 利索。不是本审计的失败场景。

### 2) 跨文件重构

- **理想：** code.impact → refs → 并行 read → 多次 edit → 测试。  
- **Carina：** impact/refs 有。并行 **不能** 带 `code.*`（kernel `Call` 持 mu + 懒索引）——这是正确的工程约束，不是偷懒。  
- **卡点：** B 冷索引；B 多次 serial read 可 batch，模型不一定发 batch。  
- **体感：** 第一次 `code.map` 像「0/702」。Index T1 仍是 P1。

### 3) 从零小功能 + 测试

- **理想：** 读约定 → patch 新文件 → run test → 失败则 edit。  
- **Carina：** 新文件 patch 不需要旧 span。  
- **卡点：** git 一等仍缺，提交靠 `run git`（A，P1）。todo 已落地。  
- **体感：** 能做完；进度可盯 session checklist。

### 4) 调研外部文档/API 再改代码

- **理想：** web.search → web.fetch（untrusted）→ 再 edit。  
- **Carina：** `web.search` + `web.fetch`（host 批准，untrusted）。constitution 禁止 `run curl`。  
- **卡点：** Browser 非必需（P1），除非验证本机 docs 站。  
- **体感：** 有 query 可搜再 fetch。

### 5) 多子任务并行

- **理想：** spawn tasks[] 或 workflow DAG；父等 summary；可 list/wait。  
- **Carina：** spawn 扇出 + workflow 独立步并行 **有**。父 run 同步等子结束。RPC 有 background task registry（`task submit mode=background`、`task.list`），**不是模型工具**。  
- **卡点：** C 模型看不见 job id/wait/cancel。  
- **体感：** 能扇出；不能「你去跑，我继续问」。

### 6) 长会话恢复

- **理想：** session-dialogue + memory + rewind + compact 后重读 F。  
- **Carina：** 跨 run hydrate、rewind fork（0.8.36）**已发**。Memory snapshot 冻结在 run 始。Compact 后重读 cited files（P1-C1）；converse 仍不灌 AGENTS。  
- **卡点：** B/C 压缩重建；D 无失败轨迹→prompt 补丁。  
- **体感：** 「对此你怎么看」已修；长 build 仍可能丢项目法。

---

## 5. Phase 3 — 评分卡

| 维度 | 分 | 证据 | 主要 GAP | 优先级 |
|------|---:|------|----------|--------|
| 仓库内读写改跑闭环 | **8.5** | edit 唯一 span + patch 事务 + run fail-closed | 测试/构建只能 run | — |
| 代码智能 | **7** | 真 index/LSP/impact | 冷启动；code.* 不能进 batch | P1 T1 |
| 编排 spawn/workflow | **7.5** | summary-only；DAG；depth 闸 | 无模型侧 wait | P1 |
| 人机 ask_user/done | **7.5** | 结构化问题 + 真 todo | done 无产物清单 | — |
| 开放世界 | **6.5** | web.search+fetch；禁 curl | 无 browser | P1 |
| 扩展总线 | **7.5** | MCP `mcp_find` + 短 index；S10 Catalog 稳 | 用户不觉得是总线 | — |
| 任务外显 | **7** | todo/update_plan dispatch | 无奏折 | P1 |
| Git/PR | **4** | `/changes` 用 git status；模型靠 run | 无 git 一等工具 | P1 |
| 工具消费 | **7.5** | 2k snip；list/read/search extract+batch；summary-only | 全量 builtins 仍在 D（一行） | — |
| Proactive 旁路 | **2** | 无提案通道 | 只有显式 spawn | P1 |
| Meta 可演进 | **3.5** | skills/MCP/agent specs 配置化 | 无注册自测/金丝雀 | P2 |
| **总体实用** | **8** | 改本仓 + 搜网 + todo 够用 | 旁路/git/browser 仍 P1 | |
| **总体 SOTA（日用 P0）** | **7.5** | 治理 + search/todo/fetch 成对 | 相对 Claude/OMP 缺奏折与模型侧 job | |

**完整？** 本仓闭环完整；搜网与计划 P0 已补；旁路（奏折）仍缺。  
**SOTA？** 治理是。日用 P0 工具面已收。奏折/git 不是。  
**实用？** 对「改仓库 / 先搜再干 / 给个 todo」是。对「你先准备着别吵我」不是。

---

## 6. Phase 4 — Broken patterns（出现即 FAIL）

对照清单，**只标 Carina 现状**。已修的不重开。

### 4.1 Missing / 错误抽象

| # | 模式 | Carina | 处置 |
|---|------|--------|------|
| 1 | 只有 fetch 无 search | **PASS**（T-S1 `web.search`） | 保持 untrusted + 禁 curl |
| 2 | 无 MCP/plugin 总线 | **部分 PASS**（MCP+WASM 有；体验弱） | 强化发现，不新造总线 |
| 3 | 无 todo/plan 外显 | **PASS**（T-S2 dispatch） | 不要做成短语分类器 |
| 4 | 无 git 一等 | **FAIL** | P1；**禁止** git-only 回滚 |
| 5 | 无 checkpoint/rewind | **PASS**（0.8.36） | 不重开 |
| 6 | 无 background wait/cancel 工具 | **RPC 有、模型无** | P1 |
| 7 | 无 browser | **有意缺** | 仅 docs/UI 验证时 P1 |
| 8 | 用小工具堆数量 | **未犯** | 保持短 D |

### 4.2 Bad consumption

| # | 模式 | Carina |
|---|------|--------|
| 9 | 可并行却串行 | list/read/search **能**并行；模型常不发 batch。code.* **禁止**并行（对） |
| 10 | 大结果回灌 | snip 2k **有**；search/list extract **有**（T-S3） |
| 11 | 子 agent 回全文 | **PASS** summary-only |
| 12 | 每轮全量 schema | JSON ReAct 一行 builtins；native schema；S10 Catalog 不含 REQUESTED |
| 13 | run 万能锤 | constitution 禁 curl/禁 shell 编辑。**git/测试仍只能 run** |
| 14 | 慢工具堵 loop | kernel mu 是结构。keepalive 已发。Index 冷启动仍堵体感 |
| 15 | 失败无结构 | `toolExecutionOutcome` 有 category；模型看见的多半仍是字符串 |
| 16 | ask_user 过度 | 无分类器强迫提问；仍可能被模型滥用 |

### 4.3 Missing proactive

| # | 模式 | Carina |
|---|------|--------|
| 17–20 | 无奏折、后台只能显式 spawn、结果插主 transcript、无静默策略 | **FAIL C** |

### 4.4 Unsafe meta

| # | 模式 | Carina |
|---|------|--------|
| 21–24 | 空心 meta / 无门控自改 / 无金丝雀 / 不可禁用自演进 | **未宣称 L4**。保持：技能与 MCP 配置化，生产不开放自改工具面 |

---

## 7. Phase 5 — Proactive 奏折 MVP

**定义（用户原话，法律）：** 不是自动乱改。有 Ownership 的旁路根据意图**提前准备**，以奏折提交；**不打断、不污染主对话 agent**。

spawn = 主 agent **显式**委派。  
proactive = **可无显式请求**的旁路准备。  
background job = 执行句柄。奏折是 **提案面**，不是 job 本身。

### 触发（可）

- 同一模块被连续 read/edit ≥ N 次，且存在测试文件未跑。  
- `run` 测试失败且同一命令即将被重试。  
- 操作者在 **build** 且任务像跨文件重构（用 **mode + 已调用的 code.impact**，**禁止** utterance 表）。  
- 长任务过半（turn 预算）。

### 必须静默

- converse / 问候 / 短问答。  
- plan 未批准。  
- 用户关掉「建议」。  
- 置信不足（没有失败证据、没有重复触及）。  
- 已有未读奏折超过上限（默认 1）。

### 权限

- 默认 **只读**：read/search/code.*/web.fetch（已批准 host）。  
- **禁止** patch/edit/run-that-writes/memory/MCP-write。  
- 采纳后才把建议动作交给主 agent 或一次性 child（仍走 kernel）。

### 输出形态（侧栏/overlay，不是聊天气泡）

```
title: 准备 auth 回归
why: 已 3 次读 auth 包，尚未跑测试
done: 列出 TesAuth / 调用图 12 边
propose: 跑 go test ./internal/auth
risk: 只读；采纳后才会执行测试
[采纳] [忽略] [静音本类]
```

主 transcript：**默认不进全文**。最多一行 muted：「有 1 条准备就绪」。

### 五个产品场景

1. 改 auth 时旁路备好测试名与 impact 图。  
2. CI/测试红了：旁路复现命令与失败摘录，奏折「要不要自动再跑」。  
3. 依赖升级前扫 breaking 调用点（code.refs）。  
4. 长 refactor 中途备好未改文件的 read 集合。  
5. docs 站改动：若将来有 browser，旁路只 **提议** 打开预览，不自己点。

### 滥用抑制

- 每会话最多 3 条未决；每 10 分钟最多 1 条新提案。  
- 忽略同类则冷却。  
- 总开关 `proactive=off` 默认 **off**（MVP 可先 build-only + opt-in）。

### 非目标

- 自动 commit/PR。  
- 在 converse 推销功能。  
- MiniLM 意图分类。  
- 把奏折当 Buddy/宠物。

### 验收

- build 长任务中，侧栏可出现 ≤1 条只读准备；主对话字数不因此涨。  
- 忽略后不再刷。  
- 采纳后动作仍走审批。  
- converse `hi` 零奏折。

### MVP 落点

- 新内部 runner：`proactive.prepare`（只读 profile，depth 1）。  
- 事件：`proposal.created`（session 级，不进 model transcript）。  
- TUI：`/inbox` 或 header 计数，复用 needs_input 视觉，**不要新皮肤**。

---

## 8. Phase 6 — Meta 成熟度

**当前总级：L1 配置化，局部 L2（MCP + WASM plugin）。**

| 类 | 级 | 现在 | 最小下一跳 | 不要跳到 |
|----|----|------|------------|----------|
| **Meta-Tools** | L1 | 硬编码 dispatch + 一行 catalog；MCP 经 `mcp`/`mcp_find` | 工具 registry 表：name/schema/timeout/effect；MCP 已是外总线 | 模型热加载任意二进制 |
| **Meta-Agents** | L1 | `agents.md` 模板、ToolNames、RestrictedTools、explore 衰减 | 能力清单进 `agent.list`（已有字段）+ 测试夹具 | 子 agent 默认 yolo |
| **Meta-Prompts** | L0–L1 | Intent-Meta 静态；无失败→补丁 | 失败 trace 写入 **提案**（奏折），人批后才改 skill 体 | 自动改 constitution |
| **Meta-Harness** | L0 | 编排写死在 loop | 策略钩子接口：allow/deny/propose | L4 自改生产权限 |

### 为未来 LLM 预留（现在做接口，不绑实现）

| 面 | 现在已有 | 预留 | 明确不做 |
|----|----------|------|----------|
| 事件流 | hash-chained audit；tool lifecycle started/completed | 稳定 `tool/call/result/error/decision` 对外 schema（SDK 已有 lifecycle） | 把 audit 当模型输入 |
| 策略钩子 | Pre/PostTool hooks；kernel Decision | `trigger/allow/deny/propose` 可替换实现 | 钩子绕过 kernel |
| 经验库 | checkpoint + compaction receipts | 成功/失败轨迹可回放（只读店） | 实时梯度下降 |
| 在线更新 | 无 | 只许改 **prompt/skill/策略开关**，且批准+回滚 | 无门控改 ToolNames 放行写 |
| 双工 IO | keepalive 是 UI 心跳 | 模型可 **推送意图**；harness 仲裁执行 vs 奏折 vs 丢弃 | 模型直连 shell |

持续学习 = **轨迹数据面 + 批准后的提示补丁**，不是把训练环塞进 daemon。

---

## 9. Phase 7 — 路线图

### P0（已收口；日用剩余 P0 = 无）

| ID | 项 | 类 | 价值 | 参考 | 改动点 | 侵入 | 验收 | 风险 |
|----|----|----|------|------|--------|------|------|------|
| T-S1 | **Landed:** `web.search` | A | 调研任务不再卡 URL | Grok web_search；OMP WebSearch；Codex 可选 | dispatch + schema + NetworkAccess；host 批准；结果 untrusted；**禁止** run/curl | 中 | 有 query 无 URL 时走 search | SSRF：只出允许解析器；失败 closed |
| T-S2 | **Landed:** 真 `todo`/`update_plan` | A | plan/build 可盯 | Claude plan；Grok todo；OMP TodoTool | dispatch；session 级清单；plan 允许 / explore 否 | 中 | plan 模式 `todo` 不再 unknown tool | 不要做成短语分类器 |
| T-S3 | **Landed:** search/list 结构化摘要 | B | 少灌 token | Claude toolResultStorage；CONTEXT P1-C4 | search 按文件聚合 path+why；list 目录级 | 低 | 默认观测无 50 行垃圾 | 模型少看到原文 |
| S10 | **Landed:** REQUESTED skills 出 Catalog | B | prefix 稳 | 24 / PROMPT_SPEC | `buildDynamicSkillPrompt` 拆 Catalog/Requested | 低 | CacheSections 与问候无关 | 已锁 |
| — | MCP 总线 | — | **已有** | — | 不要重做 | — | 用 `mcp_find` + 短 index | 禁止 passthrough |

**不把「同 turn 并行」当新 P0：** list/read/search 已并行。扩大到 code.* 会打 kernel 锁。先摘要，再考虑 index RPC 批量化。

### P1

| ID | 项 | 类 | 价值 | 备注 |
|----|----|----|------|------|
| T-P1 | 奏折通道 | C | 有准备不吵 | 默认 off；只读；侧栏 |
| T-P2 | git 一等：status/diff/log | A | 少 run git | **补丁仍走 edit/patch**。git 只读信息 + 显式 commit 仍审批 |
| T-P3 | 模型可见 job wait/cancel | C | 边聊边等 | 包现有 `runs` registry，不要第二套队列 |
| Index T1 | 闲时预热 map | B | 免 0/N | 范围门：git 根；拒 `/` `$HOME` |
| P1-C1 | compact 后 cited-file 重建 | B | 长任务不丢引用 | **Landed:** volatile Rebuild（8k）；converse 不灌 AGENTS |
| Browser | 仅若要验本机 UI | A | 可选 | 默认关 |

### P2

| ID | 项 | 类 |
|----|----|----|
| Meta registry + 金丝雀自测 | D |
| 失败 trace → skill 补丁（批准后） | D |
| 事件/策略钩子文档化（双工预留） | D |
| done 产物清单可选字段 | B |

回滚：每个新 tool 用 feature flag；dispatch 无 flag 即 unknown。奏折默认 off。git 工具只读先发、commit 后发。

---

## 10. Phase 8 — GAP 分类清单

**Missing tools (A)**  
模型侧 git status/diff（P1）；browser（条件）；`job.wait`/`job.cancel`（模型工具）。`web.search` 与真 todo **已落地**。

**Bad consumption (B)**  
code.* 冷索引；失败字符串不够结构化；done 无验收清单。search/list extract 与 S10 **已落地**。

**Missing channels (C)**  
奏折/提案面；后台句柄对模型不可见（RPC 已有 registry）。

**Missing meta (D)**  
工具自测金丝雀；prompt 补丁批准流；L3 门控 registry。L4 默认永不进生产。

**已有、不要当成缺口：** MCP 总线、WASM plugin、spawn summary-only、rewind/checkpoint、并行 list/read/search、精确 span edit、fail-closed sandbox、plan mode 面具、background run **RPC**。

---

## 11. 反模式

1. 无门控自改工具面或 constitution。  
2. 奏折全文进主对话。  
3. 用 `run` 无限替代 fetch/edit/git-info。  
4. 只加工具不改返回纪律。  
5. 研究级自演进默认开。  
6. 把 todo 做成用户话语分类器。  
7. 为审计交差复制 OMP 32 工具或 Grok scheduler。  
8. MCP passthrough、git-only 回滚、hashline 换掉精确 span。

---

## 12. 阶段性结论（按强制顺序）

- **Phase 0：** 标尺是分层 + 消费 + 通道 + 门控 meta，不是工具个数。  
- **Phase 1：** 内核工具 S/A；T-S1/T-S2 已补开放世界与 todo。  
- **Phase 2：** 本仓 bugfix 与调研/计划任务可走通；奏折仍缺。  
- **Phase 3：** 日用 P0 收口；奏折/git/browser 仍 P1。  
- **Phase 4：** 剩余 FAIL 在奏折、模型侧 job（P1）。  
- **Phase 5：** 奏折 = 只读旁路 + 侧栏提案 + 默认关。  
- **Phase 6：** L1/局部 L2；下一跳 L2 体验 + 有限 L3 门控。  
- **Phase 7：** 日用 P0 = 无。P1 = 奏折 + git-info + job 句柄 + index 预热。

**下一刀（若「高质量收敛」）：** 不要并行开奏折/git/browser。日用 P0 已空。
