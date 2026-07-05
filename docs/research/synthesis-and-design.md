# 综合:四机制 → carina 集成蓝图

四路一手调研(见同目录 claude-code-subagent / workflow-orchestration / coding-agent-loop / codex-goal + carina-extensions-findings)收敛出的统一设计。

## 统一洞察:为什么 carina 是这些机制的理想基座

四份调研反复指向同一结论 —— carina 已有的四个底牌,恰好是这些机制最安全的落地基础,而且能让 carina 版本**比原版更可控、可审计**:

| carina 底牌 | 让哪个机制受益 |
|---|---|
| **独立 session**(独立 profile + 独立 event log + workspace 边界) | sub-agent 的 context 隔离;workflow step 的隔离 |
| **Rust kernel 内核级能力强制**(非 prompt 软约束) | 能力单调衰减(child⊆parent);Codex 两轴解耦;workflow step 的 risk_ceiling |
| **哈希链 event log** | 免费的 workflow/loop checkpointer(replay 重建状态);防篡改审计 |
| **现有 approval 设施**(waiting_approval + pendingCmds + task.action.approve) | goal 的分级审批;workflow 的 human-in-the-loop 全部复用 |

## 四机制的统一关系模型
```
        workflow  (确定性 DAG 编排,super-step/BSP)
           │  step 可以是 ↓
   ┌───────┼─────────┐
 agent   tool     sub-agent
 step    step     step (隔离受限子 loop)
   └──────────────────┘
           │ 每个 agent step 都是一个 ↓
        loop  (ReAct: thought→action→observation)  ← goal 建在它上面
                                                      (两轴解耦 + success_criteria + 失败升级 + 审批记忆)
```
- **loop** = 地基。单 agent ReAct。
- **goal** = loop 的执行策略层:能力×许可两轴解耦、可选客观成功判据、沙箱优先失败升级、审批记忆。
- **sub-agent** = 把子任务委托给一个隔离受限的子 loop(独立 session,能力单调衰减)。
- **workflow** = 把多个 step(agent/tool/sub-agent)按 DAG 确定性编排。

## 实现范围与顺序(每块真能跑 + 测试)

### 阶段 1 — Loop 增强(地基,优先级最高)
让单 agent 生产级。核心:**喂给模型的 context 是 event log 的「有界投影」,审计链永保全,压缩只作用于模型视图。**
- typed transcript(取代 strings.Builder)——使能压缩/缓存/并行对齐
- **上下文压缩**:省略(留最近 N 条 verbatim,旧输出→`[elided]`,Pinned 永不省)+ 超预算摘要 head(可用更便宜模型)
- 模型调用重试:指数退避 + 尊重 Retry-After,5xx 重试、context_overflow 不重试→压缩(当前 Think 出错即死)
- LoopGuard 防循环:动作指纹去重(MaxRepeat)+ 无进展检测(MaxNoProgress)
- 优雅降级:非 done 终止时产出部分结果 + 已应用 patch ID,标记 degraded(仿 SWE autosubmit)
- requery:畸形动作紧凑回喂、不计正式轮(maxRequeries=3)

### 阶段 2 — Goal 机制(建在 loop 上,改动集中在 policy+agent)
- **两轴解耦**:Task 加 `approval_mode`(untrusted/on_request/never),从 Profile 拆出。`carina-policy.evaluate` 加 approval_mode 入参:never 把 RequiresApproval 降 Allowed(profile 兜底);untrusted 把非白名单命令升 RequiresApproval。
- **success_criteria**(可选):done 时跑客观判据(command_zero_exit/file_exists/grep_absent),失败回灌 continue;空=模型自判(Codex 默认)。
- **审批记忆**:session 级 approvalCache(capability+resource 前缀),ApprovedForSession 命中即 Allowed,砍疲劳。
- 预设:suggest/auto_edit/full_auto 映射到 (Profile, ApprovalMode) 组合。

### 阶段 3 — Sub-agent(独立受限子 session)
- **AgentSpec**:markdown + frontmatter(name/description/tools→capability grants/model/max_turns),从 `~/.carina/agents/*.md` + `.carina/agents/*.md` 加载。
- **spawn_agent 工具**:主 loop 可派生 subagent。派生本身是受能力门控的副作用(父需持 spawn 能力)。
- **能力单调衰减(核心安全不变量)**:`child.Profile = (parent ∩ spec.grants) \ denies`,强制 child⊆parent,子永不提权,kernel enforce。
- 子 = 独立 session(独立 profile + 独立 event log + Depth<5)+ 独立 loop。
- 三模式:single / parallel(goroutine fan-out)/ chain(用 {previous} 传递)。
- 结果回传:final message 逐字 + usage,大产物走引用。全程审计,子事件挂回父 spawn 调用。

### 阶段 4 — Workflow(DAG 引擎,坐在 scheduler 之上)
- **workflow schema**(纯数据):steps(kind: agent/tool/model/router/evaluator/subworkflow)+ edges(normal/conditional/join)+ state(channels+reducers)+ limits。条件用沙箱表达式(不可执行代码)。
- **super-step/BSP 引擎**:算 ready 集 → 并发派 Task → barrier → reducer 合并 State → 评估 edges → 记 EdgeTaken。
- 五模式映射:chaining=normal;routing=router+conditional;parallel=fan-out+join / replicas+append;orchestrator=运行时 Send;evaluator=有界环。
- 免费复用:checkpoint=replay event log;HITL=step.requires_approval 复用现有审批;终止=max_supersteps+max_visits。
- 每 step 声明 risk_ceiling,kernel 强制 —— 能编排能力但不能提权。
- 新事件 + RPC(workflow.submit/status) + CLI。

## 安全不变量(贯穿四机制,carina 的差异化)
1. 一切副作用经 kernel(即便 spawn subagent、跑 success_criteria、workflow step)。
2. 能力只能衰减不能提升(subagent child⊆parent;workflow step risk_ceiling≤profile)。
3. 编排是数据/确定性代码,不是模型输出(workflow 条件是沙箱表达式)。
4. 审计链永保全(压缩只动模型视图;每个 spawn/step/approval 都进哈希链)。
