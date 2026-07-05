# 调研:Claude Code Sub-agent 机制

来源:code.claude.com/docs 官方 sub-agents + Agent SDK subagents;Anthropic 工程博客
(multi-agent-research-system、building-effective-agents、when-to-use-multi-agent)。

## 原理:四个"独立"
Sub-agent = 主 agent 通过 `Agent` 工具(旧名 `Task`)派生的**完整隔离 Claude 实例**:
1. **独立 context window**(最关键):看不到主对话历史/已读文件;所有中间噪音留在子级。
2. **独立 system prompt**:用自己的 prompt,不是主 Claude Code system prompt。
3. **受限工具集**:`tools` allowlist / `disallowedTools` denylist。
4. **独立 model / effort**。

## 通信:单通道下发 + 逐字回传
- **下发(父→子)**:唯一通道 = `Agent` 工具的 `prompt`(委派消息)。父必须把所需 context 全写进去。
- **回传(子→父)**:子的**最终一条消息逐字**作为 tool_result 返回。噪音留子级,父只拿信号(~200 token vs 内联数千)。
- 子消息带 `parent_tool_use_id` 归属;完成返回 `agentId` 可 resume。

## 哲学
- **search 的本质是压缩**:探索会污染工作 context(读 50 文件找 1 个),subagent 把噪音关在自己窗口。
- 唯一重要决定 = **隔离边界**:子级需不需要知道别人在干嘛?研究类几乎不需要。
- 并行主要为 **thoroughness(覆盖独立搜索路径)**,顺带 latency(复杂查询 -90%)。多 agent 内部 eval 比单 agent +90.2%。
- **POLA 最小权限**:限工具既安全又聚焦又省钱(探索路由到 haiku)。
- **昂贵所以克制**:多 agent token 3–10×;token 用量解释 ~80% 性能方差。
- 只在 ① context 污染 ② 独立并行探索 ③ 强专业化 时用;**反模式**:顺序阶段(plan→impl→test)、紧耦合、频繁同步。
- **context-centric 而非 problem-centric 分解**:按 context 能否隔离切,不按角色切。

## frontmatter 字段
必填 `name`/`description`(description 是路由依据);可选 `tools`/`disallowedTools`/`model`/`permissionMode`/`maxTurns`/`skills`/`mcpServers`/`hooks`/`memory`/`isolation`(worktree)/`background`/`effort`。
嵌套 depth 硬上限 = 5;resume via agentId;fork 变体(继承父 context + 复用 prompt cache)。

## → carina 落地(调研 agent 给的设计,高度契合)
- **1 subagent = 1 独立 Session**:ParentID / AgentType / Depth / 独立 Context / 独立 Profile(kernel 强制) / 独立 EventLog / model。复用主 loop 实现。
- **AgentSpec = frontmatter 能力化版**:`tools` → capability grants,由 **Rust kernel 内核级 enforce**(比 Claude Code 的 prompt 软约束更强)。
- **spawn_agent 本身是受能力门控的副作用**:父需持 spawn 能力 + 目标类型在 SpawnableTypes allowlist。
- **★ 能力单调衰减(核心安全不变量)**:`child.Profile = (parent.Profile ∩ spec.Grants) \ spec.Denies`,且强制 `child ⊆ parent`,子永远无法提权。
- **结果回传**:final message 逐字 + agentId + Usage;大产物走 content-addressed BlobRef 引用(避免 telephone game)。
- **并行** = goroutine + 独立 session,channel 回收;foreground 阻塞 / background 挂 session tree。
- depth/maxTurns/spawn-allowlist 三重防跑飞。
