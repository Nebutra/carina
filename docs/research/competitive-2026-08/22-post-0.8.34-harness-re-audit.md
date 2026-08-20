# Carina 智能体 Harness 再审计（v0.8.34）

> **取证日期**：2026-08-20  
> **产品锚点**：`go/product/version.go` → **0.8.34**（`599a695` / tag `v0.8.34`）  
> **角色**：Senior Agent Harness Architect + Product Auditor  
> **方法**：本仓库源码实测 + 本机竞品树（未 clone DeepSeek）+ 既有 DNA 卡（`01-dna-*`）SHA 复核  
> **前序**：`16` 是 **0.8.27 诚实性**（「无 P0」已被 0.8.28–0.8.34 证伪，勿再当现状）；审美 SSOT 仍是 `18`；Prompt 法是 `PROMPT_SPEC.md`；热路径法是 `HOT_PATH.md`  
> **本文角色**：0.8.34 全 harness 现状 SSOT。偷 **job**，不偷像素 / 品牌 / 内核。  
> **未做**：并排 PTY、同机 RSS PSS、DeepSeek 行号级 DNA、真实 Grok 会话再拍（Intent-Meta 需重启 daemon）

---

## 0. Executive Summary

Carina 0.8.34 已经不是 08-02 那个「治理 5、可感 3」的 runtime 半成品。治理护城河仍在；日用债里 **问候爬树、全宽用户条、BRIEF 产品简介、默认 build 巡库** 已经发完。还没过的产品标准是：**Grok/OpenAI 每一轮仍吃一整块 ~1.4k tok 的 constitution**，其中工具说明书仍是大头。

「Agent 能回话」不是 pass。用户愿意每天打开，取决于：回答对准意图、首帧不空等、长会话不瞎截、副作用可回、画面是对话文档而不是 syslog。

### Top 5（只排还没发、且用户会感到的）

| # | 行动 | 优先级 | 用户一句话 |
|---|------|:------:|------------|
| 1 | **S8** 砍 JSON `toolsHelp`，live constitution（无 Workspace/F）&lt; 800 tok | **P0** | 每一轮别先吞掉一千 token 的工具说明书 |
| 2 | **S7** constitution 改成命名 A–D 段，而不是一个 Go `const` 拼接 | **P0** | Anthropic 能分段 cache；Grok 诚实标 `cache=none` |
| 3 | **S10** `REQUESTED SKILLS` 出 Catalog、进 VolatileSuffix | **P0** | 问候和 `$pdf` 不要打爆 prefix cache |
| 4 | **C1** build/plan compact 后重读 AGENTS.md + 引用文件 | **P1** | 长会话不要把仓库规矩压没了还假装记得 |
| 5 | **A107/A106** paste/`@` chip 与 slash 选中行不要双氰 | **P1** | 输入行别比答案还吵 |

**明确不做（仍有效）**：默认 yolo · snapcompact · MiniLM 默认开 · 3D/Buddy · 整包 Grok pager · Cordis 核 · MCP passthrough · git-only 回滚 · hashline 换事务 patch · Codex fuzzy 当权威 edit · ACP 当交互协议 · SaaS 多租户当 day-one · 新 TUI 皮肤 · 堆 utterance 分类器。

**16 的纠错**：0.8.27 说「无 P0」是诚实性切片的结论。之后 pebble 四轮、问候 Explore×46、热路径首帧，证明日用还有刀。那些刀已经随 0.8.30–0.8.34 发出。不要用 16 否定本文剩余 P0。

---

## 1. Phase 0 — Carina 模块现状地图

```
TUI / CLI / VS Code / Web / SDK
        │  JSON-RPC（unix socket；Gateway 可选 HTTP/WS）
        ▼
Go daemon  agent loop · reasoner · promptcache · compact · goal · MCP
        │  kernel.request（stdio JSON-RPC，Client.Call 全程持 mu）
        ▼
Rust kernel  Capability + audit hash-chain + transactional patch
        │
        ▼
Zig  carina-scan / grep / diff / run / pty / patch-native
```

### 1.1 入口

| 面 | 路径 | 状态 |
|----|------|------|
| CLI | `apps/carina-cli` | **SHIPPED**。`carina` 是交互壳；`update` / doctor / audit |
| TUI | `crates/carina-tui` → `carina-ui` | **SHIPPED**。默认 Fullscreen alt-screen；独立 input 线程；16 ms present |
| Daemon | `apps/carina-daemon` + `go/daemon` | **SHIPPED**。会话权威 |
| Kernel | `crates/carina-kernel` | **MUST-PROTECT** |
| Worker | `apps/carina-worker` / `go/worker` | **SHIPPED** 远程 worker 包；非 Windows 桌面 daemon |
| VS Code | `integrations/vscode` | **WIRED**；Marketplace 未激活 |
| Web Operator | `integrations/web` | **UNPRODUCTIZED** 托管 |
| SDK | `sdk/{go,python,typescript}` | **SHIPPED** 协议封装；runtime 兼容钉 0.8.34 |
| Gateway | `go/daemon/gateway_http.go`、`go/rpc/websocket.go` | **ISOLATED**：token + pin 有；不是多租户 SaaS |

### 1.2 Agent runtime（活路径）

| 模块 | 今天做什么 | 分类 |
|------|------------|------|
| `agent.go` `runAgentLoop` | ReAct：`compact(nil)` → `seg.full()` → Think → kernel/Zig → snip | SHIPPED |
| `prompt_mode.go` | **只按 agent 名**：build/plan 灌 AGENTS.md；converse/explore 不灌 | SHIPPED 0.8.34 Intent-Meta |
| `promptcache.go` | 一次/run：Constitution → Workspace → Catalog；TASK 在 VolatileSuffix | SHIPPED；Grok/OpenAI `cache=none` |
| `agents.go` | 默认 **converse**；build/plan/explore 一句话叠在同一 `systemPrompt` 上 | SHIPPED |
| `tool_schema.go` | HTTP native tools 时 `withToolContract(nativeToolsContract)` 换掉 `toolsHelp` | S11 大半已发；JSON ReAct（Grok）仍走 `toolsHelp` |
| `transcript.go` | snip → 陈旧 read → elide → collapse；模型摘要在 **turn 后** | SHIPPED 0–3；**无** rebuild F（C1） |
| `grok_reasoner.go` | 持久 grokHome；inspect 成功一次后跳过；system = JSON ReAct | SHIPPED 0.8.33 |
| `keepalive.go` | Think/tool &gt;1s bus-only `execution.keepalive`，不写 audit | SHIPPED |
| `subagent.go` | 父只见 `done.summary` | SHIPPED |
| `explore.go` | 只读工具 allow-list | SHIPPED |
| `workflow.go` | DAG 独立步并行 | SHIPPED |
| `swarm_channel.go` | 通道存在 | **ISOLATED** |
| `bestofn.go` | 默认关 | **UNPRODUCTIZED** |
| `memory_hms.go` | 默认 `off` | **ISOLATED**；MiniLM 保持关 |
| `go/contextengine` | identity / noop | 不要当 compressor 卖 |

### 1.3 Live constitution（0.8.34 实测）

`go/daemon/agent.go` backtick 常量：

| 段 | 字节 | ~tok (len/4) | 是否进 live concat |
|----|-----:|-------------:|-------------------|
| `productIdentity` | 475 | 114 | 是 |
| `intentFirst` | 851 | 211 | 是 |
| `toolsHelp` | **3451** | **862** | JSON ReAct **是**；native HTTP **被替换** |
| Orchestration | 676 | 169 | 是 |
| **合计 live** | **~5461** | **~1365** | S8 目标无 F &lt; 800 → **FAIL** |
| `productCapabilityBrief` | 1256 | 314 | **否**（dormant const） |

对照审计 21（v0.8.33）：当时 ~8457 B / ~2115 tok、`toolsHelp` ~4806 / 1202。S9 + 协议改写成 meta 已经瘦了一截，仍超标。

`systemPrompt = productIdentity + intentFirst + toolsHelp + orchestration`。仍是 **一个 const 拼接**（S7 开）。converse 再前置一句 mode。

### 1.4 堆叠规则盘点（Intent-Meta 之后）

| 东西 | 判定 |
|------|------|
| `shouldLoadProjectInstructions` | **mode switch**。保留 |
| `looksLikePromptTooLongMessage` | provider 错误串 → compact。协议，不是 FAQ |
| `looksLikeActionEnvelope` | 把模型漏出的 JSON 从用户可见答案里拿掉。协议 |
| `exploreRestrictedTools` | 能力 allow-list。内核侧，不是 utterance 分类 |
| `projectInstructionCandidates` | 文件名候选。加载器，不是意图 |
| `$name` / `skill://` | 显式调用。保留 |
| `CARINA_IMPLICIT_SKILL_PROMPTS` | **默认关**。打开才走 trigger 表。保持关 |
| `buildDynamicSkillPrompt(..., UserPrompt)` | Catalog 里的 **REQUESTED SKILLS** 仍跟这句用户话走 → **S10 剩余** |
| phrase table / chatter map / BRIEF 按「你有啥用」拼接 | **已删**（0.8.34） |

### 1.5 巨石（SLOP，随功能拆，不要为拆而拆）

| 文件 | 行 |
|------|-----|
| `crates/carina-tui/src/app/render.rs` | 17842 |
| `crates/carina-tui/src/app/mod.rs` | 12478 |
| `crates/carina-tui/src/i18n.rs` | 8783 |
| `go/daemon/grok_reasoner.go` | 3361 |
| `go/daemon/agent.go` | 2197 |

### 1.6 用户视角短板（源码，非营销）

已经不再是：`hi` 默认 build + 灌 AGENTS.md + Explore×46（0.8.31）；用户条铺满一行（0.8.30）；BRIEF 当自我介绍（0.8.34）；首帧卡在 `model.list`（0.8.33）。

**现在还能感到的：**

1. Grok JSON 路径每一轮 ~1365 tok constitution，工具表 862 tok。贵、慢、模型容易背目录。  
2. compact 后 build/plan **不重读** AGENTS.md（层在 task 开始冻死）。  
3. paste / `@` chip 反色氰；slash 选中行双氰。  
4. `/context` 仍说 `ledger` / `hash` / `ctx`。  
5. kernel stdio 一把锁：并行 read 的 **授权** 已一次，**管道** 仍串行。不要拆锁。  
6. 默认 Fullscreen，原生 scrollback 只在 `/minimal`（#11 partial）。

---

## 2. Phase 1 — 六家（偷 job）

竞品树 08-20 复核：版本与 08-02 DNA **基本未漂**；补了 git SHA。DeepSeek **无 clone**，不编。

| 产品 | 锚点 | 架构一句话 | 可偷的 3–5 个 job | 禁止 | 成本 |
|------|------|------------|-------------------|------|------|
| **Jcode 0.64.2** `d6c7c36` | `jcode-app-core` turn_loops；`jcode-compaction-core`；`jcode-swarm-core` | Server 拥有 session；static/dynamic split prompt；记忆当 trailing user | ① prefix 稳定、回忆进 suffix ② compact 三套计量对齐 provider ③ 子 agent 只回 TLDR ④ `intent` 进 schema ⑤ daemon 拥有 session | MiniLM 默认 ONNX；idle 3D donut；1000-agent | 抄 server 模型低；抄 embedding 栈高且伤 RAM 身份 |
| **Grok** `SOURCE_REV` `8d69c91`（clone SHA `a422116`） | pager ≠ shell；`xai-grok-compaction`；`acp_session_impl/turn.rs` | 全屏 pager + ACP leader；compact full-replace + 事后 reminder | ① compact-core 与 host 分离 ② 扩展不许劫持 loop ③ 子 agent 能力相交 ④ 副作用三态：可见/可批/可逆 ⑤ compact 后把 plan/todo **再注入** | 整包 pager；ACP 当聊天协议；GrokNight | 原则中；pager/leader **极高** |
| **Claude notes 2.1.88** | `09-system-prompt工程`；`10-上下文压缩`；QueryLoop | `string[]` + `__DYNAMIC_BOUNDARY__`；四级压缩后 rebuild ≤5 文件 | ① 轻→重 compact + 3-strike ② 静态/动态边界，破 cache 要有理由 ③ Fork 是 prefix 布局 ④ validate→authorize→execute ⑤ Explore 故意不灌项目指令 | Buddy；terracotta 当身份；Ink 整包 | 笔记当字典，不当 clone |
| **Codex** `feee0b0` | `core/src/session/turn.rs`；`protocol.rs` 双轴；`compact.rs` | SQ/EQ 引擎；approval ⟂ sandbox；context fragments | ① 双轴同名贯穿 config/prompt/TUI ② 一条 orchestrator ③ fragments+cap ④ Plan 硬掩码 ⑤ 对话=typed cells | fuzzy 换精确 span；Never+unsandboxed 默认 | 双轴/fragments 低–中，强化内核 |
| **OMP 17.2.3** `09a7c86` | `agent-loop.ts`；`docs/compaction.md`；`docs/skills.md` | 「harness is the product」；hashline 编辑；compaction 一等 entry | ① 子 agent structured yield + URI ② compact 是 session entry ③ skill 目录+`skill://` ④ 成功 read 塌缩 ⑤ steer ≠ follow-up 队列 | **默认 yolo**；snapcompact；hashline 垄断；π 品牌 | 原则中；DSL/默认策略 **否决** |
| **DeepSeek** | 无树 | 公开 README：Everything is a Plugin / Cordis | Trajectory / 可组合 session log（文档级，中置信） | **Cordis 核** | 无源码不排期 |

Carina 已吸收且必须保住：capability kernel、hash-chain audit、事务精确 span patch、子 agent 只回 summary、converse 默认、F 按 mode、TASK 出 cache、Intent-Meta。

---

## 3. Phase 2 — Scorecard

评分 1–5。竞品是「这类产品的最佳 job」，不是均分对赌。

| 维度 | 最佳实践代表 | Carina 0.8.34 | 差距 | 用户价值 |
|------|--------------|:-------------:|:----:|----------|
| A 功能与交互（slash / 会话 / Goal / 队列） | OMP 发现面；Codex resume/fork；Grok prompt-queue | **4** `/` 注册齐全；`/new` `/fork` `/resume` `/goal` `/queue` `/btw` | P2 | 日用够用；slash 双氰是审美不是缺命令 |
| B 性能与资源 | Jcode RAM 预算；Grok inspect 复用 | **3.5** H1–H7 已发；kernel 锁仍在；constitution 税仍在 | **P0=S8**；锁为 P2 保护项 | 每轮 token 税可感 |
| C 上下文工程 | Claude 四级+rebuild；jcode split+EWMA | **4** 0–3 级有；F 不重建；Grok cache none | **P0=S7/S10**；P1=C1 | 长会话保真 |
| D 智能体范式 | Claude AgentTool；Grok subagent 相交 | **4** spawn/workflow/explore 活；swarm 孤岛 | P2 | 主路径够；别开 1000-agent |
| E 扩展 | OMP skills index；Grok plugin=skills+hooks+MCP | **4** MCP index+`mcp_find`；skill://；implicit 默认关 | P2 | 不要把 schema 灌进每一轮 |
| F TUI / 审美 | Codex 着色 job；Grok 答案不装箱；Claude 开口栏 | **4** A010–A013 已发；chip/slash/context 仍吵 | **P1** | 日用文档感，不是新皮肤 |
| G 部署 / SaaS | Codex app-server；Gateway token | **2** 本地 daemon+SDK 强；多租户 **UNPRODUCTIZED** | **不排 P0** | 先做好单机日用 |
| 治理 / 安全 | Carina 自己 | **5** | — | 护城河 |
| **用户可感（加权）** | — | **~5.5 / 10** | 结构 FAIL 在 S7/S8 | 问候爬树已关；token 税未关 |

维度均分去跟聊天产品对赌没有意义。治理 5 必须保护。

---

## 4. Phase 3 — SWOT + 过滤

**S** kernel + audit + 事务 patch；Intent-Meta；converse 默认；热路径首帧；对话文档四刀。  
**W** 单 blob constitution；TUI 双巨石；Grok/OpenAI 无 prefix cache；compact 不重建 F。  
**O** 把 A–D 分段（偷 Claude/jcode 的 **边界 job**）；chip 降噪。  
**T** 为审计交差再造皮肤 / SaaS / Cordis / 分类器 FAQ。

### 看起来很酷、多数用户无感 → 降级或放弃

| 项 | 处理 |
|----|------|
| 百主题 / GrokNight / π | 放弃 |
| Buddy / 3D donut / 36 帧 ASCII | 放弃 |
| MiniLM 默认、snapcompact | 放弃 |
| ACP 当主聊天协议 | 放弃（Grok ACP 只做 isolation 适配器） |
| Lovable/v0 多租户 day-one | 放弃 |
| 齐功能原生 scrollback | #11 保持 partial；Fullscreen 是产品选择 |
| 1000-agent swarm | swarm 保持孤岛 |
| 空状态 shine（A102） | 可选 P2；Fixture A 稳定后再做 |

只保留「明天打开还想用」的：对准意图、少 token 税、长会话不丢规矩、输入行不吵、副作用可回。

---

## 5. Phase 4 — 分类清单

### GAP（缺、且用户会感到）

| ID | 缺口 | 说明 |
|----|------|------|
| S7 | 命名 A–D constitution | 仍是 `const systemPrompt` 拼接 |
| S8 | toolsHelp 税 | JSON 路径 862 tok；合计 ~1365 &gt; 800 |
| S10 | Catalog 依赖 UserPrompt | `REQUESTED SKILLS` 进 cache prefix |
| C1 | compact 后重建 F | 层在 run 开始冻结 |

S11：native HTTP 已 `withToolContract` 换成 ~短协议。**剩余并入 S8**（Grok JSON 仍贴 `toolsHelp`）。

### WIP

- Scrollback #11 **partial**（ledger + insert_before；默认仍产品 viewport）  
- HMS memory 默认 off  
- Native scrollback 不是设计过的 Minimal 卡片  

### TODO（可排期 = Top 5 + 下列 P1）

- A107 paste/`@` chip 用 live `Theme`，muted pill  
- A106 slash 选中 **或** accent 名，不要两个  
- A108 `/context` VOICE，去掉 ledger/hash/ctx 黑话  
- A103 `/changes` 量度；不要 4 字符省略 ID  
- A105 问题列表 `glyphs.selected()`；Doctor 走 i18n  
- A114 品牌 hex 对比度阻塞 — **不要**把 `#8e4053` 硬画在 void 上  

### BUG

- 无新功能性 P0 bug。上游 503 仍是上游。  
- Intent-Meta **必须重启 daemon** 才进 live constitution（安装后的操作说明，不是代码 bug）。  
- 并发 rustfmt 脏树（14 个 TUI 文件）**不要**混进产品提交。

### LEGACY

- 全局 `~/.carina/daemon.sock`（workspace runtime 已是权威；旧路径仍在）  
- Go TUI 已退役；`carina-tui` 二进制名已删  

### SLOP

- `render.rs` / `app/mod.rs` / `i18n.rs` / `grok_reasoner.go` 巨石  
- 只随功能切，禁止为审计拆 pager  

### ISOLATED

- `swarm_channel.go`  
- Gateway pin / HTTP  
- HMS  
- Grok ACP（适配器，不是 UI 协议）  

### UNPRODUCTIZED

- best-of-n（默认关）  
- Marketplace / hosted Web / VS Code Marketplace  
- 多租户 SaaS daemon（有 SDK+Gateway 零件，没有租户模型）  

不要重开 ISSUE-001…018、V001–V011、#28–#39、G23–G25、R-01。

---

## 6. Phase 5 — 闭环路线图

### P0（prompt 结构，法：`PROMPT_SPEC.md`）

#### P0-S8 — 砍 JSON 工具说明书

- **用户价值**：Grok 每轮少付 ~500–800 tok；模型少背目录。  
- **参考**：Claude builtins 一行；OMP skill 只留 index；jcode 强制短 description。  
- **路径**：`toolsHelp` 已是一行/工具；再砍 Harness protocol 到 **≤5 条 standing negatives**；JSON 示例只留在测试。Native 路径已短，不要再贴回去。  
- **验收**：`len(systemPrompt without Workspace/F)/4 < 800`；`conversation_first_test` 仍无 BRIEF / AGENTS.md；Fixture G：`hi` → `done`、零 list/search。  
- **风险**：模型忘 JSON 形状 → 靠 native tools + 测试里的例子，不靠把说明书加回去。回滚：保留当前 `toolsHelp` const 别名。  
- **工程量**：1–2 人天。

#### P0-S7 — 命名 A–D

- **用户价值**：Anthropic 分段 `cache_control`；测试能断言顺序；Grok 继续诚实 `none`。  
- **参考**：Claude `string[]` + boundary；jcode `SplitSystemPrompt`。  
- **路径**：`promptLayers` 已有 Constitution/Workspace/Catalog。把 Constitution 拆成 Identity / Mode / Protocol / Tools **四个 string 字段**，`join` 仍给 Grok `full()`。禁止把 TASK 塞回去。  
- **验收**：测试断言顺序 A→D；无单一 blob 作为唯一真相（可保留 `full()` 作为派生）。  
- **风险**：Grok 适配器误标 cache。`promptCacheKindFor` 已对 Grok 返 `none` — 加回归。  
- **工程量**：2–3 人天。

#### P0-S10 — REQUESTED SKILLS 出 prefix

- **用户价值**：`$pdf` 不让下一句 `hi` 的 cache 失效。  
- **参考**：OMP skill 目录稳定、body 按需。  
- **路径**：`buildDynamicSkillPrompt` 拆成 **stable catalog**（进 Catalog）+ **requested list**（进 VolatileSuffix）。Implicit 保持默认关。  
- **验收**：同一 workspace，`hi` 与 `use $x` 的 `CacheSections()` Catalog 字节相同。  
- **风险**：模型看不到「你点名了 skill」。suffix 里写 REQUESTED 即可。  
- **工程量**：0.5–1 人天。

### P1

#### P1-C1 — compact 后重建 F

- **用户价值**：build 长会话不丢 AGENTS.md。  
- **参考**：Grok assemble 把 AGENTS 当独立 item 再注入；Claude rebuild ≤5 文件。  
- **路径**：`compact` 成功且 agent 是 build/plan 时，重跑 `loadMemory` 写回 Workspace 层（**不要**在 converse 问候重灌）。引用文件 ≤5、每份 ≤5k tok。  
- **验收**：build 会话 compact 后下一轮 prefix 含当前 AGENTS.md；converse `hi` 仍不含。  
- **风险**：把 F 灌进 converse。用现有 mode switch。  
- **工程量**：2 人天。

#### P1-A107 / A106 / A108 — 日用降噪

- **用户价值**：输入行和 slash 不再比答案响；`/context` 说人话。  
- **参考**：Codex chip 是 tint；Grok overlay 单 accent。  
- **路径**：chip 用 `self.theme`，`action()` 反色改为 muted pill；slash 选中行 **xor** 名称 accent；`/context` 走 `VOICE.md`。  
- **验收**：idle 80×24 golden：chip 无 `Theme::detected(None)`；slash 选中行不超过一种 accent。  
- **风险**：误伤 focus 态。只改 chip/popup，不动 composer 开口栏。  
- **工程量**：1–2 人天。  
- **A114**：继续 **不**把 canonical rose 画在 void 上。

#### P1 其余（A103/A105/A109）

随 overlay 改动捎带。不要单开 epic。

### P2

- 巨石随功能切。  
- compact 计量对齐 provider usage tokens（P1-C2 也可升，若账单痛）。  
- 结构化 search/list extract（P1-C4）。  
- 主动 compact EWMA（P1-C3）— 有计量再做，禁止 MiniLM。  
- Gateway 作为 **本机/内网** 嵌入面文档化；**不是**租户 SaaS。

### 多租户 SaaS（Phase 5 要求的回答）

Carina **已经**是「daemon 当 backend」：JSON-RPC + SDK + 可选 Gateway token。要变成 Lovable/v0：

| 需要 | 现状 | 裁决 |
|------|------|------|
| 租户隔离 | 无。session 绑 workspace runtime | **day-one 否决** |
| 每租户密钥/egress | 内核按 session，不是按 tenant | 不要在 prompt 里假装有 |
| 计费 / 配额 | `max-task-tokens` 是单任务，不是租户 | P2 以后 |
| Web 托管 | 包有，Marketplace 未开 | UNPRODUCTIZED |

正确路径：把 **本地 daemon 做好**（S7/S8、C1、chip），Gateway 保持 pin+token。禁止为「对标 v0」加 tenancy 表。

### 验收总闸

- `go test ./go/daemon -run 'ConversationalRequest|GreetingCompose|ShouldLoadProject|CacheSections|SystemPrompt'`  
- Fixture G：本仓库 `hi` → 零 list/read/search。  
- 4-turn 短聊 golden：用户 pill、答案比 tool 响、denied 不是 Failure、leftover Reset。  
- `make brand-check` 若动 README。  
- **不**把 rustfmt 脏文件塞进同一 PR。

---

## 7. 审美与 TUI 规范（直接指导开发）

法律：`docs/brand/AGENTS.md` + `crates/carina-tui/styles.md` + `theme.rs`。`18` 仍是审美 SSOT。`DESIGN.md` baseline 0.8.34。

1. **日用对话是产品。** 空状态和 composer 栏救不了 syslog 正文。A010–A013 已把四轮短聊从「满行色块」拉成文档雏形；下一刀是 **页内等级**（chip、slash、`/context`），不是新皮肤。  
2. 根帧 `Color::Reset`。transcript ≤3 饱和色（ion-cyan / copper-amber / event-red）。品牌玫瑰 **只**给 10×5 mark，且对比度不过就维持 elevated `#de859b`（A114）。  
3. 用户条 = 占用格子；答案不装箱；live 工具一根 `┃`；settled 工具 muted；denied 是收据不是崩溃。  
4. leftover = 空白。禁止 donut / 第二张欢迎海报。  
5. 开口栏保持上下 `─`、两侧开。禁止 OMP 四边盒当主壳。  
6. Status 在顶栏；Notice 是栏上独立行。Idle 不堆 HITL/ctx。  
7. 动效 demand-gated。禁止 body shimmer、循环欢迎。  
8. 禁止 `Theme::detected(None)` 出现在已有 `self.theme` 的 widget（A107）。  
9. 默认 Fullscreen 是为了把滚动留在产品里，不是为了抄 pager。  
10. 禁止新 token set、GrokNight、π、Buddy、terracotta 当 Carina 身份。

---

## 8. 风险与 Trade-off

| 风险 | 若做错 | 防护 |
|------|--------|------|
| 为 S8 把工具说明书砍到模型不会 JSON | 乱 action、requery 烧轮次 | native 路径已短；JSON 例子放测试；Fixture G + 循环测试 |
| 为 S7 抄 Claude 分段散文进身份 | 变成另一个产品 | 只偷边界，不偷 terracotta / XML |
| converse 不灌 AGENTS.md | 仓库惯例要自己 `read` 或 `/agent build` | **已接受**（0.8.34） |
| 拆 kernel 锁「假装并行」 | 破坏 fail-closed | 锁留下；batch 只做 IO |
| 默认开 implicit skill / MiniLM | 问候又开始灌 | env 默认关 |
| 把 Gateway 说成 SaaS | 安全故事假 | 本文否决 tenancy |
| 为交差拆巨石 / 新皮肤 | 用户无感、回归面爆炸 | P2 随功能切；审美走 A107 不是 A102 shine |

**置信度**：Carina 源码 **高**；竞品版本未漂、SHA 已钉 **高**；DeepSeek **中（无 clone）**；live Grok 是否仍背矩阵 **中**（取决于是否重启 0.8.34 daemon）。

---

## 附录 A — 维度 Checklist（A–G 压缩结论）

**A 功能与交互**：slash 发现/补全/帮助 **有**（`command.rs` 全表）。多入口同一 JSON-RPC。会话 resume/fork/new **有**。通知行 vs 状态行 **已分**。Goal/Todo/Plan **有** 且 plan 硬只读。缺的是审美降噪，不是命令。

**B 性能**：input 线程 + 16 ms；H1–H7 已发；PTY TTFF 有 bench。瓶颈改到 constitution 税和 kernel 锁（后者保护）。

**C 上下文**：cascade 0–3 有；4 重建无；5 主动无。分段 cache 仅 Anthropic。Memory 本地有、HMS 关。AGENTS.md 分层加载有，**mode 门**。

**D 范式**：主 loop ReAct。Subagent summary-only。Workflow DAG。Swarm 孤岛。无自我复制产品化。Goal 有。

**E 扩展**：MCP 生命周期 + `mcp_find`。Skills catalog + `skill://`。Hooks 有。Provider 热切有。Marketplace 未产品化。

**F TUI**：ratatui + 自研 inline；真彩/降级/NO_COLOR；鼠标在 Fullscreen。空状态有身份。Diff workbench 仍挤（P1）。主题 auto/dark/light，无目录（正确）。

**G 产品化**：SDK/Gateway 零件在；审计链在；session replay 本地有。多租户 **不做 day-one**。
