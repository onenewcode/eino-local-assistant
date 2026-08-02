# 智能体上下文压缩：业界方案调研与探索方法

> 状态：调研结论与通用实施建议，不代表任何具体产品已经实现。
>
> 调研日期：2026-07-15。产品行为、模型能力和框架 API 会持续变化，实际采用前应重新核验引用资料。

## 1. 摘要

上下文压缩的目标不是“尽可能把更多历史塞进模型窗口”，而是让每次推理拥有**完成下一步所需的最小高信号证据集**。上下文窗口虽然越来越大，但长输入仍会带来相关性污染、关键事实定位困难、token 成本和延迟上升等问题。[1][2]

业界主流并非单一摘要算法，而是一个分层的 context compiler：

```text
不可丢的指令、权限、当前目标和计划
        +
最近的原始对话和当前工具结果
        +
带来源引用的结构化滚动摘要
        +
按需召回的冷记忆和原始 artifact
        +
可恢复的 checkpoint
```

推荐采用如下组合及顺序：

1. 以 token 预算、角色/工具分组和确定性裁剪作为容量安全网。
2. 把可重新读取的大型工具输出、文件和网页结果外置为 artifact，并在上下文中保留短 digest 与引用。
3. 将稳定的旧对话前缀压缩成带 provenance 的结构化 checkpoint；原始事件永不被摘要替代后删除。
4. 对跨会话或跨文档信息使用 lexical + semantic 的混合检索、过滤和 rerank，但不把检索当成唯一正确性来源。
5. 将高体量探索隔离到独立 context 的只读 worker；主 agent 只接收受预算约束的成果卡和可重开证据。

最小可行路径是先交付“手动结构化压缩 + 原始记录保留 + 可观测性”，再逐步加入自动触发、artifact、检索和多 agent 隔离。直接从向量记忆或自动多 agent 开始，往往会在 provenance、质量评估和成本控制上失去边界。

## 2. 术语与问题边界

| 术语 | 含义 | 不应混同为 |
| --- | --- | --- |
| Context engineering | 对每次模型调用输入内容的选择、组织、更新和预算管理。 | 只改 system prompt 的 prompt engineering。 |
| 工作上下文 | 当前一次请求实际发送给模型的临时视图。 | 完整 transcript 或永久记忆。 |
| 压缩 | 用更小的可用表示替换工作视图中的旧信息。 | 删除真相源，或保证绝对无损。 |
| Checkpoint | 指向一段来源范围的、版本化的结构化工作状态。 | 唯一的会话记录。 |
| Artifact | 原始工具输出、文件片段、网页结果或测试日志等可重读证据。 | 只含一段不可验证摘要的文本。 |
| 检索型记忆 | 从冷数据中按当前任务召回并排序的候选证据。 | 保证完整召回的数据库查询。 |
| Context isolation | 将子任务放入独立 context window，只向父任务回传受控结果。 | 将完整子任务 transcript 拼回主会话。 |

好的设计追求的是**受控损失且可恢复**：模型能使用紧凑工作集，关键结论可回到原始证据，压缩失败不会毁坏历史，恢复后可以确定性重建，并且用户和运维者能看见上下文为何变化。

## 3. 为什么大窗口仍需要压缩

### 3.1 上下文是有限注意力预算，不只是硬 token 上限

Anthropic 将 context engineering 定义为在有限上下文中持续策展信息的过程，而非单纯写 prompt。[1] `Lost in the Middle` 等研究显示，相关信息放在长输入中部时，即使模型支持该长度，利用能力也可能明显下降。[2]

因此，应把 context 看作存在边际收益递减的注意力预算：

- 越多无关日志和旧尝试，越容易淹没当前约束和下一步；
- 文件读取、搜索结果和命令 stdout 往往比自然语言对话膨胀更快；
- 每个额外 token 同时增加请求成本、首 token 延迟和失败重试代价；
- 一个可容纳全部历史的窗口不等于模型能可靠地推理全部历史。

### 3.2 压缩本身也有代价

摘要消耗额外模型调用、延迟和费用，并会引入遗漏或幻觉。检索会出现漏召回、陈旧内容和排序失误。Checkpoint 与 artifact 也会增长并需要 retention。因此正确问题不是“要不要压缩”，而是：

```text
哪一类信息必须热存？
哪一类信息可变成引用？
哪一类信息应总结？
何时重读原证据？
何时宁可报错或要求用户缩小范围？
```

## 4. 主流技术路线与适用边界

| 路线 | 核心机制 | 代表性资料 | 最适合 | 主要风险 |
| --- | --- | --- | --- | --- |
| Token-aware 截断 | 按 tokenizer/token 估算接近上限后 trim、删除或只留最近窗口。 | LangChain、LlamaIndex。[9][10] | 低价值、可重取内容；最后一道容量保护。 | 静默丢失早期约束、已做决策或失败结论。 |
| 滚动摘要/compaction | 用摘要代替工作视图中稳定的历史前缀。 | Claude Code、LangChain。[3][4][9] | 需要长期叙事连续的多 turn 任务。 | 摘要累积遗漏、幻觉和“摘要漂移”。 |
| 分层/递归摘要 | 先摘要 chunk，再摘要摘要；可检索多个抽象层。 | LlamaIndex tree synthesis、RAPTOR。[11] | 大文档研究、跨来源报告和主题综合。 | 层层压缩丢失细节，关键结论失去叶节点证据。 |
| 工具输出外置/引用 | 原始 stdout、网页和文件保存在 prompt 外，保留 digest、hash、路径或范围引用。 | Anthropic Context Management。[13] | 搜索、构建、测试、代码阅读等高体量工具使用。 | Digest 若不带引用就不可恢复；外部状态可能已过期。 |
| 语义检索/长期记忆 | 将选定历史索引为冷记忆，在当前任务下混合检索、过滤和 rerank。 | LlamaIndex、Letta。[10][12] | 跨会话事实、研究证据、知识库。 | 漏召回、过时事实、错误写入和 prompt injection。 |
| 虚拟分层记忆 | 在热/冷层间移动信息，模拟超出物理窗口的上下文。 | MemGPT。[14] | 长文档、长生命周期助手的架构方向。 | 更多工具调用、调度、观测和写入策略。 |
| Checkpoint/可恢复状态 | 持久化线程状态、cursor、计划和派生摘要。 | LangGraph persistence。[15] | Resume、容错、人机协作和审计。 | 不能自动解决 prompt 体积；需要 retention。 |
| 独立上下文 worker | 子 agent 在隔离窗口探索，只回传摘要和引用。 | Claude Code subagents、Anthropic。[1][7] | 研究、日志分析、测试和并行候选方案。 | 回传结果无预算时仍会挤满父 context。 |

### 4.1 不宜单独采用的方案

- **只做最近窗口截断**：实现很快，但会在长任务中无声丢失“为什么这样做”和“哪些方案已失败”。
- **只做自由文本摘要**：上下文变短，但摘要会成为无法核验的事实源。
- **只做向量检索**：召回不是保证；模型可能找不到唯一关键约束，或把陈旧结果当成当前状态。
- **只提高 context window**：无法解决注意力稀释、工具日志膨胀和单次调用成本。
- **一开始就自动压缩**：尚未建立质量基线、原始记录和失败回滚时，自动化会把问题隐藏起来。

## 5. 产品与开源生态案例（用于验证通用结论）

本节的产品和框架只用于核验第 4 节的通用路线，不能取代前文的 Agent 上下文压缩方法论。尤其是产品内部的模型提示词、服务端选择策略和默认阈值会随版本或账户能力变化；应区分“可观察的接口/事件”与“未公开的内部实现”。

### 5.1 Claude Code

Claude Code 的官方文档体现了较完整的终端 agent 上下文管理模型：

- Context 不只含聊天记录，还包括文件内容、命令输出、项目指令、memory、skill、tool 和 system instructions。[3]
- 接近窗口上限时，优先处理较早 tool output，再在需要时摘要会话；`/compact [focus]` 支持用户声明保留重点。[3][4]
- 压缩后，项目指令和 auto memory 会重新注入，说明持久规则应存放在权威来源，不应只依赖早期一条聊天消息。[4]
- 自动压缩使用阈值而不是固定通用数字；持续低收益时停止，避免无限压缩循环。[3][5]
- `/context` 展示分类用量，MCP tool 定义可按需加载，减少初始 context 占用。[3]
- Subagent 有独立 context，通常只返回最终摘要；复杂探索不会天然污染主任务窗口。[7]
- 新输入在安全 action boundary 交付；中断是 best effort，外部命令可能已经产生副作用。[3][8]

可借鉴的不是某个固定百分比或环境变量，而是“先处理可重取输出、再压缩稳定历史、重载权威指令、检测抖动、公开状态”的交互与边界。

### 5.2 Codex CLI：如何压缩活动上下文（案例）

Codex CLI 不只是把很早的消息从客户端数组中截掉。当前本机可核验的 `codex-cli 0.144.1` 将**模型可见的活动历史**交给专门的 compact 请求，接收一个用于后续请求的 replacement history，并把这次替换作为 thread 内可观察事件安装。[18] 换言之，用户看到的是“总结历史、释放 context”，但实现边界更接近“生成一个新的活动上下文窗口”。

本节刻意将证据分级。`thread/compact/start`、app-server schema、feature 状态、事件名和二进制中的端点/错误文案属于当前版本可复核证据；服务端实际选择了哪些消息、compact prompt 的全文、摘要文本格式和各模型默认阈值均未公开，不能根据效果反推。官方手册仍是后续复核入口。[17]

#### 5.2.1 先区分三个容易混淆的“压缩”

| 层次 | 当前 0.144.1 的可观察证据 | 是否可作为“历史上下文压缩”的证据 | 结论 |
| --- | --- | --- | --- |
| 活动历史 compaction | `remote_compaction_v2` 为 `stable` 且启用；存在 `thread/compact/start`、`ContextCompaction` item、`responses/compact`、`replacement_history`。 | 是。 | 这是本节所说的 Codex context compaction 主路径。 |
| 请求层 compression | `enable_request_compression` 也是 stable feature。 | 否。 | 名称不足以说明它会怎样选择或总结聊天历史；不要把它当作 compaction 算法或 token 保留策略。 |
| 本地 thread store compression | `local_thread_store_compression` 是未默认启用的 under-development feature。 | 否。 | 公开契约未表明它会改变下一轮模型可见 prompt；不能把它当作活动历史变短的证据。 |

同理，配置中的 `tool_output_token_limit` 是工具输出尺寸管理的独立旋钮；公开契约没有说明它与 remote compaction 的先后顺序或选取规则。通用设计中应把“工具输出截短/外置”“传输编码”“本地日志压缩”和“模型活动历史压缩”分别观测，不能只看一个“压缩率”。

#### 5.2.2 可确认的主流程

```text
触发
  ├─ 用户输入 /compact
  └─ 有效模型配置的 token 计数达到 auto-compact 限额
          |
          v
thread/compact/start 或内部 auto-compact 请求
          |
          v
专用 compact turn（同一 turn 不接受 steer）
  ├─ 对应 PreCompact 生命周期 hook
  ├─ remote compaction v2 -> /responses/compact
  └─ 响应必须含恰好一个 compaction output item
          |
          v
安装替换后的活动历史（replacement_history）
  ├─ 记录 contextCompaction thread item / thread-compacted 事件
  ├─ 关联 compact window 的编号和前驱关系
  └─ 对应 PostCompact 生命周期 hook
          |
          v
后续普通 turn 使用 replacement history；必要时再次 compact
```

这个流程的每一层都能由当前版本的接口或静态契约佐证：

1. **触发。**TUI 内含精确提示：`/compact` 会“summarize history and free up context”；app-server 暴露 `thread/compact/start`，其公开入参只有 `threadId`。自动触发的配置契约包含 `model_context_window`、`model_auto_compact_token_limit` 和 `model_auto_compact_token_limit_scope`。后者可按完整 active context（`total`）计数，也可只按 carried window prefix 之后的 sampled output 与增长部分（`body_after_prefix`）计数。[18]
2. **安全边界。**协议明确把手动 `/compact` 归为不能接受 same-turn steering 的 `compact` turn。这说明压缩不是与工具调用、普通生成或新输入任意交错的后台字符串替换；它有独占的生命周期边界。它不表示已经执行的外部工具副作用可回滚。
3. **压缩调用。**`remote_compaction_v2` 在 feature 列表中为 stable；当前二进制含 `compact_remote_v2`、`compact_remote_v2_attempt`、`compact_token_budget` 和 `/responses/compact`。更关键的是，其错误契约要求 remote v2 响应“exactly one compaction output item”。因此，应把这个步骤理解成受专用输出类型约束的历史替换请求，而不是普通 agent turn、tool call，或任意一段自由文本总结。
4. **安装结果。**协议中的 `contextCompaction` 是一个独立 thread item，旧的 `thread/compacted` 通知已标记为兼容路径。二进制的 compact trace/持久化类型还出现 `input_history`、`replacement_history`、`window_number`、`first_window_id` 和 `previous_window_id`。这表明压缩有“输入历史 -> 替代历史”的结果，并追踪多次 compact window 的前驱关系；公开契约未说明 resume 或 fork 如何消费这些字段。
5. **失败容量回退。**当前二进制包含“Context window exceeded while compacting; removing oldest history item”的兜底文案。它证明 compaction 本身也可能装不下，且实现有移除最旧项的容量 fallback。该文案没有公开重试次数、被移除项类型或所有版本的保留保证，因此不能将其描述为无损策略。

#### 5.2.3 Codex 已知“压什么”和未知“怎么写摘要”

| 问题 | 可以确认 | 不应擅自补全 |
| --- | --- | --- |
| 压缩对象 | 目标是后续模型调用所用的 active/replacement history；`input_history` 与 `replacement_history` 是当前 compact 记录的可观察字段。 | 不知道服务端是否先剔除某类 tool output、如何处理 reasoning、图片、MCP 结果或项目指令。 |
| 输出形态 | UI 将结果描述为历史摘要；remote v2 约束为一个 `compaction` output item，并被安装为 replacement history。 | 不知道该 item 是纯自然语言、结构化 JSON，还是包含服务端不可见的内部表示；不能声称其具备 provenance 字段。 |
| 自动阈值 | 限额和计量 scope 可由模型/配置提供。 | 不知道任何模型的默认 token 数、触发百分比、目标水位或 hysteresis；也不能断言所有提供商启用自动压缩。 |
| Compactor 提示 | `ConfigReadResponse` 暴露了可空的 `compact_prompt` 配置字段。 | 该字段不公开默认文本，也不能单凭字段名断言远端 endpoint 的完整提示词、优先级或是否允许用户覆盖。 |
| 原始记录 | thread 中存在 compaction item 和窗口关联，CLI 可 resume/fork。 | 这不证明所有未压缩原始内容在所有存储后端永久保留、可导出或可逐字恢复。 |
| 生命周期 | 有 `PreCompact`、`PostCompact` hook 名称及 compaction 事件。 | 不能仅凭 hook 名称假定 hook 可改变 summary、阻止安装或获得完整原文。 |

因此，下面这段伪代码只表达已知的控制流，不虚构服务端提示词或选择算法：

```text
if user_requests_compact() or charged_tokens_reach_model_limit():
    begin_non_steerable_compact_turn()
    emit_pre_compact_lifecycle()

    result = remote_compact(active_history)  # 需要一个 compaction output item
    install_replacement_history(result)
    emit_context_compaction_item_and_lifecycle()

    emit_post_compact_lifecycle()

if compaction_cannot_fit_context_window():
    apply_reported_oldest-history fallback  # 具体选取与重试规则未公开
```

#### 5.2.4 对通用 Agent 重构的可迁移结论

- **采用专门的 compact transaction，而非让主 ReAct 回答“请总结”。**它应有独立 turn、不可交错的安全边界、输入/输出契约和安装步骤。
- **将“摘要”视为 replacement view，而非真相源。**Codex 的 `replacement_history` 命名直接提醒我们：这是后续推理的工作视图。通用实现仍应保留原始事件和 artifact，并让 checkpoint 带来源。
- **触发度量应明确配置。**Codex 的 `total` 与 `body_after_prefix` 表明“计入多少 token”是产品决策；不要用单一百分比掩盖稳定指令前缀、输出 reserve 和滚动窗口的差异。
- **把结果和降级做成可观察事件。**`contextCompaction` item、生命周期 hook 和最旧项 fallback 是值得借鉴的可观测边界；通用产品还应额外公开释放 token、失败原因和是否损失了可重读证据。
- **不要照搬未公开细节。**与 Claude Code 已文档化的“先清理旧 tool output、再摘要”或 `/compact [focus]` 不同，Codex 当前公开协议没有 focus 字段，也没有披露服务端摘要规则。[3][4][18]

### 5.3 LangChain 与 LangGraph

LangChain 的短期记忆文档同时覆盖 token 触发的 trim/remove 和 `SummarizationMiddleware`，清楚展示了两种基本策略的取舍。[9] LangGraph 将 thread-scoped checkpoint 与跨 thread store 区分开来：前者面向连续性、human-in-the-loop 和容错，后者面向共享事实/知识；二者不应混为一层。[15]

### 5.4 LlamaIndex、Letta 与 MemGPT

LlamaIndex 使用 token 有界短期记忆，并能把溢出内容写入结构化或向量 memory block；Letta 明确区分始终 in-context 的 memory block 与按工具查询的 archival memory。[10][12] MemGPT 则以操作系统内存层级为隐喻，将有限窗口上的数据移动和调度提升为架构问题。[14]

共同结论是：

```text
热数据要少、稳定、可预测；
冷数据要可检索、可验证、可淘汰；
二者之间必须有 provenance 和明确的写入策略。
```

## 6. 推荐的通用组合架构

### 6.1 分层数据流

```text
权威指令和权限 ---------------------+
当前用户目标和计划 -----------------+---> Context Planner ---> Prompt View ---> Model
最近完整 turn/tool 分组 ------------+
活动 checkpoint --------------------+
按需检索的证据片段 -----------------+

完整事件日志 ----> checkpoint / artifact / 索引 ----> 可重读、可检索、可审计
```

其中：

| 层 | 内容 | 处理原则 |
| --- | --- | --- |
| 不可变层 | System/developer 指令、权限、项目规则、当前工具能力。 | 每次均从权威来源装载；不可由摘要或工具数据覆盖。 |
| 活动任务层 | 最新请求、验收条件、当前计划、未决问题、待批准动作。 | 必须热存，不得把当前目标摘要掉。 |
| 工作 checkpoint 层 | 较早稳定历史的结构化状态。 | 有严格 token 上限；关键项带来源。 |
| 热证据层 | 最近的完整对话和仍有因果关系的 tool call/result。 | 保持分组完整，不能产生孤儿 tool result。 |
| 冷源层 | 完整 transcript、artifact、历史 checkpoint、索引内容。 | 默认不发送，按需重读或检索。 |

### 6.2 原始记录、摘要和引用的关系

正确的关系是：

```text
原始事件/原始 artifact = 真相源
        |
        +--> digest / 结构化 checkpoint = 派生工作状态
        |
        +--> 检索索引 = 候选证据入口
```

摘要和检索结果都不是权威记录。任何高风险决定、文件修改、权限操作或用户可见结论，必要时都应通过 source reference 重读原始证据。

多次滚动压缩还需要避免一个隐蔽的反模式：把“截至目前的全部 source ID”复制进每一代 checkpoint。这样即使摘要正文不增长，来源数组也会最终耗尽 `summary_max_tokens`。推荐把 provenance 分为两层：热 checkpoint 只保留固定上限的证据锚点、范围 hash 和父 checkpoint 引用；冷存储则让每个 checkpoint 只记录本次新增的 direct source，并沿 parent lineage 展开完整覆盖集。这样既能验证后代确实覆盖了哪些原始事件，也不会把完整来源清单重新塞回模型窗口。

### 6.3 结构化 checkpoint 的最小内容

与自由文本相比，固定 schema 更易评估、比较、校验和恢复。建议至少保存：

```json
{
  "source_range": {"from": 1, "to": 42, "content_hash": "..."},
  "trigger": "manual | auto",
  "focus": "用户要求保留的重点",
  "task_goal": "当前任务目标",
  "constraints": [{"text": "...", "source_ids": [3]}],
  "confirmed_facts": [{"text": "...", "source_ids": [11]}],
  "decisions": [{"decision": "...", "reason": "...", "source_ids": [18]}],
  "attempts_and_results": [{"text": "...", "source_ids": [21]}],
  "files_or_artifacts": [{"ref": "...", "source_ids": [22]}],
  "open_questions": [{"text": "...", "source_ids": [25]}],
  "next_actions": ["..."]
}
```

关键字段应区别 `observed`、`inferred` 和 `unknown`。`source_ids` 或等价 artifact reference 对约束、事实、决策和测试结论是必需的，不是可选装饰。

### 6.4 Prompt 组装算法

每次请求按下列顺序生成临时 view；该过程应是纯函数，不能改写原始记录：

1. 计算可用输入预算：模型容量减去输出余量、工具余量和安全余量。
2. 放入不可变层与当前活动任务层；若二者本身超限，应明确失败或要求用户缩小请求，不能以摘要掩盖问题。
3. 放入活动 checkpoint 和最近完整 turn/tool 分组。
4. 根据当前任务选择需要的 artifact digest 或重读片段；每段都附来源与新鲜度。
5. 进入软水位时安排压缩；下一次请求将越过硬水位时，在安全边界同步压缩。
6. 仍超限时使用确定性裁剪作为最终兜底，并记录发生了何种降级。

可用的预算概念为：

```text
usable_input = min(configured_input_budget, provider_context_limit)
             - reserved_output
             - reserved_tool_calls
             - safety_margin
```

不要把某一产品的阈值当成普适常量。软水位、目标水位和低收益次数应以真实 trace、模型、工具集和成本目标校准。

### 6.5 压缩事务与失败处理

压缩应在成功 turn 后、且不处于活动工具调用时执行：

1. 在完整 turn 边界选择稳定 source range，并记录 hash/版本。
2. 请求 compactor 生成候选结构化 checkpoint；`focus` 是额外保留约束。
3. 校验 schema、source reference、token 上限和必填字段。
4. 原子保存新 checkpoint，并把它标记为活动工作状态。
5. 基于原始 source 和新 checkpoint 重建 prompt view，再记录前后 token、触发原因和结果。

任何一步失败都应保持旧 checkpoint 和原始 transcript 不变。不得把半截模型输出、无引用摘要或未校验 JSON 投入下一轮上下文。

### 6.6 反抖动保护

当不可变提示过大或单次工具输出持续占满预算时，摘要可能刚生成就再次接近上限。应测量压缩释放量和后续预测用量；连续多次低收益时自动暂停，并展示可执行建议：

- 指定 `/compact <focus>`，减少需要保留的范围；
- 将文件、网页或 stdout 分块读取；
- 把无关工作迁移到新 session；
- 使用上下文隔离 worker 探索；
- 在无法安全 fit 时明确报错，而不是无限重试。

Claude Code 已将这类循环作为可检测的 thrashing 问题处理，值得借鉴。[3][5]

## 7. 如何探索和选择解决方案

探索本身常产生大量搜索、`grep`、构建、测试和网页输出，是 context 膨胀最常见来源。推荐把探索过程设计成“证据生产”而非“把所有过程聊天化”。

### 7.1 Evidence Ledger

每个候选方案或研究分支维护如下记录：

| 字段 | 要求 |
| --- | --- |
| Claim | 需要证实或证伪的候选结论。 |
| Evidence | 原始消息、artifact、文件范围、命令输出或 URL 引用。 |
| 正反观察 | 同时记录支持和反驳，避免只保留成功路径。 |
| 置信度 | 明确标为 `observed`、`inferred` 或 `unverified`。 |
| 新鲜度 | 记录何时观察；工作树、测试结果和外部网页都有失效风险。 |
| 下一次验证 | 能最小成本确认或推翻 claim 的命令、测试或原文读取。 |

最终摘要应压缩 ledger，而不是压缩成一段“看起来合理”的故事。这样能保留失败原因、证据缺口和后续验证路径。

### 7.2 上下文隔离的探索 worker

适合隔离的工作包括：代码库探索、资料搜索、长日志分析、全量测试、多个候选实现的比较。每个 worker 应有：

- 明确任务问题与完成条件；
- 只读 tool allowlist；
- 最大 turn、token、时间和 artifact 数量预算；
- 固定成果卡 schema；
- 完整 transcript/artifact 的可重开引用。

建议把回传成果卡限制为“结论、证据、反例/风险、不确定性、建议下一步、引用”。Anthropic 建议此类研究/分析由独立 context 的子 agent 深挖，主 agent 只接收 condensed result；但多个冗长 result 回流依然会占满主窗口，因此回传也必须有预算。[1][7]

### 7.3 方案选择矩阵

| 观察到的问题 | 优先手段 | 不应首先做的事 |
| --- | --- | --- |
| 单条文件/命令输出太大，但可重读 | 外置 artifact + digest + 指定范围重读。 | 用 LLM 把整份原文反复摘要。 |
| 长任务忘记了早期决策或失败方案 | 带 source 的滚动 checkpoint。 | 只保留最近 N 条 message。 |
| 跨会话需要找历史事实 | 混合检索 + metadata filter + provenance。 | 把向量命中直接当成当前事实。 |
| 多条独立研究线并发 | 独立 worker + 受限成果卡。 | 将全部子任务思考过程合并回主 history。 |
| 当前请求本身超出预算 | 要求拆分、上传/引用文件、分段处理。 | 先压缩过去历史，假装能容纳当前输入。 |
| 摘要反复不能释放空间 | 触发 anti-thrashing，拆分输入或换上下文。 | 无限自动 compact/retry。 |

## 8. 建议的交互与运行语义

以下是适用于终端 agent 的通用交互建议，而非某个特定产品的命令承诺：

| 交互 | 推荐语义 |
| --- | --- |
| `/context` | 只读展示 token 预算、各层占用、最大 artifact、活动 checkpoint、最后一次压缩原因和是否为估算值。 |
| `/compact [focus]` | 用户在稳定边界手动创建结构化 checkpoint；`focus` 指定必须保留的事实或当前任务。 |
| 自动 compact | 仅在达到软/硬水位且不在工具事务中执行；必须显示开始、成功、失败和释放量。 |
| `resume` | 恢复完整事件记录和活动工作状态；应避免多个 writer 无声交错写同一会话。 |
| 新输入排队 | 在当前 action/tool 的安全边界交付，保持 FIFO 和可见状态。 |
| 中断 | best effort cancel；记录已完成副作用，不能把“停止等待”误表示为“外部操作未发生”。 |
| 清理/新建 | 区分仅清屏、重置工作上下文、建立新会话和删除持久记录，不能让相近命令产生隐式破坏性语义。 |

压缩后的状态提示应明确说明它是派生视图，例如：

```text
context compacted: checkpoint cmp-... covers 18 turns; 21.4k -> 8.1k estimated tokens; raw history retained
```

不应把任何 lossy 操作称为“无损”；发生确定性截断时需显示截断原因和 fallback。

## 9. 分阶段实施与调研验证路径

### 9.1 建议顺序

| 阶段 | 目标 | 关键产物 | 暂不引入 |
| --- | --- | --- | --- |
| P0：可观测与基线 | 先知道上下文为何膨胀。 | 真实 token 估算、消息/工具分组、长 trace fixture、context 统计。 | 自动摘要、向量库。 |
| P1：手动结构化压缩 | 证明可恢复的 checkpoint 有用。 | 原始事件保留、schema 校验、`/compact [focus]`、失败回滚、provenance。 | 自动触发、全局记忆。 |
| P2：Artifact 与自动策略 | 解决日志和工具输出膨胀。 | Artifact digest/reference、软硬水位、输出 reserve、anti-thrashing。 | 自主多 agent。 |
| P3：检索与隔离探索 | 处理跨会话和高并发研究。 | 混合检索、rerank、retention、只读 worker、成果卡。 | 无边界的跨项目记忆。 |
| P4：高级能力 | 根据 trace 优化长期运行。 | Checkpoint 选择/undo、branch、并发 lease、人工维护 memory。 | 破坏性历史迁移。 |

### 9.2 必备测试语料

| 场景 | 评估重点 |
| --- | --- |
| 早期关键约束 + 无关长中段 + 后期矛盾请求 | Constraint retention 与优先级。 |
| 多步任务，包含选定和否决方案 | 决策及失败原因是否保留。 |
| 超大工具输出，后续只需要其中一行 | Artifact 引用、范围重读和新鲜度。 |
| Tool call/result 链 | 无孤儿消息、合法角色顺序和必要结果保留。 |
| 压缩或 stream 中取消 | 原始记录和 checkpoint 的原子性。 |
| 多次 checkpoint 后 resume | 工作视图的可复现性。 |
| 两个并发写入者 | Queue/lease 和无交错事件。 |
| 工具/网页返回的 prompt injection 文本 | 指令与数据的权限隔离。 |
| 连续接近上限的大输入 | Anti-thrashing 与清晰用户反馈。 |

LongMemEval 还可作为长期记忆的补充基准，覆盖信息提取、多会话推理、知识更新、时间推理和 abstention，并提供 evidence recall 视角。[16]

### 9.3 指标与验收

| 类别 | 应记录的指标 |
| --- | --- |
| 容量与成本 | 输入/输出 token、prompt utilization、reserve 违例、压缩比、外置工具 token、overflow/retry、压缩次数、任务成本。 |
| 延迟 | Prompt build、compact、retrieval、端到端 turn 的 p50/p95。 |
| 信息质量 | 标注关键约束的 recall、带来源支持的摘要 precision、矛盾率、引用可重开率、Recall@k/MRR、陈旧证据率。 |
| 任务质量 | 固定 trace 成功率、测试通过率、相对全历史 baseline 的回归、resume 后同结果比例、人工纠正率。 |
| 可靠性 | Checkpoint 校验失败、rollback/fallback、低收益压缩、mutex contention、损坏/孤儿事件数。 |

任何压缩方案至少要满足以下门槛：

1. 成功、失败、取消和恢复情况下都不丢失原始记录。
2. 每条高风险摘要结论都有可验证的来源引用。
3. 压缩版本在固定长 trace 上不显著劣于全历史 baseline；阈值应在实验前制定。
4. 能测得 token 成本或 overflow 下降，并同时公开延迟代价。
5. 无法安全压缩时明确失败、请求拆分或暂停自动化，而不是隐式篡改上下文。

## 10. 风险、治理与待决策项

| 风险/决策 | 推荐原则 |
| --- | --- |
| 摘要幻觉或遗漏 | 原始事件永存；结构化字段、source reference、质量基准和人工可审阅。 |
| 检索漏召回或陈旧 | Lexical + semantic 混合检索、metadata filter、rerank、时间戳和关键动作前重读。 |
| 工具/网页 prompt injection | 将外部内容视为数据，不能覆盖 system/developer 或权限规则。 |
| Secret 与隐私 | Artifact 原文、摘要和索引都可能含敏感数据；定义脱敏、加密、访问和删除策略。 |
| 并发会话 | 单 writer/lease 或清晰队列；禁止无声交错写入。 |
| 自动压缩成本 | 记录 trigger、释放量、成功率和模型费用；采用低收益熔断。 |
| Checkpoint/artifact 无限增长 | 定义 retention、导出、清理和恢复策略。 |
| Compactor 模型选择 | 显式配置质量、成本和隐私边界；不能由低质量模型悄悄替换工作状态。 |
| 跨会话记忆 | 默认关闭，直到 provenance、scope、删除和陈旧数据规则成熟。 |

## 11. 参考资料

以下资料均于 2026-07-15 复核。供应商案例和内部 benchmark 只能作为方向性证据，不能视作通用性能承诺。

1. Anthropic，[Effective context engineering for AI agents](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)，2025-09-29。
2. Liu et al.，[Lost in the Middle: How Language Models Use Long Contexts](https://arxiv.org/abs/2307.03172)，2023。
3. Claude Code，[How Claude Code works: the context window](https://code.claude.com/docs/en/how-claude-code-works#the-context-window)。
4. Claude Code，[What survives compaction](https://code.claude.com/docs/en/context-window#what-survives-compaction) 与 [compacting the conversation](https://code.claude.com/docs/en/prompt-caching#compacting-the-conversation)。
5. Claude Code，[environment variables for auto compaction](https://code.claude.com/docs/en/env-vars) 与 [auto-compaction troubleshooting](https://code.claude.com/docs/en/troubleshooting#auto-compaction-stops-with-a-thrashing-error)。
6. Claude Code，[sessions](https://code.claude.com/docs/en/sessions)。
7. Claude Code，[manage subagent context](https://code.claude.com/docs/en/sub-agents#manage-subagent-context)。
8. Claude Code，[interactive mode](https://code.claude.com/docs/en/interactive-mode) 与 [streaming versus single mode](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode)。
9. LangChain，[short-term memory](https://docs.langchain.com/oss/python/langchain/short-term-memory)。
10. LlamaIndex，[agent memory](https://developers.llamaindex.ai/python/framework/module_guides/deploying/agents/memory/)。
11. LlamaIndex，[response synthesizers](https://developers.llamaindex.ai/python/framework/module_guides/querying/response_synthesizers/) 与 Sarthi et al.，[RAPTOR](https://arxiv.org/abs/2401.18059)。
12. Letta，[context hierarchy](https://docs.letta.com/guides/core-concepts/memory/context-hierarchy/) 与 [archival memory](https://docs.letta.com/guides/core-concepts/memory/archival-memory/)。
13. Anthropic，[context management](https://www.anthropic.com/news/context-management)。
14. Packer et al.，[MemGPT: Towards LLMs as Operating Systems](https://arxiv.org/abs/2310.08560)，2024。
15. LangGraph，[persistence](https://docs.langchain.com/oss/python/langgraph/persistence)。
16. Wu et al.，[LongMemEval](https://arxiv.org/abs/2410.10813) 及其 [evaluation repository](https://github.com/xiaowu0162/LongMemEval)。
17. OpenAI，[Codex manual](https://developers.openai.com/codex/codex-manual.md)。
18. Codex CLI 0.144.1，本地可复核的 feature、app-server 协议和静态契约：`codex --version`；`codex features list`；`codex app-server generate-json-schema --out <DIR>`。本次核验使用 `ThreadCompactStartParams`、`ContextCompactedNotification`、`ThreadItemsListResponse`、`ConfigReadResponse`、`TurnStartedNotification` 与 `TurnCompletedNotification` schema，并核对当前可执行文件中的 compact endpoint、生命周期及错误文案；这是当前已安装版本的接口证据，不是跨版本 API 保证，也不是未公开服务端逻辑的证明。
