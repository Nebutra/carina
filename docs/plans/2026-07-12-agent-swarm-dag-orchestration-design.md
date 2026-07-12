# Agent Swarm：任意依赖图、集群式、高并发多智能体编排设计

状态：**P1-P5 已按当前产品边界实现（as-built，2026-07-12）**。本文前半保留最初设计推导，以下表格是代码事实的权威摘要；凡与后文早期设想冲突，以本表和 `docs/workflows.md`/`docs/worker-executor.md` 为准。

| 阶段 | As-built | 已验证 | 明确边界 |
|---|---|---|---|
| P1 | `execution_mode: streaming`、增量入度调度、16 并发硬顶、1000 节点硬顶、默认失败隔离、per-step `fail_fast` | 200 节点真实 subagent fan-out/fan-in；200 ready 节点并发不超过 16；取消/失败隔离 | 并发值目前是常量，不是每 workflow 配置 |
| P2 | typed `input`、沙箱条件、generator `spawn_steps`、深度/总量/环/冲突校验 | 动态 sibling 依赖、条件分支、typed number、非法图失败 | 没有真实图环/loop 节点；循环仍需显式展开 |
| P3 | run-scoped `swarm_publish`/`swarm_receive`、`SwarmMessage` kernel gate、每 channel 500 条有界窗口、丢弃审计 | 权限拒绝、订阅隔离、并发发布、窗口淘汰、rollup 统计 | channel 是 daemon 内存态；remote executor 不接入此 broker；daemon 重启后不回放中途消息 |
| P4 | `RemoteDispatch`、dispatch/lease/renew/report、generation fencing、掉线重排队、worker-pool affinity、WSS scoped token、worker report 瞬时失败有界重试 | 真实 RPC lease/report、错误 worker 不可租 affinity task、stale generation 拒绝、WSS hello/auth | 当前 remote 节点是 `carina-worker`+operator executor，不是“每节点完整 daemon+kernel”；仍需 2+ 真实机器部署验收 |
| P5 | run token ceiling、完成/失败/跳过/running/queued 聚合、channel activity、durable operator detail、CLI foreground/background/status/control | 本地/remote rollup、动态节点进入 detail、stop 真取消、pause 仅冻结新节点 | remote result v1 可上报 token usage；未上报的 executor 仍明确显示 `unmetered_steps`/`observed_usage_only`，不能伪称为 0 或完整预算 |

动态图的定义和结果分开持久化：graph journal 先于 generator completion 提交，并用 step definition hash 区分“同定义幂等重放”和“同 ID 不同定义冲突”。`workflow.stop` 取消真实 run context；`workflow.pause` 不伪装成进程挂起，只停止新节点 admission，已运行节点继续到终态。

## 0. 一句话定位

现有 `WorkflowStep`/`Needs` DAG（`go/daemon/workflowspec.go`）+ `subagent.go` 的 spawn 机制，是"十几个步骤、单 daemon、按层同步跑完"的编排层。Agent Swarm 不是替换它，而是在它之上加一层：**当图规模到几十上百个节点、需要节点间在运行中互相通信、需要跨机器分摊时，切到一种不同的调度语义和分发层**，图的声明格式和治理模型保持兼容延续。

## 1. 目标与非目标

**目标**
- 支持任意依赖图（不仅是静态 DAG——支持运行时动态生成节点/边、条件边、有界环/迭代节点）。
- 单次运行支持成百上千个并发 agent 节点。
- 节点间除"上游完成→下游拿结构化输出"外，支持运行中的活体消息传递（不必等上游整个跑完）。
- 集群式：一个 swarm 运行可以跨多台机器上的多个 Carina daemon 实例分摊执行，而不是单 daemon 内塞更多 goroutine。
- 治理不打折：每个节点的每个副作用仍然过本机 kernel 网关+审计链；没有"因为是 swarm 所以绕过"的例外。

**非目标**
- 不做通用分布式计算框架（不是 Spark/Ray 的替代品）；这是"AI agent 任务图"专用编排器。
- 不引入可执行代码的图定义格式（沿用本 session 已确立的原则：图是纯数据，条件是沙箱表达式，不是 handleSteps 式 TS/JS）。
- 第一阶段不做跨云跨租户的多机构联邦调度；先做"用户自己拉起的一组机器"这个场景。

## 2. 实现前基线（历史快照）

本节记录立项时的问题，不再描述当前代码。当前实现状态见文首 as-built 表。

| 组件 | 现状 | 位置 | 对"成百上千并发节点"意味着什么 |
|---|---|---|---|
| Workflow DAG | `Needs []string` 依赖，**按层同步跑**：收集全部 ready 节点→起 goroutine→`wg.Wait()`→下一层 | `go/daemon/workflow.go:115-248` | 一层里最慢的节点拖住整层；不是真正的流式 DAG 执行 |
| 步骤数硬顶 | `maxWorkflowSteps = 64` | `workflow.go:17` | 直接不够用 |
| 一失败即全灭 | 任一 step 出错，整个 workflow 返回错误 | `workflow.go:245` | 大图里一个边缘节点挂了不该拖死主干 |
| 全局并发闸 | `d.runSem = make(chan struct{}, 8)`（默认），daemon 级单一信号量 | `daemon.go:146,427` | 全 daemon 同时只能跑 8 个任务，与"上百"差两个数量级 |
| spawn 扇出 | `act.Tasks` 每项起一个 goroutine，**无上限** | `subagent.go:43-58` | 500 项 = 瞬间 500 goroutine，无背压 |
| 子智能体嵌套 | `maxSubagentDepth = 4` | `subagent.go:15` | 树形嵌套够用，但 swarm 要的是"图"不是"树" |
| Worker 池 | `Local/Remote/CI/Sandbox` 四种 kind，**只有 Local 真正实现** | `go/worker/worker.go` | 集群分发目前是骨架，没有真实跨机执行 |
| Lease 分发框架 | `Lease`/`LeaseMatching`/`ReapExpiredLeases`，at-least-once 语义**已经在** | `go/scheduler/dispatch.go:17-83` | 形状是对的，缺真正的远程 worker 实现——这是最该复用的既有资产 |
| 事件总线 | 单一全局 `Bus`，每订阅者 256 条 pending 缓冲，慢订阅者被断开 | `go/daemon/bus.go:12,152-197` | 上千节点的事件量会持续触发慢订阅断连 |
| 预算 | 只有 per-task 扁平预算，无树形/聚合预算 | `go/daemon/budget.go` | 无法给整个 swarm 设总预算天花板 |
| **内核/审计吞吐（最关键瓶颈）** | 每次 capability 请求：daemon→单线程 kernel service 的同步 JSON-RPC→每 session 的 `Mutex<AuditState>`→同步文件 append/fsync | `go/kernel/kernel.go`；`crates/carina-kernel/src/bin/carina-kernel-service.rs`；`crates/carina-audit/src/lib.rs` | 当前 kernel service 的单线程请求循环串行化全 daemon 的治理请求；每个 session 的审计锁还会串行该 session 内的写入。扩大节点并发前必须实测这条链路 |
| 通信 | 父→子只有"最终 summary 字符串"，`${step_id}` 文本插值 | `workflow.go` interpolate；`subagent.go` | 没有运行中消息传递，没有结构化 typed 输出 |
| 可信外部通道 | `go/channels`：HMAC 签名、sender 注册、按 (sender_id,event_id) 去重、权限中继，**已存在且成熟** | `go/channels/channels.go` | 这是离"节点间消息通道"最近的既有资产，应该复用其信任模型而不是另起一套 |

**结论**：现有 DAG 执行是"正确性优先的小规模同步 BSP 变体"；`workflow-orchestration.md` 提议的 Pregel/super-step 模型进一步规范化了这个方向，但 super-step 的**全局 barrier** 语义本身就与"上百节点、异构耗时、高并发"冲突——一步内最慢的节点决定整步耗时。Swarm 需要的是不同的执行语义，不是把 barrier 做得更快。

## 3. 架构总览

> 设计图是目标架构，不是当前部署拓扑。As-built 的 coordinator 位于主 daemon，远端执行端是 `carina-worker` 加 operator-supplied executor；它通过 WSS/worker credential/lease fencing 受控，但并不自动拥有独立 Carina kernel 与 audit store。

```
┌─────────────────────────────────────────────────────────────────┐
│  Swarm Coordinator（新增，逻辑单例，可选双活）                    │
│  - 持有图的权威状态（节点/边/就绪队列/结果存储）                   │
│  - 不执行任何 agent，只做调度决策 + 结果路由                       │
│  - 自身状态可持久化/可重放（同一套 event-log 哲学，非新发明）       │
└───────────────┬─────────────────────────────┬─────────────────┘
                 │ lease（复用 dispatch.go 语义）│
         ┌───────┴───────┐               ┌─────┴─────────┐
         │  Carina Daemon │      ...      │  Carina Daemon │   ← 集群里的每台机器
         │  (worker node) │               │  (worker node) │      一个完整的 kernel+
         │  本机 kernel   │               │  本机 kernel   │      daemon 栈，独立网关
         │  本机 audit    │               │  本机 audit    │      +审计，互不阻塞
         └───────┬───────┘               └───────┬────────┘
                 │ 节点内 spawn 出的 agent 群（现有 subagent.go 机制不变）
          [agent] [agent] [agent] ...      [agent] [agent] ...
```

**核心决策**：不是让一个 kernel 服务撑住上千并发请求，而是**每个 worker 节点跑自己完整的 daemon+kernel+audit 栈**，coordinator 只做图调度和跨节点消息路由。目标拓扑用 N 条独立的 kernel service 治理链路分散负载；每条链路内部仍是独立 session 的 audit mutex，而不是共享一个全局 mutex。这样既对应"集群式"要求，也保持治理不打折（每台机器的每个副作用仍然被它自己的 kernel 完整网关+审计，不存在"coordinator 说了算、本地不检查"的旁路）。当前 as-built remote worker 仍是 operator-supplied executor，并不自动拥有完整本地 kernel，边界见 §3 图前说明。

## 4. 图模型

延续 `workflow-orchestration.md` 的词汇（steps/edges/state channels+reducers），扩展三类要素：

### 4.1 节点（Node，即研究文档里的 step）

```jsonc
{
  "id": "analyze_module_7",
  "kind": "agent",              // agent | tool | router | evaluator | generator | join | subworkflow
  "agent": "code-reviewer",     // kind=agent 时：spawn 哪个 AgentSpec
  "input": {                    // 结构化输入，非字符串插值——依赖 dep 输出的字段直接映射
    "target": "${dep:scan_module_7.output.files}"
  },
  "needs": ["scan_module_7"],           // 静态依赖（control-dependency）
  "consumes_channel": ["progress:scan"], // 订阅的运行中消息通道（见 §6），不等待其结束
  "risk_ceiling": "safe-edit",           // 复用现有 profile 概念，节点级能力上限
  "on_failure": "isolate",               // isolate(默认) | fail_fast | retry
  "retry": { "max_attempts": 2, "backoff_ms": 2000 },
  "affinity": { "worker_pool": "gpu-heavy" }  // 集群模式下的调度提示，可选
}
```

关键变化：`input` 是**结构化字段映射**，不是字符串模板插值——这需要 agent 的 "done" 输出走结构化 envelope（本 session 已经在 best_of_n 的 `candidateEnvelope` 和 public-subagent-dsl 的 `input_schema`/`output_schema` 里验证过这个模式是可行的，直接复用）。

### 4.2 边（Edge）

三种边类型，对应三种真正不同的语义（研究文档没有区分这三种，是本设计的核心扩展点）：

| 边类型 | 语义 | 何时触发下游 |
|---|---|---|
| **control**（现有 `needs`） | "B 必须等 A 完成" | A 终态（成功/失败/跳过）后 |
| **data**（新） | "B 需要 A 输出的某个字段" | 与 control 一起解析，但允许**部分就绪**——B 依赖 A、C、D 三者的不同字段时，只要各自产出即可增量填充，不必等三者全部完成才评估 B 是否 ready（真正的流式而非批式） |
| **channel**（新，核心扩展） | "B 想在 A **运行过程中**收到 A 发出的消息，不是等 A 结束" | A 每次向该 channel `publish()` 时，B（若已启动且订阅）实时收到；不影响 B 的 needs 依赖判定 |

### 4.3 条件与动态图

- 条件边沿用研究文档方案：CEL/JSONLogic 沙箱表达式，作用在上游结构化输出上，**不是可执行代码**——与本 session 已确立的"图是纯数据"原则一致。
- 动态节点生成：`kind: "generator"` 节点的 "done" 输出里可以携带 `spawn_nodes: [...]`（新节点+边的声明），由 coordinator 校验后并入运行中的图。安全边界：
  - 单次运行节点总数硬顶（可配置，默认建议 5000，替代现有 `maxWorkflowSteps=64` 的角色，但作为**运行时动态上限**而非编译期常量）。
  - 动态生成的节点继承 `attenuate(parent_ceiling, requested)`（复用现有 `subagent.go:101` 的能力单调递减原语，图节点和子智能体在这一点上共享同一条治理规则，不新造一套）。
  - 生成深度上限（防止 generator 生成 generator 生成 generator 的失控链）。
- 环：图本身仍是 DAG（禁止真实图环，保持调度可判定性）；"循环"通过专门的 `kind: "loop"` 节点表达（内部对同一子图重复触发，直到条件满足或 `max_iterations`），与研究文档的 `max_supersteps`/`max_visits` 思路一致，只是把它做成显式节点类型而不是隐式图属性。

## 5. 调度语义：从"同步 barrier"到"流式 ready 队列"

这是与 `workflow-orchestration.md` 提议的 Pregel/BSP 模型的**关键分歧点**，需要讲清楚为什么：

BSP/super-step 模型（研究文档方案）：每一步，算出全部 ready 节点→并发跑→**等全部跑完**→reducer 合并→再算下一批 ready。正确性推理简单（每步后状态是确定的合并点），适合几十节点、耗时相近的场景。

Swarm 场景的问题：几百个节点耗时高度异构（有的 agent 3 秒读完文件说 done，有的要跑 5 分钟的测试）。如果坚持 barrier，快节点跑完后干等，直到本层最慢的节点收尾——延迟和吞吐都被拖垮，且完全没利用"下游只依赖这一个快节点、不依赖那个慢节点"的信息。

**方案**：Swarm 模式下调度器改为纯粹的 ready-queue 驱动，不设全局 barrier：

```
coordinator 维护:
  - 每个节点的入度计数（未满足的 control+data 依赖数）
  - ready_queue（入度归零的节点，按 affinity/优先级排序）
  - 一个节点完成 → 原子递减所有下游节点的入度 → 入度归零者立即入队，不等待"这一层"的其它节点
```

这本质是把现有 `workflow.go` 的"收集全部 ready→wg.Wait()→下一层"循环，换成经典的拓扑排序增量版本（Kahn 算法的流式变体），配合有界并发池（见 §5.1）。BSP 模型不废弃——`workflow-orchestration.md` 的引擎继续服务"十几步、要强确定性回放"的场景；两者共享同一套图 schema，运行时按 `execution_mode: bsp | streaming` 选择调度器，互不冲突。

### 5.1 有界并发，不是无限 goroutine

替换 `subagent.go:43-58` 的"每项一个 goroutine"模式为固定大小的 worker 池：

```go
type swarmWorkerPool struct {
    ready   chan *SwarmNode      // 有界 channel，背压天然存在
    workers int                  // 每个 daemon 节点的本地并发上限（远高于现有全局 8，但仍是有界值，可配置，如 64-256，按机器资源调）
}
```

节点从 `ready_queue` 出队时先尝试拿池里的一个 worker slot；池满则在 ready_queue 里等待，不会像现在这样瞬间起 500 个 goroutine 压垮调度器和内核。这与本 session 已经确立的"Workflow 工具本身的 `pipeline()` vs `parallel()`"是同一个思路的复现：`pipeline()` 让阶段间不设 barrier、item 独立流动；`parallel()` 才是 barrier。Swarm 调度器本质是把这个思路从"工具调用工作流"下沉到"agent 编排"这一层。

## 6. 节点间活体通信

复用 `go/channels`（HMAC 签名、sender 注册、按 `(sender_id, event_id)` 去重、权限中继）的信任模型，而不是另起一套：

- 每个 swarm 节点在启动时，coordinator 给它签发一个 channel sender 身份（复用 `go/channels` 的注册机制），作用域限定为"这次 swarm 运行内的合法目标节点集合"。
- 节点通过新工具 `swarm_publish`（`{"tool":"swarm_publish","channel":"progress:scan","payload":{...}}`）向命名通道发消息；目标节点若在 `consumes_channel` 里订阅了该通道，实时收到。
- 治理：`swarm_publish` 走新增 `Capability::SwarmMessage`（模式同本 session 已加的 `Capability::ContextCompress`/`Capability::SubagentSpawn`——每次发布仍是一次 kernel 请求+审计事件，不是绕过网关的旁路总线）。
- payload 走与节点输出同样的结构化 schema 约束（不是自由文本），避免变成"prompt injection 快递员"。

## 7. 集群分发：填平 Worker Lease 的空缺

`go/scheduler/dispatch.go` 的 `Lease`/`LeaseMatching`/`ReapExpiredLeases` 已经是**正确的形状**（at-least-once、租约过期重新入队、能力匹配），只是缺一个真正的远程 worker 实现。设计：

1. **Remote Worker 进程** = 一个普通的 Carina daemon 实例，额外跑一个 `worker-agent` 侧车：轮询 `work.poll`，`Lease` 到节点后，在本机以正常方式（`agent.dispatch`/`spawn`）执行，通过 `work.report` 把结构化输出和错误状态回传给 coordinator。
2. **Coordinator 侧**：把 swarm 节点当作 dispatch 队列里的任务，按 `affinity` 提示做能力匹配（复用现有 `supports func([]string) bool`），租约过期（worker 掉线/卡死）自动重新入队，保持 at-least-once。
3. **治理边界**：coordinator 与 worker 节点之间的通信本身需要新的 `Capability::RemoteDispatch`（跨进程/跨机器信任边界，值得独立于 `SubagentSpawn` 单独治理——一台机器决定信任另一台机器执行代码，这和"同一进程内 attenuate 一个子会话"是不同量级的信任决策，应该有自己的审批语义，而不是复用一个语义更弱的能力）。
4. 第一阶段只做"用户自己的机器群，coordinator 显式配置 worker 列表"，不做自动发现/弹性伸缩——那是后续阶段的事，先把租约+执行+回传这条主链跑通、跑对。

## 8. 内核/审计吞吐：水平扩展与单 daemon 瓶颈

这是本设计对现有瓶颈最直接的回应。`AuditLog` 由 session-scoped `Kernel` 持有，所以 `Mutex<AuditState>` 是 per-session；全 daemon 的治理请求目前实际串行在 `carina-kernel-service` 的单线程 stdin/JSON-RPC 循环。把 1000 个 agent 塞进一个 daemon 仍会撞上单服务进程的治理吞吐上限，但不能把原因误归为一把跨 session 的审计锁。

水平扩展仍是主要容量手段：swarm 按 §7 分摊到 N 台机器上的 N 个 daemon，每个 daemon 只处理自己承接的节点，从而分散单 kernel service 的治理负载。但这不是单机吞吐优化的替代品；单 daemon 的上限需要用真实基准量化，再决定是否引入并行 kernel 请求处理或 group commit 等更复杂机制。

## 9. 分级预算与失败隔离

- **预算树**：swarm 运行有一个顶层预算（token/成本），coordinator 按图结构做**认领式**分配——节点执行前向 coordinator 申领一份预算额度（复用现有 `go/daemon/budget.go` 的 per-task 记账机制，coordinator 层加一层聚合视图，不改动底层记账逻辑本身）。任一节点超支只影响它自己降级，不需要感知全局；coordinator 定期汇总，接近总预算天花板时对新入队节点降低并发度或暂停接纳新节点，而不是硬杀已在跑的节点。
- **失败隔离（默认 `on_failure: isolate`）**：一个节点失败，只把它和**仅依赖它、无其它满足路径**的下游节点标记为 `skipped`，图的其余部分继续跑。`fail_fast` 仍可选，用于关键路径节点。这是对现有 `workflow.go:245`"一失败即全灭"的直接修正，理由很朴素：图越大，局部失败的概率越高，全灭策略在大图上不可用。
- 审计：`NodeSkippedDueToUpstreamFailure` 作为新事件类型，记录因果链（skip 是被谁的失败传导的），保证事后可追溯，不是静默丢弃（呼应本 session 一路坚持的"任何丢弃都要有审计记录"原则）。

## 10. 大规模可观测性：聚合优先，明细按需

1000 个节点不可能像现在这样把每个节点的完整事件流都推给一个订阅者（`go/daemon/bus.go` 的 256-buffer-then-disconnect 机制在这个量级下会持续抖动）。方案：

- 节点向 coordinator 上报**状态转移**（queued→running→completed/failed/skipped）和**周期性心跳摘要**，不是逐 token 转发完整 transcript。
- Coordinator 维护一份"图的聚合视图"（N 完成/M 运行/K 失败/剩余预算），这才是默认订阅内容——直接对应本 session 一直在用的 `/workflows` 进度展示模型（按 phase 分组，不是 firehose）。
- 明细下钻：客户端对某个具体节点 ID 发起单独订阅时，才拉取它的完整事件流——按需，不默认全量推送。
- 这与现有 Bus 机制不冲突：Bus 继续服务单会话内的正常事件流；swarm 聚合视图是 coordinator 内新建的一层，向下订阅各 worker 节点的 Bus（本机的、量级正常的），向上只吐聚合结果。

## 11. 治理与协议改动清单

新增能力类型（延续本 session 已确立的模式：新能力默认走 `RequiresApproval`，除非有明确理由默认 `Allowed` 并留给 policy bundle 收紧）：

| Capability | 默认 verdict | resource 携带 |
|---|---|---|
| `SwarmSpawn` | RequiresApproval | 图规模估算（节点数、预估总 token） |
| `SwarmMessage` | Allowed（审计但不阻塞——高频路径，类比 `ContextCompress` 的理由） | `channel:目标通道名` |
| `RemoteDispatch` | RequiresApproval | 目标 worker 的机器标识 + 节点 risk_ceiling |

新增 RPC（`protocol/jsonrpc/methods.json` 扩展，风格对齐现有 `workflow.*`/`work.*`）：
- `swarm.submit` / `swarm.status` / `swarm.cancel` / `swarm.graph.mutate`（generator 节点注入新节点走这个）
- `swarm.node.status`（单节点下钻）
- `swarm.channel.publish` / `swarm.channel.subscribe`

新增事件类型（进哈希链，`actor` 视触发方是 `go`/`model`）：
`SwarmStarted`, `SwarmNodeReady`, `SwarmNodeDispatched`, `SwarmNodeCompleted`, `SwarmNodeSkippedDueToUpstreamFailure`, `SwarmChannelMessagePublished`, `SwarmBudgetThresholdReached`, `SwarmCompleted`, `SwarmDegraded`。

新增 schema：`protocol/schemas/swarm-graph.schema.json`（节点/边/条件表达式的声明格式，字段设计见 §4；与 `workflow-orchestration.md` 提议的 `workflow.schema.json` 共享 state/channel/reducer 词汇，避免两套图语言并存）。

## 12. 分阶段落地顺序

以下阶段均已进入代码；“验收标志”列保留原始目标，实际覆盖和边界见文首 as-built 表：

| 阶段 | 内容 | 依赖 | 验收标志 |
|---|---|---|---|
| **P1（完成）** | 单 daemon 内新增流式 ready-queue + 有界 worker 池；保留 BSP 模式；streaming 使用独立的 1000 节点硬顶；`on_failure: isolate` 默认化 | 无（纯本地改造现有代码） | 单 daemon 内跑通 200+ 节点、异构耗时的图，无 barrier 空等 |
| **P2（完成）** | 结构化 `input`/`output`（data edge）+ 条件边（沙箱表达式）+ generator 动态节点注入，节点数/深度安全上限 | P1 | 图可以在运行中长出新节点，仍受硬顶约束 |
| **P3（本机完成）** | `SwarmMessage` channel 通信，复用 `go/channels` 信任模型 | P1 | 节点间可在运行中互相推送消息，不必等上游完成 |
| **P4（传输与执行链完成）** | Remote worker 真正实现（填 `go/worker` 的 Remote kind 空白）+ `RemoteDispatch` 能力 + coordinator/worker 分离 | P1-P3（图/通信语义先在单机跑稳，再谈跨机） | 一次 swarm 运行真实跨 2+ 台机器分摊执行 |
| **P5（完成，remote usage 有明确缺口）** | 预算树聚合视图 + 大规模可观测性聚合层 | P1-P4 | 1000 节点规模的 swarm 运行，coordinator 侧内存/事件量不随节点数线性爆炸给单一订阅者 |

P1 单独就有独立价值（现有 workflow 引擎的 barrier 问题不需要等集群能力就能修），可以先落地验证调度语义，再决定是否投入 P3/P4 这类工作量更大的部分。

## 13. 关键风险与待定问题

- **审计 durability/句柄开销与性能决策门禁——已收口（2026-07-12）**：`AuditLog` 长期持有文件句柄，每次 append 后 `sync_all()`；Unix 上每次打开还同步父目录，覆盖主机崩溃/断电时目录项丢失的窗口。write/fsync 失败会 poison 当前实例，禁止基于陈旧链头继续写；并发压力和故障注入测试覆盖链完整性与失败关闭。`scripts/bench-audit.sh` 以并发 fsync 写入、hash-chain 校验、EPS 和 p99 延迟形成 CI 决策门禁。当前基线满足门禁，因此不引入会改变顺序/生命周期语义的 group commit；只有后续真实生产数据触发门禁，才重新开启该设计。
- **Coordinator 单点——确认为设计决策，非待办（2026-07-12）**：不打算在 P1-P5 之外的某个后续阶段"升级"成多活。理由：①现有 event-log 可重放哲学已经覆盖了单点崩溃恢复（重启后从持久化状态继续），这是本设计从一开始就依赖的机制,不是权宜之计；②真正的多活/双活需要 leader 选举 + 跨节点状态复制,是一整块独立的分布式系统工程,不是"顺手做了"的规模；③核实过 Claude Code 自身的多 Agent 设计（其 Team/Coordinator 模式）也是单一 leader 进程，没有 leader 复制——单点 coordinator 不是 Carina 独有的权宜设计，是这类"任务图编排器"场景下的合理默认。**如果未来要做**，触发条件应该是"coordinator 进程本身的可用性成为实测出来的真实瓶颈"，而不是"理论上单点不够优雅"。
- **条件表达式引擎已经收口**：当前使用仓库内的小型 JSONLogic-compatible evaluator，并对未知操作符/坏类型失败关闭；不执行 JS/Python。
- **generator 动态节点的滥用风险——已收口（2026-07-12）**：新增 `Capability::SwarmSpawn`（RequiresApproval，仅当运行时图规模已超过硬顶一半时才会被请求——小图完全不触碰这个能力）+ 滑动窗口速率限制（`maxGeneratorNodesPerWindow`/`generatorInjectionWindow`，独立于已有的深度/总量硬顶，防止多个各自合法的 generator 级联出突发注入）。两者都失败关闭，被拒绝的注入不消耗速率预算。
- **权威 schema 已统一**：两种执行模式共享 `protocol/schemas/workflow-graph.schema.json` 与 `WorkflowSpec`；BSP 和 streaming 是同一产品的两种调度语义。
