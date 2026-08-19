# Post-0.8.23 Harness Audit Refresh

> **取证日期**：2026-08-18  
> **产品锚点**：`go/product/version.go` → **0.8.23**  
> **工作树**：含未提交 reasoner 握手修复（Grok `consent_gate` / Claude 增量 assistant / 503 UserAction）  
> **方法**：本仓库源码 + `crates/carina-tui/CAPABILITIES.md` + GitHub ISSUE-019…040 closed + 本机竞品树抽查 + DeepSeek 公开 README（**本机无 clone**）  
> **前序**：`07-post-0.8-gap-refresh.md`（0.8.0）已过时，**以本文为现状真相**  
> **未做**：并排 PTY、同机 RAM PSS、DeepSeek 本地行号级 DNA

---

## 0. Executive Summary

Carina 0.8.23 已经不是「内核强、壳弱」。ISSUE-001…018、视觉密度 V001–V011、queue inspect、以及 0.8.22–23 的 **harness honesty**（#28–#39 / epic #40）都已合入。竞品树相对 2026-08-02 **几乎无漂移**（Jcode 仍 0.64.2；Grok `SOURCE_REV` 仍 `8d69c91f…`）。

**今天的产品力短板不是功能清单，而是首轮推理能不能办成事、失败时说不说实话。** 用户截图「帮我在桌面上写个贪吃蛇」三连失败：Grok 361ms 被误判 safety、Claude 7s 被误判 protocol、Mox 503 已重试但界面写「尝试 1 次」。前两条是 Carina 分类/握手 bug（工作树已修、**尚未安装进 live daemon**）；第三条是上游 503 + 计数诚实度。

DeepSeek Harness（Cordis「一切皆插件」）**不得**作为 day-one 架构。可偷 Trajectory 不变量、Goal 激活语义、会话内 Schedule；不可偷插件核。

### Top 5 优先行动

| # | 行动 | 用户价值 | 状态 |
|---|------|----------|------|
| 1 | **安装 reasoner 握手修复**（Grok `consent_gate`、Claude thinking→text assistant、503 文案） | 首轮不再「办不了」 | 代码已在 WT，未 commit/install |
| 2 | **重试计数诚实**：TUI「尝试 N 次」= 执行 lineage，需并列 HTTP/ACP 耗尽 | 不再把 4 次 503 说成 1 次 | P0 残余 |
| 3 | **Compact 一等 transcript 条目**（receipt 已有，缺 Claude/OMP 式 compaction cell） | 长会话压缩可看见、可回放 | P1 |
| 4 | **Goal/续跑激活语义**（DSH：unload 必须 disarm） | 避免重启自动续跑 | P1 审计 |
| 5 | **R-01 回归铁门**（lifecycle + ScreenMode + steer 已产品化，缺持续 gate） | 防 12k/16k TUI 巨石回潮 | P1 过程 |

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
| Reasoner / 路由 | `reasoner.go` + grok/claude/codex CLI 隔离 | **SHIPPED** + **BUG**（握手/文案，WT 已修） |
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

用户视角短板：首轮 reasoner 失败、尝试次数语义、live daemon 仍是未打补丁的 0.8.23。

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
| G23-01 | 首轮 isolated CLI 握手（Grok settings / Claude partial assistant） | P0 |
| G23-02 | 失败「尝试 N 次」≠ HTTP/ACP 重试耗尽 | P0 |
| G23-03 | Compact 不是一等 transcript cell | P1 |
| G23-04 | Goal/续跑是否 unload 后仍 armed（待对照 `goals.go`） | P1 |

### WIP

- Native scrollback **partial**（`CAPABILITIES.md` #11）
- Semantic memory / HMS：默认 off，BYOK 才有

### TODO

- Commit + 安装 + 重启 daemon（G23-01 才能被用户摸到）
- R-01 回归铁门进 release companion
- 可选：DeepSeek clone 后再写 `01-dna-deepseek.md`

### BUG

- Live 0.8.23 daemon **不含** WT 握手修复
- 503 已重试 4 次，TUI 显示尝试 1 次（计数语义）

### LEGACY

- `~/.carina/daemon.sock` 全局布局（显式 mode）

### SLOP

- `app/mod.rs` 12225 行、`render.rs` 16778 行
- `go/worker` 头注释「Phase 3 remote」过期

### ISOLATED

- Grok ACP 私有线、Gateway pin、Nebutra sync、HMS、swarm_channel、xai-ratatui forks

### UNPRODUCTIZED

- best-of-n、swarm 通道、VS Code Marketplace、hosted Web Operator、HMS

**已闭合（不要当缺口重开）**：ISSUE-001…018、V001–V011、R-02 `/queue`、#28–#39 honesty。

---

## 6. Phase 5 — 路线图

### P0-A · 安装握手修复（0.5 人天）

- **价值**：Grok/Claude 首轮能过握手。  
- **参考**：本仓库 WT；Grok 1.0.5 `consent_gate`；Claude 2.1.220 `--include-partial-messages`。  
- **路径**：已实现 → commit/push → `make install` → 重启 runtime。  
- **验收**：隔离 `grok` inspect+ACP preflight 绿；实录 Claude thinking→text 流 `finish` 成功；503 文案含「暂时不可用」。  
- **回滚**：revert 9-file diff；分类器仍 fail-closed（未知 settings 对象仍拒）。

### P0-B · 重试计数诚实（1 人天）

- **价值**：Mox 503 显示「提供方重试 4/4 后仍不可用」，不是「尝试 1 次」。  
- **参考**：`agent.go` `RoutingRetryScheduled`；Grok `retry_state`。  
- **路径**：Failure cell 增加 `provider_attempts`；不改执行 lineage。  
- **验收**：503 四次重试后 TUI 同时可见 HTTP 耗尽；单次 Grok 握手失败仍为 1。  
- **回滚**：只投影字段。

### P1 · Compact 一等 cell（2–4 人天）

- **价值**：长会话看见「压缩了什么」。  
- **参考**：OMP `type:compaction`；Claude collapse；Carina `ContextCompacted` receipt。  
- **路径**：投影已有 receipt → SemanticCellKind，禁止假装 contextengine 压缩。  
- **验收**：live=replay；`engine=noop` 不出现假 savings。

### P1 · Goal disarm 审计（1 人天）

- **价值**：重启不会偷偷续跑。  
- **参考**：DSH goal activation 不持久化。  
- **路径**：读 `goals.go` / scheduler；若 armed 落盘则改 process-local。

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

**置信度**：Carina 路径高；竞品抽查高（版本未漂）；DeepSeek 中（无本地树）；Goal disarm 待源码确认（中）。
