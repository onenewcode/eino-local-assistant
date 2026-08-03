# 长对话中的意图变更、Agent 切换与上下文构建：行业实践

> 状态：行业调研笔记，不是实现方案或本仓库改造计划。
>
> 调研日期：2026-07-30。产品和 SDK 的行为会变化，采用前应复核引用版本。
>
> 范围：一个用户可见的长会话里，新用户消息如何改变既有任务；何时保持原 Agent、何时 handoff / spawn / fork；以及把什么上下文、以什么一致性边界交给下游 Agent。
>
> 非范围：通用的首次请求路由体系、自动生成任务清单、模型训练细节、或对任何本地产品代码的评审。本文只研究外部产品、公开 SDK 与开源实现。

## 1. 结论

- **不要把“意图识别”直接等同于“选 Agent”。** 长会话的新消息首先应被解释为对活动任务的结构化增量（继续、细化、扩展、替换、取消、控制），再在任务、能力、权限、工作区和风险约束下决定是否换 Agent。`topic -> agent` 的单步分类会把“纠正上一条”“取消正在跑的命令”和“新开一个话题”误判为同一类问题。
- **默认保持用户可见的主 Agent；把换人作为有语义的状态迁移。** 当任务目标、能力边界、权限与工作区都未变时，保持同一主 Agent 能保留工作记忆并减少交接损耗。需要不同专业能力、不同信任域或不同工具政策时才 handoff；独立的并行子任务通常应 spawn，主 Agent 不变；比较两条思路或保留旧工作线时才 fork。
- **下游 Agent 不应拿到“整段历史”或“一段万能摘要”，而应拿到按接收者构造的上下文包。** Canonical transcript、任务状态、证据/工件、跨会话记忆和运行态应分别保存；每次交接从它们构造有版本、有来源、有最小权限的 manifest。OpenAI Agents SDK 的 handoff 默认传递完整历史，但提供 `input_filter`、会话输入回调和历史映射；这说明“历史如何转交”是显式控制面，而不是框架自动能替应用做对的事。[事实，OpenAI Agents SDK][openai-handoffs]
- **压缩和记忆都不是事实账本。** 成熟产品将会话、压缩、分叉和跨会话记忆分开：Grok Build 持久化完整会话、TODO 状态、压缩检查点和子 Agent 元数据；其记忆在首轮和自动压缩后检索补回相关信息。[事实，Grok Build][grok-sessions] [事实，Grok Build][grok-memory] 可靠做法是保留追加式事件账本，把摘要标成覆盖范围与版本，再按当前任务检索。
- **“取消”不是“回滚”，也不是“立即没有旧结果”。** Claude Agent SDK 的 `interrupt()` 只能用于流式连接；Codex 的公开 TUI 代码还专门处理被中断 turn 的待提交 steer 消息，并有回归测试表明中断 turn 后后台终端进程仍可存在。[事实，Claude Agent SDK][claude-client] [事实，Codex][codex-interrupt] 因此替换/取消任务需要取消令牌、租约、版本栅栏、幂等外部操作和迟到结果隔离。
- **公开资料没有披露主流产品的完整“意图变化分类器”。** 下文把产品可验证的会话、handoff、fork、压缩和中断机制与综合推论分开；推论是从这些机制归纳出的控制面，不应被表述为某厂商内部实现。

## 2. 问题边界：五个状态不能混成一个“上下文”

长会话里至少有五类状态。把它们混成一个 prompt，会造成错误交接、摘要污染和取消竞态。

| 状态层 | 它回答的问题 | 推荐的权威载体 | 不应被误当成 |
| --- | --- | --- | --- |
| 对话账本 | 发生过什么、顺序是什么、谁说的 | 追加式 turn / tool / approval 事件 | 当前任务的唯一摘要 |
| 任务状态 | 正在完成哪个目标，何种验收条件、步骤和依赖仍有效 | 带版本的 task / plan / artifact 状态 | 全部聊天记录 |
| Agent 与执行状态 | 谁对用户负责，哪些子任务、工具调用、审批正在运行 | owner、run、lease、cancel token、权限快照 | 用户意图本身 |
| 长期记忆 | 哪些跨会话事实可能相关 | 有作用域、来源和检索分数的 memory store | 未经验证的系统指令 |
| 模型输入包 | 这一次某个接收 Agent 需要看到什么 | 可审计的 context manifest | 持久化的真实世界状态 |

### 2.1 一个可审计的控制流（综合推论）

```text
新用户事件
  -> 写入不可变账本（event_seq）
  -> 识别显式控制/显式目标 Agent
  -> 计算 IntentDelta（相对活动任务，而非孤立主题标签）
  -> 原子更新 TaskState / owner / run lease / cancel epoch
  -> 选择 keep | handoff | spawn | fork | fresh-thread
  -> 以 recipient 为中心构造 context manifest
  -> 运行 Agent 与工具；所有可变提交经过 version fence
  -> 结果以来源和 event_seq 回写；迟到结果只入审计，不自动污染新任务
```

这个顺序有两个刻意的分离：

1. **任务关系先于 Agent 路由。** “把上一条的数据库改为 SQLite”通常是细化；“顺便写迁移文档”通常是扩展；“算了，改为审查安全问题”是替换。它们即使都由同一个 Agent 处理，状态变化也不同。
2. **状态迁移先于上下文组装。** 如果先复制全文给下游 Agent、再决定取消旧任务，新 Agent 可能执行已经被撤销的目标或继承过期权限。

### 2.2 术语：handoff、spawn 和 fork 不是同义词

| 动作 | 用户可见的负责人 | 任务线 | 上下文语义 | 适合的意图变化 |
| --- | --- | --- | --- | --- |
| 保持（keep） | 不变 | 同一条 | 只增量补入新消息和最新状态 | 继续、低风险细化 |
| Handoff | 切换到目标 Agent | 同一活动任务或其明确继任者 | 发送受控交接包 | 专业能力、权限或责任主体改变 |
| Spawn | 父 Agent 不变 | 子任务 | 子 Agent 独立运行，回传合同化结果 | 可分解、可并行、结果由父 Agent 汇总 |
| Fork | 分出同级会话/分支 | 两条并存任务线 | 以某个历史与工作区快照为起点 | 比较方案、保留替代探索、回退后重试 |
| Fresh thread | 新负责人或默认负责人 | 新任务根 | 默认不继承旧工作上下文，只按需取证 | 无关主题、租户/工作区/保密边界变化 |

OpenAI Agents SDK 明确区分：`Agent.as_tool()` 让 manager 保持对用户会话的控制，handoff 则使专门 Agent 成为当前 turn 的活动 Agent。[事实，OpenAI Agents SDK][openai-multi-agent] Grok Build 将子 Agent 表述为独立 child session，拥有自己的上下文窗口，结束后向父 Agent 返回摘要；`resume_from` 则让已完成子 Agent 续接自己的 transcript、工具状态与模型。[事实，Grok Build][grok-subagents] Claude Agent SDK 公开了 `fork_session`：恢复的会话可 fork 到新的 session ID，而非继续原会话。[事实，Claude Agent SDK][claude-types]

## 3. 可验证的行业机制

本节只陈述公开文档或公开源码可核验的事实；并不声称这些产品以同一套内部分类器做决策。

### 3.1 Codex：会话分叉、压缩、Agent thread 与中断 steer

- Codex 的公开 TUI slash command 定义中，`/compact` 被描述为“总结会话以避免触及上下文限制”，`/fork` 为“fork 当前聊天”，`/side` / `/btw` 为“在临时 fork 中开始侧边对话”，`/agent` / `/multi-agents` 为“切换活动 Agent thread”。[事实，Codex][codex-slash]
- 同一公开代码将 turn 终态分为 completed、interrupted、failed。中断时，UI 会处理待提交 steer：注释明确指出服务端到达中断状态时已丢弃 pending input，UI 必须恢复未确认的 steer；这表明“用户中途改口”需要在协议层有独立于最终回答的输入队列语义。[事实，Codex][codex-input-restore]
- 公开回归测试名为 `interrupt_preserves_unified_exec_processes`，测试中 turn 已中断但统一 exec 的后台进程仍被 `/ps` 列出。它是代码层观察，不应扩大成所有 Codex 工具的一般产品承诺；但足以说明 UI/模型 turn 的取消不能假定外部工作已经回滚。[事实，Codex][codex-interrupt]

**未能从这些公开材料证明的内容：** Codex 是否用一个固定的 intent-delta taxonomy 自动决定“保持、handoff 或 fork”，以及其服务端如何对冲突 turn 做最终提交。本文不会据此臆测。

### 3.2 OpenAI Agents SDK：把历史传递设计成可编排表面

- handoff 在 SDK 中表现为给模型的工具；接收 Agent 默认看到此前完整会话历史。`input_filter` 可重写交接输入，`HandoffInputData` 将既有历史、本 turn 的 pre-handoff 项、新生成项和 run context 分开暴露。[事实，OpenAI Agents SDK][openai-handoffs]
- SDK 的 nested handoff history（beta、默认关闭）会将可摘要历史压缩为有序 assistant summary segment，同时保留无损消息项的原位置；也可用 `handoff_history_mapper` 自定义交接历史。该机制说明“压缩”和“避免重复追加同一消息”需要由运行时显式管理。[事实，OpenAI Agents SDK][openai-handoffs]
- Session 会在每次 run 前取回历史、在 run 后持久化新项；`session_input_callback` 可以改变“历史 + 新输入”的合并，`SessionSettings(limit=N)` 可限制取回的最近条数。SDK 同时警告 session persistence 不应和 `conversation_id` / `previous_response_id` 混用，否则可能重复上下文。[事实，OpenAI Agents SDK][openai-sessions] [事实，OpenAI Agents SDK][openai-running]
- SDK 明确区分 **本地应用 context**（给工具、hook、handoff 回调，默认不发给模型）与 **模型可见 context**（instructions、消息、工具/检索结果）。这与“把所有运行态塞进聊天历史”的做法相反。[事实，OpenAI Agents SDK][openai-context]
- 其 auto-compaction 在候选阈值达到后可自动改写 session history；文档提示压缩会阻塞流式 run 的完成，低延迟场景可在空闲期手动压缩。[事实，OpenAI Agents SDK][openai-sessions]

### 3.3 Claude Agent SDK：可中断的流、可 fork 的恢复与可定义子 Agent

- `ClaudeSDKClient` 提供多轮 `query(..., session_id=...)`，并提供 `interrupt()`；公开示例强调 interrupt 需要活跃地消费消息，处理完中断后才发送新指令。[事实，Claude Agent SDK][claude-streaming] [事实，Claude Agent SDK][claude-client]
- `ClaudeAgentOptions` 公开 `resume`、`continue_conversation`、`session_id` 与 `fork_session`；文档字符串说明 `fork_session=true` 时，恢复会话会产生新的 session ID 而不是继续原 session。SDK 还允许定义由 Agent tool 调用的自定义 subagents。[事实，Claude Agent SDK][claude-types]
- SDK 支持将 transcript 镜像到外部 session store，并在恢复时 materialize；类型中还明确有子 Agent transcript 的 subpath。这证明会话主线与子 Agent 记录是可分离的持久化对象。[事实，Claude Agent SDK][claude-types]

### 3.4 Grok Build：完整会话、自动压缩、跨会话记忆和子 Agent 隔离

- Grok Build 文档称 session 持久化完整对话、工具调用、TODO/task-list state、文件快照、token/turn 计数与子 Agent session；存储目录还区分原始 chat history、`plan.json`、compaction checkpoints 与 subagent metadata。[事实，Grok Build][grok-sessions]
- `/fork` 将当前会话分支为从对话副本开始的 peer agent，可选择独立 git worktree；`/compact` 可接收“保留什么”的补充上下文，并在接近 context-window 上限时自动触发。[事实，Grok Build][grok-sessions]
- 子 Agent 有独立 context window 和工具集，父 Agent 接收其完成摘要；可以通过 capability mode、MCP inheritance 与 worktree isolation 缩小其能力和可见资源。`resume_from` 续接的是原 child 的 transcript、工具状态和模型，而不是把完整父会话重新复制进去。[事实，Grok Build][grok-subagents]
- Grok Build 的跨会话 memory 具有 global/workspace/session 作用域、全文与向量混合搜索；首轮会检索并注入相关 memory，自动压缩后也会再次检索。其 `/flush` 用于在压缩前保留更丰富的会话结论。[事实，Grok Build][grok-memory]

### 3.5 Aider：递归摘要保留最近尾部，而非假装全文永远可用

开源 Aider 的 `ChatSummary` 会按 token 计算历史；超过阈值时，把较早 head 交给摘要模型，同时保留较新的 tail；若摘要加 tail 仍过大则递归。代码还为模型输入预留 buffer。[事实，Aider][aider-history] 这是一个清晰的工程取舍：**最近原文通常比远处原文更值得保真**。它不解决任务替换、权限隔离或迟到工具结果，因此不能单独充当多 Agent 上下文路由方案。

## 4. 把新消息判定为“任务增量”，而不是孤立意图标签

### 4.1 推荐的结构化输出（综合推论）

对每个用户 turn 产生一个小而可审计的 `IntentDelta`，而不是让模型直接输出某个 Agent 名称：

```json
{
  "kind": "continue | refine | extend | replace | cancel | control | ambiguous",
  "target_task_id": "task_42 | null",
  "explicit_agent_target": "security-reviewer | null",
  "relation": "same_goal | correction | dependent_addition | independent_addition | new_goal | stop",
  "changed_constraints": ["must_not_modify_api", "deadline=tomorrow"],
  "invalidates": ["decision:db=postgres", "step:3"],
  "scope": {"workspace": "...", "tenant": "...", "sensitivity": "..."},
  "confidence": 0.0,
  "needs_confirmation": false,
  "evidence_event_ids": ["e101", "e118"]
}
```

这里的 `confidence` 只影响是否澄清，**不能越过显式用户控制和安全策略**。先由确定性规则识别 `/cancel`、`/fork`、`@agent`、租户/工作区切换和审批操作，再让模型在受限 schema 内判断任务关系，最后由状态机校验目标任务确实存在、用户是否有权指定目标 Agent、以及该 Agent 是否可用。

### 4.2 六类增量与默认状态迁移（综合推论）

| 增量 | 典型含义 | 任务状态变化 | 默认 Agent 决策 | 下游最小上下文 |
| --- | --- | --- | --- | --- |
| `continue` | “继续”“把测试跑完” | 保持目标与验收条件；追加工作 | 保持 owner；必要时把新消息作为 steer | 活动任务、最近未完成步骤、最近原文与运行结果 |
| `refine` | “不是 A，是 B”“不要改公开 API” | 同一 task version 上修正约束；标记受影响结论/步骤失效 | 通常保持 owner；能力/权限改变才 handoff | 更正本身、被否定的决策、受影响工件、更新后验收条件 |
| `extend` | “顺便补文档/迁移/测试” | 创建依赖子任务或扩大 task graph | 主 owner 保持；独立部分可 spawn | 父任务摘要、依赖产物、子任务合同；不必全文 |
| `replace` | “先别做这个，改查性能问题” | 冻结或终止旧任务，建立新任务根或明确继任关系 | 能力相同可保持 owner；否则 handoff / fresh thread | 新目标为主；旧任务只给关联、可复用证据和未完成副作用状态 |
| `cancel` | “停”“不要继续改文件” | 取消指定 task/run/child；撤销未来提交授权 | 无需换人；先处理运行态 | task/run ID、已启动操作、实际副作用状态、取消范围 |
| `control` | “切到审查 Agent”“从第 3 步继续”“开分支” | 更改 owner、会话分支或展示状态，但不必改目标 | 服从显式目标和策略 | 控制参数与必要快照；不是完整任务 prompt |

`ambiguous` 应优先保留当前状态并提出一个针对性澄清，**但只在错误的代价高时**。例如“改一下”可先请求所指对象；“删除旧数据，改为生产环境”则在任何可变工具调用前要求范围确认。

### 4.3 显式指定 Agent 的优先级（综合推论）

用户说“交给 `security-reviewer`”时，这不是模型可以随意忽略的主题信号，而是一个控制请求。合理顺序是：

1. 解析目标标识，检查其存在、是否被用户/租户允许、当前会话是否允许切换，以及该 Agent 的能力/权限是否能覆盖请求。
2. 若可用，记录 `explicit_agent_target`、handoff reason 与新的 owner epoch；如果用户只要求子任务由该 Agent 做，则 spawn 而非改变用户可见 owner。
3. 若不可用，不要静默换成相似 Agent 后声称已经路由；应明确说明拒绝/降级原因，并要求用户选择或使用安全默认。
4. 仍对目标 Agent 应用信息最小化：用户选择某 Agent 不等于允许它看到其他 task、其他租户、密钥或完整内部思维链。

这和 OpenAI SDK 的机制相符：每个 handoff 指向一个明确目标 Agent，`input_type` 只能传递结构化交接元数据，不能动态更换目的地；有多个候选时需要注册多个 handoff 或自己实现选择逻辑。[事实，OpenAI Agents SDK][openai-handoffs]

## 5. 何时不换、何时 handoff / spawn / fork

### 5.1 决策表（综合推论）

| 观察到的变化 | 保持同一 owner 的条件 | 应切换/分支的条件 | 首选动作 |
| --- | --- | --- | --- |
| 继续现有任务 | 目标、工具政策、工作区和验收条件一致 | 无 | `keep` |
| 纠正或补充约束 | 当前 Agent 能理解被修正的决策，且没有安全边界变化 | 新约束要求不同权限/受监管专业角色 | `keep`，必要时 `handoff` |
| 扩展为可并行子任务 | 主任务需要统一决策、子结果不应直接面对用户 | 子任务输入/输出可合同化、可独立验证 | `spawn`，主 owner 保持 |
| 目标替换 | 新旧任务使用同一能力和资源，保留 owner 能减少交接成本 | 目标域、工具权限、责任主体、语言/合规域改变 | `keep` + 新 task，或 `handoff` |
| 比较替代方案/保留旧线 | 用户不需要旧线继续运行 | 两条路线都值得保留、需要独立工作区或可回到某快照 | `fork` |
| 取消 | 无需换 owner；先停止新提交 | 可将纯只读、可重用研究保留为“候选证据”，但不得自动并回 | `cancel` |
| 租户、工作区、保密域切换 | 很少成立 | 旧上下文与权限不应跨域传播 | `fresh-thread` 或受限 `handoff` |

**经验性默认：** handoff 有上下文重建和“谁对用户负责”的成本；spawn 有并发、冲突与汇总成本；fork 有状态复制和工作区分叉成本。因此它们不该因为新消息包含了一个新关键词而触发。Grok Build 把 `worktree` isolation 设计为子 Agent 编辑时的明确选择，正是对“同一工作区并行修改”风险的产品化承认。[事实，Grok Build][grok-subagents]

### 5.2 让任务 owner 与执行 worker 分开（综合推论）

一个稳定的模式是：

- **owner** 负责用户可见的目标解释、优先级、最终答复与交接决定；
- **worker/subagent** 接受有限输入合同，执行研究、审查、测试或实现，回传带来源的结果；
- **router** 可以是确定性代码、轻量分类模型或 owner 的受限工具，但它只提出/执行状态迁移，不能绕过权限和版本栅栏。

这避免了“每一个 specialist 都成为新主对话”的抖动。OpenAI 将此区分为 manager 调用 agent-as-tool 与 handoff；Grok 将子 Agent 结果回传父 Agent；两者都支持“专家参与但不一定接管用户会话”的模式。[事实，OpenAI Agents SDK][openai-multi-agent] [事实，Grok Build][grok-subagents]

## 6. 下游 Agent 的上下文：从账本生成可审计的 manifest

### 6.1 四层数据模型（综合推论）

1. **不可变账本（source of truth）**：用户消息、assistant 输出、tool request/result、审批、分叉、取消、handoff、压缩事件。每项有 `event_id`、顺序、父因果关系、来源与敏感度。
2. **可变任务投影**：`task_id`、目标、验收条件、计划、状态、依赖、owner、任务版本。它可从账本重建，但为了效率可物化。
3. **工件与证据库**：文件快照/commit、命令输出、检索结果、测试报告、决策记录。大对象只传引用、内容 hash 和按需读取能力。
4. **长时记忆**：按用户/工作区/项目/会话作用域检索的事实。它是候选上下文，不能覆盖当前用户的明确约束或账本中的新事实。

模型输入只是一份从这四层挑选出来的 **manifest**。OpenAI SDK 的“local context 不自动给 LLM”和 Grok Build 的“session、任务状态、memory 分目录保存”都支持这种分层，而不是将所有状态串成一条历史文本。[事实，OpenAI Agents SDK][openai-context] [事实，Grok Build][grok-sessions]

### 6.2 推荐的 context manifest（综合推论）

```json
{
  "manifest_version": 1,
  "recipient": {
    "agent_id": "reviewer",
    "role_contract_id": "review-v3",
    "tool_policy_id": "read-only-v5",
    "clearance": "project-a"
  },
  "causal_fence": {
    "thread_id": "th_17",
    "upto_event_seq": 281,
    "task_id": "task_42",
    "task_version": 9,
    "owner_epoch": 4,
    "run_id": "run_88",
    "cancel_epoch": 12,
    "workspace_revision": "git:abc123+dirty:hash",
    "policy_version": "policy-5"
  },
  "request": {
    "user_event_id": "evt_281",
    "raw_message": "不要继续实现，改为只做安全审查",
    "intent_delta": "replace",
    "acceptance_criteria": ["列出可复现问题", "不修改文件"]
  },
  "task_snapshot": {
    "active_goal": "...",
    "valid_decisions": ["..."],
    "invalidated_decisions": ["..."],
    "pending_steps": ["..."],
    "side_effect_status": ["migration job started: unknown completion"]
  },
  "evidence": [
    {"ref": "artifact://test/77", "hash": "...", "trust": "tool-output", "relevance": 0.94},
    {"ref": "event://evt_267", "kind": "user-correction", "trust": "user"}
  ],
  "history": {
    "recent_raw_event_ids": [270, 275, 281],
    "summary_refs": [{"covers": [1, 240], "hash": "...", "task_version": 8}]
  },
  "memory_candidates": [{"ref": "memory://project/14", "scope": "project", "score": 0.81}],
  "handoff_contract": {
    "expected_output": "findings-with-evidence",
    "must_not_do": ["edit", "spawn-unapproved-agent"],
    "return_to": "owner-agent"
  }
}
```

注意两个边界：

- **接收 Agent 的 system/developer instructions 是可信静态配置，不应从用户历史“继承”。** 历史、检索页、网页、工具输出都应带角色和不可信来源边界，不能让其中的“忽略此前指令”变成新的指令。
- **manifest 中的权限快照不是授权本身。** 真正执行工具时仍应由当前服务端策略、用户审批和 version fence 再检查；否则一个旧交接包可以被重放以获得过期权限。

### 6.3 按增量选择上下文，而不是总把完整历史送出去（综合推论）

| 变化 | 必须带入 | 应谨慎带入 | 通常不应带入 |
| --- | --- | --- | --- |
| 继续 | 最近原文、活动步骤、未消费的工具结果、当前工作区版本 | 相关早期决策摘要 | 无关历史、已完成子 Agent 全文 |
| 细化 | 新的约束/纠正、被否定决策、受影响工件 | 原决策的最小证据链 | 已被新约束推翻的旧计划当作现行指令 |
| 扩展并 spawn | 父任务合同、明确的子任务输入、依赖工件、输出格式 | 局部历史摘要 | 父 Agent 的无关推理和其他子任务私有数据 |
| 替换并 handoff | 新目标、与旧任务的关系、仍在运行的副作用、可复用证据 | 旧任务摘要（标记为历史） | 整个旧 prompt、已撤销权限、旧 Agent 的私有 scratchpad |
| 取消 | task/run 标识、外部操作状态、取消原因 | 可能需要用户确认的未完成副作用 | 将旧 task 继续作为模型目标的正文 |
| fork | 明确的 `at_event_seq`、会话快照、工作区 revision、分支 directive | 与分叉点相关的摘要 | 分叉点之后的消息或工件，除非用户显式选择 |

OpenAI 的 `input_filter`、session merge callback 与 handoff history mapper 正是可实现这种“按收件人筛选”的接口；默认完整历史只能作为简单场景的保守起点，不应被当作隔离策略。[事实，OpenAI Agents SDK][openai-handoffs] [事实，OpenAI Agents SDK][openai-sessions]

### 6.4 一个有成本约束的构造顺序（综合推论）

在 token 预算内，优先级通常应是：

1. 接收 Agent 的可信角色合同和硬安全约束；
2. 最新用户原文与已经确认的 `IntentDelta`；
3. 当前 task snapshot、验收条件、已取消/已失效的信息；
4. 能直接改变正确性的少量原始证据（最新 diff、失败日志、审批结果）；
5. 覆盖较早事件、且带范围/版本的摘要；
6. 经权限过滤、与当前任务检索命中的长期记忆；
7. 可按需读取的工件引用，而不是大段文本。

当预算不足时，先降级第 5/6/7 项，**不要删除新用户的否定词、当前权限限制、未完成副作用或任务版本**。Aider 的“摘要旧 head、保留新 tail”是这一优先级的一个开源实现实例；OpenAI 的有序 summary segment 则进一步避免把所有历史抹成一个不可追溯段落。[事实，Aider][aider-history] [事实，OpenAI Agents SDK][openai-handoffs]

## 7. 压缩、记忆与版本：三者必须可追溯

### 7.1 摘要的最低元数据（综合推论）

每份摘要至少应保存：

```text
summary_id, source_event_range, source_digest, generated_at,
task_ids/task_versions, workspace_revision, preserved_facts,
invalidated_facts, unresolved_questions, sensitivity, generator_version
```

这样在“用户说不对，改成 B”后，可以标记涉及 A 的摘要条目为失效，仍保留原始事件供审计，而不是覆写整段会话。Grok Build 的 compaction checkpoint 与 `/flush` 前保存丰富会话知识，说明产品也将压缩与长期保留区分开。[事实，Grok Build][grok-sessions] [事实，Grok Build][grok-memory]

### 7.2 记忆的作用域和取回条件（综合推论）

- 记忆应携带 `scope`（用户、租户、项目、工作区、会话）、来源、创建时任务版本、敏感度、可见 Agent 集和过期/复核策略。
- 检索必须经接收 Agent 的权限过滤，然后按新任务相关性排序；不能因为“以前保存过”而把旧偏好、旧 API 约束或其他项目材料自动注入。
- memory 命中应在 prompt 中标为“候选历史事实，必要时核验”，并链接到可追溯来源。它不应覆盖当前用户刚刚给出的替换/取消命令。
- 压缩后可重新检索 memory，但检索结果也必须标记来源和版本。Grok Build 的首轮注入与压缩后重新搜索是产品层的先例，不等于应无条件注入所有 memory。[事实，Grok Build][grok-memory]

### 7.3 不要混用两套会话连续性（事实与推论）

OpenAI Agents SDK 说明 client-managed session 与 server-managed `conversation_id` / `previous_response_id` 不应在同一次 run 混用，以免历史重复。[事实，OpenAI Agents SDK][openai-running] **综合推论：** 无论选哪种供应商连续性机制，应用仍应保留自己的任务版本、工件索引、取消 epoch 与 manifest 账本；供应商的对话链 ID 不能代替业务一致性 ID。

## 8. 版本栅栏与取消竞态

### 8.1 最小状态字段（综合推论）

```text
TaskState {
  task_id, parent_task_id, status,
  task_version, owner_agent_id, owner_epoch,
  accepted_goal, acceptance_criteria, workspace_revision,
  active_run_id, run_lease_id, cancel_epoch,
  policy_version, last_event_seq
}
```

- `task_version`：任务目标、约束、计划或依赖变化时递增；
- `owner_epoch`：用户可见 owner handoff 时递增；
- `cancel_epoch`：取消、替换或安全撤权时递增；
- `workspace_revision`：交接/执行所依据的文件或工件快照；
- `last_event_seq`：本次投影涵盖的账本位置。

这些字段不是为了让模型“理解更多”，而是为了让运行时拒绝把旧 worker 的输出和操作提交到新任务。

### 8.2 替换/取消的推荐时序（综合推论）

```text
1. 原子追加 user event e=281；保存原文和到达顺序。
2. 解析显式控制，得出 replace/cancel；比较当前 TaskState.version = 9。
3. CAS 更新为 version = 10、cancel_epoch = 12、status = cancelling；
   旧 run / child 的 lease 因 epoch 不匹配而失效。
4. 向可取消 worker 发送 cooperative cancellation；停止未开始的队列项。
5. 新 owner 或新 task 只拿 version=10 的 manifest 开始工作。
6. 任一旧 worker 回传时，服务端检查 lease、task_version、cancel_epoch、
   workspace_revision 和 operation_id；不匹配则记录为 stale，不合入新 task。
7. 已经开始的外部操作单独查询实际结果；只有确认终态后才把任务标为 cancelled。
```

“协作式取消”需要明确确认边界。Claude 的公开示例要求先处理 interrupt，再发新 query；Codex 的公开 UI 还要恢复未确认 steer。这些事实意味着客户端不能只按“调用过取消 API”就假定旧 turn 已完全静止。[事实，Claude Agent SDK][claude-streaming] [事实，Codex][codex-input-restore]

### 8.3 任何可变操作都需要提交时检查（综合推论）

对编辑文件、发送消息、创建云资源、数据库写入等操作：

1. **prepare** 阶段记录 `operation_id`、目标、幂等键、预期 `task_version/cancel_epoch/workspace_revision`；
2. 工具真正执行前再次验证当前授权、租约和策略；
3. **commit/merge** 阶段用 compare-and-set 检查同一组预期值；
4. 超时或网络失败时，把结果标为 `unknown`，以 `operation_id` 查询或幂等重试，不能盲目重做；
5. 已发出的不可逆操作不能被“取消”伪装成未发生，应返回实际状态和补救路径。

审批也应绑定到待执行操作的 digest、任务版本和权限版本；在审批弹窗后用户说“停止”或任务已 handoff，旧审批必须自动失效。Grok Build 的 worktree isolation 与 capability mode 提供了把并行修改风险和工具能力分开的产品机制；版本栅栏是在此基础上处理异步提交的综合推论。[事实，Grok Build][grok-subagents]

### 8.4 压缩和分叉也有竞态（综合推论）

- 摘要任务应记录它读取的 `[start_seq, end_seq]` 和 `base_task_version`；写回时仅在覆盖范围仍有效时合并，否则作为候选摘要重新计算。
- fork 应引用精确的 `at_event_seq` 和 `workspace_revision`。若只复制“当前摘要”，分叉可能包含已撤销的上下文；若共享工作区，还会产生并发写冲突。
- 子 Agent 结果回收必须携带输入 manifest ID 和输入 fence；父 Agent 只在它仍对应活动任务时采纳。否则保留为审计/可检索证据，不能悄悄改变新任务结论。

## 9. 常见陷阱

| 反模式 | 为什么失败 | 更稳妥的替代 |
| --- | --- | --- |
| 让一个 topic classifier 直接返回 Agent 名 | 忽略“相对活动任务”的纠正、取消和任务替换 | 先产出 `IntentDelta`，再由策略选择动作 |
| 每次换 Agent 都传完整 transcript | token 膨胀、提示注入传播、泄露无关信息、旧指令压过新任务 | 账本 + 任务投影 + 收件人 manifest |
| 用一个摘要覆盖原历史 | 无法知道哪条约束被用户撤销，也不能审计 | 保留账本，摘要带 source range/hash/version |
| 用 handoff 处理所有新增工作 | 用户可见 owner 抖动，责任和最终汇总消失 | 独立子任务用 spawn，主 owner 保持 |
| 把 fork 当作常规 handoff | 分叉复制陈旧上下文和工作区状态，合并复杂 | 只在需要并存路线/回退点时 fork |
| 用户指定 Agent 时静默降级 | 用户失去控制，审计也无法解释实际责任主体 | 显式验证、记录目标/拒绝原因、让用户决定降级 |
| 把 cancel 当作回滚 | 后台进程或外部写入可能已经发生 | cooperative cancel + 实际状态探测 + 幂等/补救 |
| 旧 worker 的晚到结果自动合并 | 新任务可能已替换目标或撤销权限 | lease / epoch / revision 检查，不匹配只归档 |
| 跨 Agent 复制 local context、密钥或所有 MCP | 违反最小权限；OpenAI 的 local context 本就不自动暴露给模型 | 让运行时按接收者政策重新授予工具与证据 |
| 将计划模式、任务计划、会话压缩混为一层 | 不能判断哪些是权限门、哪些是任务状态、哪些是文本优化 | 分别建模权限、task state、ledger 与 manifest |

## 10. 可观测性与评估

以下是综合推论，但它们使上述机制可以被验证，而非只依赖模型自述：

- **每次转移记录：** `event_seq`、`IntentDelta`、候选任务、最终动作、显式用户目标、拒绝/澄清原因、owner 前后值、task/cancel epoch。
- **每次交接记录：** recipient、handoff/spawn/fork reason、manifest ID、来源 event range、摘要 ID、memory 检索项、token 分配、敏感信息剔除计数。
- **每次执行记录：** run lease、工具 `operation_id`、预期与实际版本、取消请求/确认、迟到结果是否被拒绝、实际外部副作用状态。
- **离线回放集：** 至少覆盖“纠正前一条结论”“扩展但不换主 Agent”“显式指定 specialist”“替换时旧工具仍在跑”“取消后晚到子 Agent 结果”“压缩后恢复关键约束”“fork 后两个 worktree 并行”。正确性应按最终任务和安全边界评分，不能只按分类标签准确率。
- **在线信号：** 用户立即改口/重复说明率、handoff 后回切率、澄清后的任务完成率、stale-commit 拒绝数、上下文超预算率、摘要引用缺失率、跨域信息剔除率。OpenAI SDK 的 tracing 将 handoff 等事件单独建 span，说明 agent 交接本身应是可观测的一等事件。[事实，OpenAI Agents SDK][openai-tracing]

## 11. 仍待复核的问题

- Codex、Claude Code 等产品的公开材料展示了中断、分叉、会话和子 Agent 接口，但没有完整披露其生产环境里的 intent-delta 分类、冲突解决与迟到结果合并策略；不要把 UI/SDK 可见行为外推为内部算法细节。
- 各模型供应商对“取消”的语义不同：取消 HTTP/流式响应、停止模型推理、取消排队工具、终止本地进程、撤回已提交外部副作用是五个不同层次，必须逐一核验 API 合同。
- 记忆检索的相关性分数不足以表达真伪、时效和权限；跨租户、跨仓库或跨 Agent 的记忆继承需要额外的数据治理规则。
- 长任务中的共享工作区 revision 如何定义，取决于工具（git、远程编辑器、数据库等）；简单使用 Git HEAD 不足以覆盖未提交修改和外部系统状态。
- 什么时候以用户体验为由自动 fork，什么时候必须明确询问，是产品策略而非纯技术问题；高成本/不可逆变化应保留用户确认点。

## 参考资料

所有链接于 2026-07-30 访问；GitHub 链接固定到调研时检查的 commit，减少 main 分支漂移。

- <a id="openai-multi-agent"></a>OpenAI, *Agents SDK: Multi-agent orchestration*，官方文档，访问 2026-07-30。[`docs/multi_agent.md`](https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/multi_agent.md)
- <a id="openai-handoffs"></a>OpenAI, *Agents SDK: Handoffs*，官方文档，访问 2026-07-30。[`docs/handoffs.md`](https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/handoffs.md)
- <a id="openai-sessions"></a>OpenAI, *Agents SDK: Sessions*，官方文档，访问 2026-07-30。[`docs/sessions/index.md`](https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/sessions/index.md)
- <a id="openai-context"></a>OpenAI, *Agents SDK: Context management*，官方文档，访问 2026-07-30。[`docs/context.md`](https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/context.md)
- <a id="openai-running"></a>OpenAI, *Agents SDK: Running agents*，官方文档，访问 2026-07-30。[`docs/running_agents.md`](https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/running_agents.md)
- <a id="openai-tracing"></a>OpenAI, *Agents SDK: Tracing*，官方文档，访问 2026-07-30。[`docs/tracing.md`](https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/tracing.md)
- <a id="codex-slash"></a>OpenAI, Codex 开源 TUI 的 slash command 定义，commit `6219b7c`，访问 2026-07-30。[`slash_command.rs`](https://github.com/openai/codex/blob/6219b7c40fc9c702c0aef9964e72b492558f60e4/codex-rs/tui/src/slash_command.rs)
- <a id="codex-input-restore"></a>OpenAI, Codex 开源 TUI 的 interrupted-turn 与 steer 恢复逻辑，commit `6219b7c`，访问 2026-07-30。[`input_restore.rs`](https://github.com/openai/codex/blob/6219b7c40fc9c702c0aef9964e72b492558f60e4/codex-rs/tui/src/chatwidget/input_restore.rs)
- <a id="codex-interrupt"></a>OpenAI, Codex 开源 TUI 的中断后统一 exec 进程回归测试，commit `6219b7c`，访问 2026-07-30。[`exec_flow.rs`](https://github.com/openai/codex/blob/6219b7c40fc9c702c0aef9964e72b492558f60e4/codex-rs/tui/src/chatwidget/tests/exec_flow.rs)
- <a id="claude-client"></a>Anthropic, Claude Agent SDK Python 的多轮 query 与 `interrupt()` 客户端实现，commit `f8b9ec9`，访问 2026-07-30。[`client.py`](https://github.com/anthropics/claude-agent-sdk-python/blob/f8b9ec923982082a02c485924e0f60367949c3a1/src/claude_agent_sdk/client.py)
- <a id="claude-streaming"></a>Anthropic, Claude Agent SDK Python 的 interrupt 示例，commit `f8b9ec9`，访问 2026-07-30。[`examples/streaming_mode.py`](https://github.com/anthropics/claude-agent-sdk-python/blob/f8b9ec923982082a02c485924e0f60367949c3a1/examples/streaming_mode.py)
- <a id="claude-types"></a>Anthropic, Claude Agent SDK Python 的 session、fork、agent 与 store 类型，commit `f8b9ec9`，访问 2026-07-30。[`types.py`](https://github.com/anthropics/claude-agent-sdk-python/blob/f8b9ec923982082a02c485924e0f60367949c3a1/src/claude_agent_sdk/types.py)
- <a id="grok-sessions"></a>xAI, *Grok Build User Guide: Session Management*，commit `500129c`，访问 2026-07-30。[`17-sessions.md`](https://github.com/xai-org/grok-build/blob/500129c714ad1b10e6095481f4a8387a2ec52649/crates/codegen/xai-grok-pager/docs/user-guide/17-sessions.md)
- <a id="grok-subagents"></a>xAI, *Grok Build User Guide: Subagents and Personas*，commit `500129c`，访问 2026-07-30。[`16-subagents.md`](https://github.com/xai-org/grok-build/blob/500129c714ad1b10e6095481f4a8387a2ec52649/crates/codegen/xai-grok-pager/docs/user-guide/16-subagents.md)
- <a id="grok-memory"></a>xAI, *Grok Build User Guide: Cross-Session Memory*，commit `500129c`，访问 2026-07-30。[`13-memory.md`](https://github.com/xai-org/grok-build/blob/500129c714ad1b10e6095481f4a8387a2ec52649/crates/codegen/xai-grok-pager/docs/user-guide/13-memory.md)
- <a id="aider-history"></a>Aider, 开源递归聊天历史摘要实现，commit `5dc9490`，访问 2026-07-30。[`aider/history.py`](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/history.py)

[openai-multi-agent]: https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/multi_agent.md
[openai-handoffs]: https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/handoffs.md
[openai-sessions]: https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/sessions/index.md
[openai-context]: https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/context.md
[openai-running]: https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/running_agents.md
[openai-tracing]: https://github.com/openai/openai-agents-python/blob/992abf763d24881bab55663de6a93cf58f1c6118/docs/tracing.md
[codex-slash]: https://github.com/openai/codex/blob/6219b7c40fc9c702c0aef9964e72b492558f60e4/codex-rs/tui/src/slash_command.rs
[codex-input-restore]: https://github.com/openai/codex/blob/6219b7c40fc9c702c0aef9964e72b492558f60e4/codex-rs/tui/src/chatwidget/input_restore.rs
[codex-interrupt]: https://github.com/openai/codex/blob/6219b7c40fc9c702c0aef9964e72b492558f60e4/codex-rs/tui/src/chatwidget/tests/exec_flow.rs
[claude-client]: https://github.com/anthropics/claude-agent-sdk-python/blob/f8b9ec923982082a02c485924e0f60367949c3a1/src/claude_agent_sdk/client.py
[claude-streaming]: https://github.com/anthropics/claude-agent-sdk-python/blob/f8b9ec923982082a02c485924e0f60367949c3a1/examples/streaming_mode.py
[claude-types]: https://github.com/anthropics/claude-agent-sdk-python/blob/f8b9ec923982082a02c485924e0f60367949c3a1/src/claude_agent_sdk/types.py
[grok-sessions]: https://github.com/xai-org/grok-build/blob/500129c714ad1b10e6095481f4a8387a2ec52649/crates/codegen/xai-grok-pager/docs/user-guide/17-sessions.md
[grok-subagents]: https://github.com/xai-org/grok-build/blob/500129c714ad1b10e6095481f4a8387a2ec52649/crates/codegen/xai-grok-pager/docs/user-guide/16-subagents.md
[grok-memory]: https://github.com/xai-org/grok-build/blob/500129c714ad1b10e6095481f4a8387a2ec52649/crates/codegen/xai-grok-pager/docs/user-guide/13-memory.md
[aider-history]: https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/history.py
