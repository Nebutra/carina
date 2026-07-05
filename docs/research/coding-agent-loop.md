# 调研:Coding Agent Loop 机制

来源:ReAct 论文(arXiv:2210.03629);opencode/aider/SWE-agent 真实源码;Anthropic Building Effective Agents / Effective Context Engineering。

## ReAct 原理
Thought(推理)→ Action(动作)→ Observation(环境反馈)交错循环。**接地压过幻觉**:每步 Thought 被真实 Observation grounding,错误在下轮被环境纠正而非累积。ReAct 在 ALFWorld/WebShop 比基线 +34%/+10%。carina 现有 loop 正是这个骨架(model 出 JSON action,kernel 执行喂回 observation,thought 字段承载推理)。

## 通用 turn 结构
```
1. 拼 context = system + tools + (压缩后)history
2. (超预算) compact/elide history
3. 调 model → thought + tool_call(s)
4. 解析/校验 → 错则 requery(不执行、不计轮)
5. 授权 + 执行 → typed observation
6. observation 写回 history
7. 判断终止:done / 无 tool_call / 预算耗尽 / 降级 → 否则 continue
```

## 三家对比
| | opencode | aider | SWE-agent |
|--|--|--|--|
| 终止 | 模型自然停止(finish≠tool-calls) | 无 reflected_message 就 break,反射≤3 | action=exit/submit → done |
| observation | typed，单条≤2000 字符 | lint/test 输出作反射喂回 | bash stdout + state |
| context 管理 | 自动 compaction(摘要 head+保留 tail)+ prune 旧 tool 输出 | RepoMap(tree-sitter+PageRank)+ ChatSummary | LastNObservations 省略旧观察(留最后5) |
| 错误恢复 | retry.ts 指数退避 | 反射(错误当新任务) | requery(格式错)+ 致命错 autosubmit 降级 |

关键常量(opencode):COMPACTION_BUFFER=20k, PRUNE_PROTECT=40k, TOOL_OUTPUT_MAX_CHARS=2000, TAIL_TURNS=2;retry INITIAL=2s BACKOFF=2 MAX=30s,5xx 重试,ContextOverflow 不重试(改压缩)。SWE:max_requeries=3,ContextWindowExceeded/CostLimit → autosubmit。aider:max_reflections=3。

## 关键工程点
- **终止三叠加**:done 工具/模型自然停 + 预算兜底(steps/tokens/cost/time)+ 反射/requery 上限(防同一子问题打转)。
- **错误两层**:传输层(API 错误)指数退避+Retry-After,重试 5xx/限流,**不重试 context-overflow(改压缩)**;语义层(模型产出错)格式错 requery(不执行不计轮),致命错降级提交部分。
- **并行工具**:只读(read/search/list)并发,变更(patch/run)串行 → 保事务可回滚。carina 一轮一动作提速的最大杠杆。
- **上下文压缩(loop 跑长命脉)三级**:① 裁剪/省略(截断单条、擦旧 tool 输出、留最后 N 条,注意破坏 prompt 缓存 → polling)② 摘要 compaction(廉价模型摘要 head:保留决策/未解决 bug/patch ID,丢原始输出,留 tail)③ 持久笔记/子 agent(NOTES.md/todo,压缩后读回)。
- **防无限循环**:动作指纹去重 + 无进展检测(三家都不完善,carina 该补)。

## 哲学
接地>幻觉;小步可观测可验证(lint/test/exit-code/kernel 决策);有界自治+优雅降级(model=策略,kernel=安全调速器,observation=传感器);context 是有限资源(context rot,目标是"达成目标的最小高信号 token 集",压缩是正确性前提);做最简单能 work 的事。

## → carina 7 改进(读了 agent.go 现状:maxAgentTurns=14,strings.Builder 全量拼 history,Think 出错即死,无压缩/并行/循环检测)
**架构判断:喂给模型的 context 应是 event log 的一个「有界投影」,而非只增字符串。审计链永保全,压缩只作用于模型视图。**
1. **typed Transcript**(取代 strings.Builder)—— Observation{CallID,Tool,Content,Pinned,Elided...} + Turn{Actions[],Results[]} + Transcript{Summary,Turns,Notes}。prompt 由投影生成 → 才能结构化压缩/打缓存标记/并行对齐。(使能基础)
2. **★上下文压缩(最高优先,当前完全缺失)**:Think 前 token 预算检查。(a) 省略:留最近 KeepLastObs 条 verbatim,旧 run/read 输出替换 `[elided:N lines]`,Pinned(失败测试/当前文件/patch 结果)永不省;(b) 仍超 → 廉价模型摘要 head 成 Summary,留 tail。审计链不受影响。
3. **模型重试**:RetryPolicy(MaxAttempts=5,InitialDelay=2s,Backoff=2,尊重 Retry-After;5xx/rate_limit 重试,context_overflow 不重试→压缩)。用 context.Context 控超时,重试写审计。
4. **畸形动作 requery**:内层子循环 maxRequeries=3,解析/schema 校验失败 → 具体错误紧凑回喂、不执行不计正式 turn;3 次才算失败。给 action 加 schema 校验。
5. **终止+防循环**:LoopGuard(动作指纹 hash(tool|path|argv)→次数,MaxRepeat=3 注入 nudge 再超中止;turnsSinceEdit,MaxNoProgress=5 提示"要么编辑要么 done")。非 done 终止**优雅降级**(仿 autosubmit:部分结果摘要+已应用 patch ID,标记 degraded,而非简单 failed)。maxAgentTurns 改为 token/cost 预算+步数组合。
6. **并行工具**:一轮出 actions 数组,kernel 逐个授权,只读并发/patch 串行进事务/run 串行。需把文本解析升级为带 CallID 的结构化 tool_call。
7. **prompt 缓存友好**:稳定前缀 + 最近 N 轮打 cache_control + 省略用 polling 每 P 步改一次。

**优先级:2(压缩)>3(重试)>5(终止/防循环)>4(requery)>6(并行)>1/7(结构化/缓存,前几项使能基础,先落 typed transcript)。**
