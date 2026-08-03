# CoT 删冗余压缩与后续上下文：业界实践

> 状态：业界调研笔记，不是实现方案。
>
> 调研日期：2026-07-22。供应商的协议、模型支持范围和默认阈值变化很快，采用前应复核原文。
>
> 范围：当代原生 reasoning/thinking 模型与 agent 的 context compaction。这里的 "CoT summary" 专指删除不必要的推理、工具轨迹和旧对话后，为继续任务保留的压缩状态。
>
> 不在范围：Prompted CoT（"think step by step"、few-shot CoT、ReAct 等）、面向用户展示的 reasoning summary、获取原始私有 CoT、本仓库现状或实现方案。

## 1. 结论

- **有 compaction 的成熟产品通常会把压缩产物用于下一轮上下文，但并非把原始 CoT 变成一段可读历史。** OpenAI Responses 使用不可编辑的 encrypted/opaque compaction item；Anthropic 使用 compaction block；Claude Code、Codex CLI 和一批 agent 则用结构化任务状态摘要替换旧 history。[1][3][5][6]
- **“进入 history”必须分层说。** 它可以进入客户端保存的 message list、框架的 state/checkpoint、下一次实际 API input，或只驻留在 provider 的 server-side continuation。最后一种情况下，客户端并不会看到一条 summary message，但下一轮模型确实从压缩状态继续。[1][2]
- **原生 raw CoT 通常仍是隐式/不可访问的。** OpenAI 明确不通过 API 暴露 reasoning token；其 compaction artifact 只被描述为承载 prior state and reasoning 的 opaque machine state。不能把它等同为“可读 CoT 的摘要”。[1][2]
- **通用框架没有统一默认。** LangChain/LangGraph、Semantic Kernel 等提供可选 summary/reducer；安装并触发后，文本摘要会替换旧 messages 并成为后续输入。未启用这些组件时，普通 session/history 不会自动生成这种摘要。[9][10][11]
- **不同 agent 对“可见 reasoning 是否参加摘要”有相反策略。** Aider 先删除 tagged reasoning 再总结；OpenHands 的 native reasoning fields 不会被文本化给 condenser，但可见 `thought` 可能会；Cline 的 agentic compaction 会让截断后的可见 `thinking` 参与，而其 basic fallback 会删除 thinking。[12][13][14]
- 因而，对问题“主流框架会把 CoT summary 放入上下文历史吗？”最准确的回答是：**会把“可续做的压缩状态”放入后续上下文的实现很常见；会把 raw CoT 或 UI reasoning summary 放进去则不是常规，更不是统一默认。**

## 2. 先把对象分开

本题中的 summary 应称为 **compaction artifact** 或 **continuation state**，以避免和可见 reasoning summary 混淆。

```text
旧任务轨迹
  = 用户目标 + assistant 结果 + 工具输出 + 可见思路 + 可能的 provider 状态
                         |
                         | compaction：删冗余、保留续做所需状态
                         v
压缩产物 + 最近未压缩 tail
                         |
                         v
下一次模型采样的上下文
```

| 层 | 含义 | 是否等于本题的 summary |
| --- | --- | --- |
| 原生 raw CoT / reasoning tokens | 模型内部推理；往往不通过 API 返回 | 否。通常不可读、不可改写，也不应假设能被应用总结 |
| reasoning display summary | 为 UI 或调试而生成的简短说明 | 否。显示过不代表下一轮会读到 |
| compaction artifact | 旧轨迹的替代物，保留任务目标、决策、证据、完成项与待办 | 是 |
| transcript / session store | 应用或框架保存的完整会话记录 | 不一定；保存了不代表下一轮会发送 |
| actual model input / server continuation | 真正参与下一次采样的 messages 或 provider state | 这是判断“是否放进上下文”的唯一充分标准 |

所以应当问两个独立问题：

1. 压缩结果是否保存到了本地 history/state？
2. 下一次调用是否以它替代旧轨迹，或由服务端以它续接？

只有第二个答案为“是”，才是“进入了模型上下文”。

### 2.1 现在的原生 CoT 是显示还是隐式？

对非 prompted CoT 而言，不能用一个跨厂商的“显示/不显示”开关概括，但 **raw 推理通常是隐式或受 provider 控制的**：

- OpenAI 明确说 reasoning tokens 不通过 API 暴露。支持 persisted reasoning 的模型可以在后续请求使用兼容 reasoning items；这是 opaque continuation，而不是把 raw reasoning 文本拼进普通聊天记录。[2]
- Anthropic 的 extended thinking 与 compaction 是两套机制。thinking 的显示可以被摘要化或省略，签名/加密状态则要按协议 round-trip；它不能被当作一个可由框架任意删改的普通 CoT string。[4]
- 因而，产品 UI 显示 plan、thinking summary 或进度，既不能证明它展示了完整 CoT，也不能证明该文本就是下一轮上下文。应以实际 message/state 协议判断。

这也是本题为什么要聚焦 compaction：它处理的是**任务连续性状态**，不是提供一个跨模型通用的 raw-CoT 读取和压缩接口。

## 3. 直接比较：压缩产物是否进入后续上下文

| 系统 / 路径 | 压缩产物的形态 | 是否进入下一轮实际上下文 | 对原生/可见 CoT 的处理 | 默认边界 |
| --- | --- | --- | --- | --- |
| OpenAI Responses API | canonical compacted context window，含 encrypted/opaque compaction item，可能也有 retained items | **是。** Stateless 链路把完整 `compacted.output` 原样传给下一次 `/responses`；`previous_response_id` 链路由服务端续接 | 可携带 prior state and reasoning，但 raw reasoning 不暴露且产物不可按普通文本编辑 | 需启用 `context_management` 或调用 `/responses/compact` [1][2] |
| Anthropic Messages server-side compaction | `compaction` block，其中写入会话的压缩摘要 | **是。** 客户端 append response 后，API 丢弃该 block 之前的 content，从其继续 | 不是原始 thinking block；thinking signature 的 round-trip 是另一套协议 | 当前为 `compact_20260112` beta，需要显式启用 [3][4] |
| Claude Code | structured conversation summary | **是。** `/compact` 用它替换对话；启动内容另行重载 | 文档明确 full tool outputs 和 intermediate reasoning 会消失，而目标、关键决策、文件和下一步被保留 | 手动 `/compact` 或产品的自动 compaction；不是 UI thinking summary [5] |
| Codex CLI，inline/remote compaction | inline 路径为最近真实 user messages 加一条 compaction summary；remote 路径可为 opaque compaction item | **是。** replacement history 被安装为后续 turn 的基线 | inline 路径不保留旧 assistant/reasoning item；远程路径显式过滤 `ResponseItem::Reasoning`、tool call 和 tool output | 手动或自动上下文压缩，取决于运行路径/阈值；特性分支也可仅开 fresh window [6][7][15] |
| OpenAI Agents SDK `OpenAIResponsesCompactionSession` | Responses compact output | **是。** decorator 清空 underlying session 后写入 compacted output，下一 run 从该 session 读取 | 继承 OpenAI provider-native 的 opaque 语义，不是 SDK 造一段 UI summary | 必须显式包裹 session；默认触发阈值可覆盖 [8] |
| LangChain / LangGraph `SummarizationMiddleware` | 普通 `HumanMessage("Here is a summary...")` 加保留的 recent messages | **是。** `before_model` 清空 messages state 后写入 summary 和 tail；checkpointer 可跨 invoke 保留 | 仅能总结已 materialize 的 messages；没有 hidden provider CoT 的通用通道 | middleware 和 trigger 均需配置；默认 `trigger=None` 不做摘要 [9][10] |
| Semantic Kernel `ChatHistorySummarizationReducer` | 一条 summary message 加未压缩 remainder | **是。** reducer 重组 `self.messages`，该 ChatHistory 后续会传给 chat completion | 默认不把 function call/result 纳入摘要；无法取得 provider raw CoT | `auto_reduce` 默认 `false`，需显式使用/触发 [11] |
| Vercel AI SDK | 默认没有 continuation summary；`pruneMessages` 只删除 parts | **默认否。** 应用须自行生成 summary，并通过 `prepareStep` 返回新的 messages；跨 HTTP request 也须应用自行持久化 | `pruneMessages` 可去掉 reasoning/tool parts，但不把它们凝练成任务状态 | SDK 提供入口，不代替应用配置压缩策略 [17] |
| Aider | role=`user` 的聊天摘要，加较新的 tail | **是。** 新 `done_messages` 参与后续 request | 先 `remove_reasoning_content()`，再写回 history 和总结；所以不是 CoT 压缩 | token 阈值触发 history summary [12] |
| OpenHands SDK | `CondensationSummaryEvent`，转换为 role=`user` message | **是。** condenser 删除旧 events、插入 summary，View 再转 provider messages | native reasoning fields 不由 event `str()` 文本化；`thought` 仍可能进入 | 显式请求或 token/event 阈值触发 [13] |
| Cline | `Context summary:` 的 role=`user` message 加 recent tail | **是。** summary message 与 tail 组成下一轮 history，并可持久化 compaction state | agentic 模式将可见 thinking 截断后纳入摘要输入；basic fallback 删除 thinking/redacted thinking | 启用 compaction 后按输入预算自动或手动触发 [14] |

表中最重要的共同点是：**summary 的目标不是复述推理过程，而是替代旧上下文以继续任务。** 其内容通常更接近“任务意图、已验证事实、做过的变更、未完成工作、当前约束”，而不是逐步思维链。

## 4. 三种主流机制

### 4.1 Provider-native opaque compaction

OpenAI 的 `/responses/compact` 是最清楚的例子：输出是新的 canonical context window，官方要求不要编辑或继续裁剪它，而是原样用于下一次调用。其 encrypted compaction item 在更少 token 中携带 key prior state and reasoning。[1]

这回答了“压缩后的 CoT 会不会在上下文中”：**可能会以 provider machine state 的方式在，但不是应用可读的 CoT summary。** 对 `previous_response_id` 而言，压缩 state 可以只存在服务端；对 stateless input array 而言，它必须作为 output item 被客户端保存并回传。[1][2]

Anthropic 的 server-side compaction 走相似目标、不同 wire format：到阈值时模型生成 compaction block；将 response content 接回 messages 后，API 用该 block 取代此前内容。它是可继续任务的会话摘要，但不等同于 extended thinking 的 signature 或 raw thinking。[3][4]

这类机制的边界是：provider output item/block 属于各自的续接协议。OpenAI item 是 opaque；Anthropic block 也必须按 API 要求完整 round-trip。把它只摘成一段显示文本、删掉 metadata，或用自己生成的摘要冒充原 item，均不能保证连续性。

### 4.2 Agent 自己重写 active history

Claude Code 的公开 context-window 资料说明，`/compact` 会把 conversation 替换为 structured summary；保留用户请求和意图、关键技术概念、重要文件/修改、错误及修复、pending work，而完整 tool output 和 intermediate reasoning 被移除。[5]

Codex 的公开源码可以更精确地说明两个路径：

- inline path 收集真实 user messages，生成 summary，然后安装新的 compacted history；`build_compacted_history` 只构造 user messages 和 summary message；[6]
- remote path 将 compact endpoint 的 output 安装为 replacement history，但 `should_keep_compacted_history_item` 明确丢弃 `ResponseItem::Reasoning`、tool calls 和 tool outputs，同时保留 compaction item 和允许的消息。[7]

因此，两个主流 coding agent 的可核验实现都不支持“把旧 raw CoT 压缩成普通可读 history”这一概括；它们保留的是可续做的状态，并主动移除大量中间轨迹。

Codex 还存在版本/feature 路径上的例外：启用 TokenBudget 的 compact 路径会开启 fresh context window，而不进行 model/server summary。因此不能将某一条实现路径写成所有 Codex compaction 的永久协议。[15]

### 4.3 框架的 message-state reducer

LangChain/LangGraph 的 `SummarizationMiddleware` 是字面意义上的“summary 写进 history”：达到开发者配置的 trigger 后，它返回 `RemoveMessage(REMOVE_ALL_MESSAGES)`，再写入一条带 `lc_source: "summarization"` 的 `HumanMessage` 和最近保留的 messages。下一次 model node 看到的就是这套新 state。[9][10]

Semantic Kernel 的 `ChatHistorySummarizationReducer` 同样在 `reduce()` 后设置新的 `self.messages`。它默认不将 function call/result 放入摘要，显示该框架将“保留可续做任务事实”与“保留完整运行轨迹”分开处理。[11]

这里的“CoT 是否参与”完全取决于框架 state 中原本有什么：如果 provider 的 thinking 从未 materialize 成 message，reducer 无从总结；如果开发者将可见 reasoning 写入消息，它则可能像普通文本一样被总结。LangChain、Semantic Kernel 都没有跨 provider 的通用 raw-CoT 读取通道。

反例也很重要：Vercel AI SDK 的内建 `pruneMessages` 是删 reasoning/tool parts 的 pruning，不会自动生成 continuation summary；只有应用自己生成并从 `prepareStep` 返回替代 messages 时，摘要才会进入当前和后续 agent-loop step。独立 HTTP 调用之间仍由应用负责保存与重传。[17]

## 5. 同样写 summary，CoT 处理为什么不同

公开 OSS agent 的源码给出三个很有价值的反例：

| Agent | 总结前的 reasoning 策略 | 说明 |
| --- | --- | --- |
| Aider | 删除 tagged reasoning content | 摘要进下一轮，但它保留的是对话/结果状态而不是 tagged reasoning [12] |
| OpenHands | condenser 对 forgotten event 使用 `str(event)`；native provider reasoning fields 不出现在该文本表示中 | 原生 reasoning 不会自动文本化；普通 `thought` 字段仍可参与，不能笼统说“完全剥离” [13] |
| Cline | agentic strategy 让截断的可见 `thinking` 参与 summary；basic strategy 删除 thinking | 即使同一个产品，不同 compaction strategy 也会得到相反结果 [14] |

这解释了为什么“主流框架会不会把 CoT summary 放进 history”不能用单个 yes/no 回答：

- **压缩状态会不会用于后续上下文**：在启用 compaction 的系统中，常常会；
- **压缩器的输入是否含可见 reasoning**：实现和策略不同；
- **是否含 provider raw CoT**：通常否，或只以 opaque continuation state 的形式按 provider 协议续接；
- **是否向用户显示**：又是独立的 UI 决策。

## 6. 常见误判和边界

1. **把 visible reasoning summary 当 compaction artifact。** 前者可以只用于 UI；后者必须在下一次模型调用或 server continuation 中生效。这两种数据即使都叫 summary，也没有相同的语义。
2. **从“数据库/trace 有 summary”推导“模型读到了它”。** 必须检查实际 request builder、state reducer 或 provider continuation 协议。
3. **把普通文本 summary 当 opaque state 的替代品。** OpenAI 和 Anthropic 的 provider-native output 有专门的 round-trip 语义；文字摘要不能保证恢复 tool-loop 或 reasoning continuity。[1][3]
4. **把删除 CoT 和关闭模型推理混为一谈。** compaction 是旧上下文管理；当前请求仍可能产生新的 reasoning token，二者是不同控制面。[2]
5. **认为所有 tool trace 都应进摘要。** Claude Code、Semantic Kernel、Codex、Aider 均表明成熟实现会主动删掉大量完整工具输出或 reasoning，只保留对续做有价值的结论、产物和 pending state。[5][7][11][12]
6. **忽略摘要漂移。** 反复用 summary 替换 history 会累积遗漏或错误重述；这也是产品通常保留 recent tail、保留少量真实 user messages，或允许手动带焦点压缩的原因。[5][6][9]
7. **把持久 transcript 与活跃 model context 当成同一个东西。** Claude Code 文档同时说明 session transcript/resume 的完整历史与 `/compact` 对 active history 的替换；公开资料没有披露 resume 时的完整 replay 算法，不能据此断言被压缩的 raw reasoning 会重新送回模型。[16]

## 7. 可复用的总判断

业界主流并非“保存 CoT transcript”，而是如下分层：

```text
raw/provider reasoning     -> 不暴露，或按 opaque state 协议续接
可见消息与工具轨迹          -> 删除、裁剪或提炼
任务连续性状态              -> compaction artifact / structured summary
下一次模型调用              -> artifact + recent tail + 新输入
```

所以，若“CoT summary”指本题定义的“删除不必要内容后的续做摘要”，答案是：**成熟 agent 和带 summary reducer 的框架经常把它放进后续上下文，但它的正确名字应是 compaction state，而不是 raw-CoT summary。** 如果没有打开 compaction/session decorator/middleware，通用 framework 的默认 history 通常不会自动做这件事。

## References

[1] OpenAI, [Compaction](https://developers.openai.com/api/docs/guides/compaction), accessed 2026-07-22.

[2] OpenAI, [Reasoning models](https://developers.openai.com/api/docs/guides/reasoning/), accessed 2026-07-22.

[3] Anthropic, [Server-side compaction](https://platform.claude.com/docs/en/build-with-claude/compaction), accessed 2026-07-22.

[4] Anthropic, [Extended thinking: controlling thinking display](https://platform.claude.com/docs/en/build-with-claude/extended-thinking#controlling-thinking-display) and [thinking encryption](https://platform.claude.com/docs/en/build-with-claude/extended-thinking#thinking-encryption), accessed 2026-07-22.

[5] Anthropic, [Claude Code: Explore the context window](https://code.claude.com/docs/en/context-window), accessed 2026-07-22.

[6] OpenAI Codex, [inline compaction and replacement-history construction](https://github.com/openai/codex/blob/21db216db05d13713f09189fc44872d22cf47fc4/codex-rs/core/src/compact.rs#L344-L379) and [compacted-history builder](https://github.com/openai/codex/blob/21db216db05d13713f09189fc44872d22cf47fc4/codex-rs/core/src/compact.rs#L601-L665), fixed public source revision, accessed 2026-07-22.

[7] OpenAI Codex, [remote compaction installs replacement history](https://github.com/openai/codex/blob/21db216db05d13713f09189fc44872d22cf47fc4/codex-rs/core/src/compact_remote.rs#L258-L315) and [filters reasoning/tool items](https://github.com/openai/codex/blob/21db216db05d13713f09189fc44872d22cf47fc4/codex-rs/core/src/compact_remote.rs#L317-L359), fixed public source revision, accessed 2026-07-22.

[8] OpenAI Agents SDK JS, [Sessions: history compaction](https://github.com/openai/openai-agents-js/blob/b04baf06313564e40c1879c13d4ee960f02b6167/docs/src/content/docs/guides/sessions.mdx#L158-L204) and [session implementation](https://github.com/openai/openai-agents-js/blob/b04baf06313564e40c1879c13d4ee960f02b6167/packages/agents-openai/src/memory/openaiResponsesCompactionSession.ts#L156-L216), fixed public source revision, accessed 2026-07-22.

[9] LangChain, [Short-term memory: Summarize messages](https://docs.langchain.com/oss/python/langchain/short-term-memory#summarize-messages), accessed 2026-07-22.

[10] LangChain, [SummarizationMiddleware state replacement](https://github.com/langchain-ai/langchain/blob/592055e15e138f5369dce95dd049ce22430996e2/libs/langchain_v1/langchain/agents/middleware/summarization.py#L369-L443), [summary message construction](https://github.com/langchain-ai/langchain/blob/592055e15e138f5369dce95dd049ce22430996e2/libs/langchain_v1/langchain/agents/middleware/summarization.py#L720-L848), fixed public source revision, accessed 2026-07-22.

[11] Microsoft Semantic Kernel, [ChatHistorySummarizationReducer](https://github.com/microsoft/semantic-kernel/blob/e15ae168f6d8d67ee95bd6e89d9b67ff9dfc1a4a/python/semantic_kernel/contents/history_reducer/chat_history_summarization_reducer.py#L35-L210), fixed public source revision, accessed 2026-07-22.

[12] Aider, [history summarization](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/history.py#L15-L119), [history replacement](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/base_coder.py#L1002-L1046), and [reasoning removal](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/reasoning_tags.py#L14-L40), fixed public source revision, accessed 2026-07-22.

[13] OpenHands Software Agent SDK, [LLM summarizing condenser](https://github.com/OpenHands/software-agent-sdk/blob/56ac31719f91059dc9b319dbc6bb79f17ef60cd7/openhands-sdk/openhands/sdk/context/condenser/llm_summarizing_condenser.py#L96-L223), [summary event and model-message conversion](https://github.com/OpenHands/software-agent-sdk/blob/56ac31719f91059dc9b319dbc6bb79f17ef60cd7/openhands-sdk/openhands/sdk/event/condenser.py#L83-L132), and [action string representation](https://github.com/OpenHands/software-agent-sdk/blob/56ac31719f91059dc9b319dbc6bb79f17ef60cd7/openhands-sdk/openhands/sdk/event/llm_convertible/action.py#L135-L164), fixed public source revisions, accessed 2026-07-22.

[14] Cline, [compaction trigger](https://github.com/cline/cline/blob/099c6179e4203cb45aef87fce4434de1d5faff50/sdk/packages/core/src/extensions/context/compaction.ts#L248-L335), [agentic replacement](https://github.com/cline/cline/blob/099c6179e4203cb45aef87fce4434de1d5faff50/sdk/packages/core/src/extensions/context/agentic-compaction.ts#L205-L280), [summary message](https://github.com/cline/cline/blob/099c6179e4203cb45aef87fce4434de1d5faff50/sdk/packages/core/src/extensions/context/compaction-shared.ts#L720-L740), and [thinking handling](https://github.com/cline/cline/blob/099c6179e4203cb45aef87fce4434de1d5faff50/sdk/packages/core/src/extensions/context/compaction-shared.ts#L132-L175), fixed public source revision, accessed 2026-07-22.

[15] OpenAI Codex, [TokenBudget compact path](https://github.com/openai/codex/blob/4f3852107e5eedeb4cb89b57a6d4a35b49f8a59a/codex-rs/core/src/compact_token_budget.rs#L20-L89) and [compaction-path selection](https://github.com/openai/codex/blob/4f3852107e5eedeb4cb89b57a6d4a35b49f8a59a/codex-rs/core/src/tasks/compact.rs#L27-L81), fixed public source revision, accessed 2026-07-22.

[16] Anthropic, [Claude Code sessions: what a resumed session restores](https://code.claude.com/docs/en/sessions#what-a-resumed-session-restores) and [manage context within a session](https://code.claude.com/docs/en/sessions#manage-context-within-a-session), accessed 2026-07-22.

[17] Vercel AI SDK, [Compact Agent Context](https://ai-sdk.dev/cookbook/guides/agent-context-compaction), [`pruneMessages`](https://ai-sdk.dev/docs/reference/ai-sdk-ui/prune-messages), [pruning implementation](https://github.com/vercel/ai/blob/66b71512135a0246a9306e97c8847a5d0bcc57ae/packages/ai/src/generate-text/prune-messages.ts#L17), and [`prepareStep` input/history handling](https://github.com/vercel/ai/blob/66b71512135a0246a9306e97c8847a5d0bcc57ae/packages/ai/src/generate-text/generate-text.ts#L819-L855) [and loop-state update](https://github.com/vercel/ai/blob/66b71512135a0246a9306e97c8847a5d0bcc57ae/packages/ai/src/generate-text/generate-text.ts#L1348-L1350), accessed 2026-07-22.
