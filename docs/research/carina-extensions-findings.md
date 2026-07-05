# 一手调研:pi (PiPi Agent) 社区扩展如何实现这些机制

来源:`earendil-works/pi` 的 `packages/coding-agent/examples/extensions/` + `src/core/compaction/`。
这是可运行的开源实现,直接指导 carina 的集成。

## 1. Sub-agent(subagent 扩展)

**核心设计:**
- Agent 定义 = markdown 文件 + YAML frontmatter:`name / description / tools(逗号分隔) / model` + body(system prompt)。**与 Claude Code 的 agent 格式一致。**
- **隔离 context**:每个 subagent 跑在**独立的 `pi` 子进程**里 —— 独立 context window、独立 system prompt、受限工具集、可指定不同(更便宜)的 model。
- 三种调用模式(一个 `subagent` 工具):
  - `{agent, task}` — 单个
  - `{tasks: [...]}` — 并行(最多 8,并发 4);每个结果回传父模型,capped **50 KB/task**
  - `{chain: [...]}` — 顺序,用 `{previous}` 占位符把上一步输出传入下一步
- **发现**:user-level `~/.pi/agent/agents/*.md`(总加载)+ project-level `.pi/agents/*.md`(需 `agentScope: project|both`,且交互时确认 —— 因为 repo 控制的 prompt 能指示读文件/跑命令,是信任边界)。
- **结果回传**:只把最终输出返回父,中间状态不共享(context 隔离的关键)。失败时回传 stderr/错误诊断。
- 样例 agent:scout(Haiku,快速侦察,返回压缩上下文)、planner(Sonnet,只读只规划)、reviewer、worker(全能)。

**对 carina 的直接启示:** carina 的**独立 session**(独立 permission profile + 独立 event log + 独立 workspace 边界)天生就是 subagent 隔离的载体。subagent = 一个受限 profile 的子 session + 独立 agent loop,结果回传父 session。

## 2. Plan / Goal(plan-mode 扩展)

**核心设计:**
- **read-only 探索模式**:禁用 edit/write 工具,bash 只允许 allowlist 的只读命令(`isSafeCommand`)。安全地先探索。
- 从模型输出的 `Plan:` 段落提取编号步骤 → todo 列表。
- 执行阶段:模型用 `[DONE:n]` 标记完成第 n 步 → 进度 widget(`3/7`)。
- 状态持久化:plan/todos 存 session,resume 后存活。
- 流程:**规划(只读安全)→ 用户确认「执行计划」→ 执行(可写)→ 逐步标记完成**。

**对 carina 的启示:** goal 机制 = task 加 `plan`/`success_criteria` + 一个 `read-only`(或 `plan`)profile 做探索阶段,再切到 `safe-edit` 执行。approval_mode 映射到 kernel 的 approval。

## 3. Loop 上下文管理(compaction / handoff)

**compaction**(`src/core/compaction/compaction.ts`,26 KB 核心逻辑):
- 钩子 `session_before_compact`。当 context 太长,把旧 messages **summarize 成一个 summary**,丢弃旧 turns,只保留 summary + 最近若干 turn。
- 可用**更便宜的模型**(如 Gemini Flash)做 summarization。
- 关键状态:`messagesToSummarize / turnPrefixMessages / tokensBefore / firstKeptEntryId / previousSummary`。
- 有 `branch-summarization`(分支摘要)。

**handoff**(替代 compaction 的另一思路):不压缩(有损),而是让模型**提取「下一步需要的关键上下文」(决策/文件/任务)生成一个自包含的新 prompt**,开一个聚焦的新 session。适合任务切换。

**todo**:任务列表存在 tool result details 里(不是外部文件),这样 session 分支时状态自动正确。

**对 carina 的启示:** agent loop 现在的 transcript 是无限拼接的。生产级需要:event log/transcript 超阈值时触发 compaction(summarize 旧轮次),用同一个 reasoner 或更便宜的做摘要。

## 综合:四机制的关系
- **loop** 是地基(单 agent 的 ReAct 循环 + 上下文管理)。
- **goal/plan** 是 loop 之上的「先规划后执行 + 成功标准 + 审批分级」。
- **sub-agent** 是「把一个子任务委托给一个隔离的、受限的子 loop」。
- **workflow** 是「把多个 agent 步骤按 chain/parallel 确定性编排」(scout→planner→worker 就是 workflow)。

pi 用「独立进程 + markdown agent + chain/parallel 工具」实现,carina 用「独立 session + kernel 受限 profile + scheduler 编排」实现会更安全、可审计(每个 subagent 的每步副作用都进 kernel + 哈希链审计)。
