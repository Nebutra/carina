# 调研:Agent Workflow 编排机制

来源:Anthropic Building Effective Agents;LangGraph(StateGraph/Pregel);Vercel AI SDK;CrewAI Flows;OpenAI Swarm。

## 原理:workflow vs agent = 谁掌控 control flow
- **Workflow**:LLM 步骤由**预定义代码路径**编排,顺序/分支/并行/终止由开发者写死,控制流**确定**。LLM 只做每步内的推理。
- **Agent**:LLM **动态决定下一步**,循环到停止条件。控制流**模型驱动/非确定**。
- carina 现在的 `agent.go` 就是纯 agent(ReAct loop),**还没有 workflow 层**。
- Anthropic 原则:用最简方案;workflow 给"well-defined 任务的可预测一致性",agent 给"步数不可预测的开放问题"(更贵更易错)。

## 五种模式(都可归约为「带有界环的 DAG」)
| 模式 | 结构 | 何时用 |
|---|---|---|
| **Prompt chaining** | 线性管道,step n 输出→n+1,步间可加程序化 gate | 任务可拆成**固定**子任务 |
| **Routing** | 分类 LLM 打标签→代码 dispatch 到专门路径 | 输入类别分明、分类可靠 |
| **Parallelization** | 并发+程序化聚合。Sectioning(拆独立子任务)/ Voting(同任务跑 N 次) | 子任务可并行 / 多次尝试提高置信 |
| **Orchestrator-workers** | 中央 LLM **运行时**动态分解、派 worker、综合 | 子任务**无法预先确定** |
| **Evaluator-optimizer** | 生成→评估打分反馈→**循环**到通过/上限 | 有明确评估标准、迭代有效 |

## LangGraph 核心抽象(最完整)
- **StateGraph**:声明 typed **State** → `add_node`/`add_edge`/`add_conditional_edges` → `.compile()`。
- **State + Channels + Reducers**:state = typed channels;reducer `(V,V)->V` 定义写入如何合并(默认 overwrite;`add_messages`/`add` 为 append)。**state 不可变**,节点返回 partial update。这是并发写 + replay 确定性的关键。
- **Nodes** = 纯函数 `state -> partial update`;**Edges** = normal / conditional(函数看 state 返回下一节点名 → 实现 route 和 loop)。环是一等公民(支持 agent loop / evaluator-optimizer)。
- **执行 = Pregel / super-step / BSP**:离散 super-step,所有 active 节点并行跑→同步 barrier→reducer 合并→订阅了被更新 channel 的节点下一步 active。无 active 则终止。
- **Send API**:动态 map-reduce fan-out(运行时决定并行分支数)= orchestrator-workers 原语。
- **Checkpointer**:每 super-step 后持久化 → 崩溃恢复 + human-in-the-loop + time-travel。`recursion_limit` 界环。
- **Subgraphs**:编译后的图可作节点 → 组合。

## 其它框架
- **Vercel AI SDK**:workflow 就是 TS control flow(await 链 / Promise.all / while+break)—— **不需要图引擎,宿主语言的有界步骤编排就够**。
- **CrewAI**:Crews(自主角色协作)vs Flows(确定性 `@start`/`@listen`/`@router`)。"Crews for autonomy, Flows for control",可组合(Flow 编排,步骤内委托 Crew)—— 正是 carina 目标:确定性外层包自主内层。
- **Swarm**:agent + handoff(去中心化路由,可审计性差,已被 Agents SDK 取代)。

## 哲学:为何 workflow 常优于全自主
控制流是**代码/数据而非模型输出**,所以:可预测可重复(无长循环累积错误)、可审计可回放(plan 是可 review 的 artifact)、可测试(每步近纯函数)、成本/延迟有界、失败隔离可治理(retry/timeout/approval/能力上限 per-step)。成熟设计 = **hybrid**:确定性 workflow 管外层,个别步骤是有界自主 agent。

## → carina 设计(agent 给的方案,直接可实现)
**加一个薄 `go/workflow` 引擎坐在 scheduler 之上**。carina 已有 workflow 引擎需要的 4 块:scheduler(Task 状态机)、session+durable state、**哈希链 event log(= 免费 checkpointer,replay 重建状态)**、kernel-gated effects + worker pool + Bus。

- **workflow 定义 = 纯数据**(新 `protocol/schemas/workflow.schema.json`):steps(kind: agent/tool/model/router/evaluator/subworkflow)+ edges(normal/conditional/join)+ state(channels+reducers)+ limits(max_supersteps/max_visits)。**条件用沙箱表达式(JSONLogic/CEL,不可执行代码)** —— 相对 LangGraph 任意 Python 节点的安全+审计优势。
- **执行 = super-step/BSP 循环**:算 ready 集(in-edges 满足+条件真+visits<上限)→ 并发派 scheduler.Task → barrier → reducer 合并 State → 评估 out-edges → 记 EdgeTaken → Superstep++。
- **五模式映射**:chaining=normal edges;routing=router step+conditional;parallelization=fan-out+join(sectioning)或 replicas+append(voting);orchestrator-workers=step 运行时发 Send;evaluator-optimizer=grade→patch 有界环。
- **免费复用**:checkpoint/resume=replay event log;HITL=step.requires_approval 复用 waiting_approval+pendingCmds+task.action.approve;终止=max_supersteps+max_visits。
- **★ 安全差异点**:每 step 声明 `risk_ceiling`,kernel 强制 —— workflow 能**编排**能力但不能**提权**;编排是确定性 Go,step 内副作用仍走 kernel→Zig,审计保留 Go→Rust→Zig actor。
- 新事件:WorkflowCreated/StepStarted/StepCompleted/StepFailed/StepSkipped/StepAwaitingApproval/EdgeTaken/WorkflowCompleted/WorkflowFailed(进哈希链,actor=go)。
- 新 RPC:workflow.define/submit/status/cancel + workflow.events.stream。CLI:pi workflow submit/status/replay。
- **构建顺序**:① linear chaining+events+replay ② conditional edges(routing+evaluator 环)+沙箱求值 ③ static parallel fan-out/join+reducers ④ dynamic Send ⑤ HITL+subworkflow。
