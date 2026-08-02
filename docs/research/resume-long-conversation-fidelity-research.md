# 已压缩超长会话的 Resume：业界如何恢复并继续

> 状态：研究笔记，不是本仓库的实施计划。
>
> 调研日期：2026-07-21。CLI 存储格式、服务端保留策略和 API 语义会变化；采用前应重新核验引用。
>
> 范围：会话**已经**发生 compaction，进程/CLI 随后退出；只研究用户 resume 后，产品怎样找到会话、读取什么、重建什么、以及第一条新消息怎样进入下一轮模型调用。
>
> 不在范围：如何触发 compaction、如何生成摘要/opaque carrier、普通短会话、RAG/长期记忆，以及本仓库的实现方案。

## 1. Resume 要恢复的不是完整 transcript，而是可继续的 active context

本笔记把先前 compaction 已经留下的状态记作 `C`，它可能是 opaque carrier、`replacement_history`、structured summary 或 `Condensation` event；把 `C` 后尚未再次压缩的消息记作 `T`。

```text
持久会话 store
  |- 原始记录 E1 ... E900（有些产品保留，有些由调用方/服务端决定）
  |- 已存在的压缩状态 C
  `- C 之后的 tail T
             |
             | 用户选择 session ID / --resume / carrier / response ID
             v
resume runtime
  1. 定位会话
  2. 读 C、T 和恢复运行时所需 state
  3. 重建本轮 active history 或由服务端解析它
  4. 加入当前规则、工具和新的用户消息 E901
  5. 发起下一次模型调用
```

“恢复了完整 history”在不同产品中有两种含义，必须分开：

- **存储层恢复**：能重新找到 JSONL、event log、Markdown transcript 或服务端 chain。
- **模型层恢复**：下一请求只使用 `C + T`（或由完整 log 派生的 view）和当前环境；原始记录不自动全部进入 prompt。

下面只描述这条 resume 链路；`C` 是一个既有输入，不讨论它如何产生。

## 2. 有公开源码的 resume 算法

### 2.1 Codex：反向找到最后 checkpoint，只 replay 其后 tail

证据等级：**公开源码实现**，固定提交 `3bc49e1721ef0453d695950dc67bd2f0616c4883`。这说明该版本如何恢复，不是永久外部 API 合同。[4]

#### 退出前已经存在的持久状态

- 本地 canonical durable history 是 rollout JSONL；SQLite 是可重建查询投影。
- JSONL 中可以同时有较早 raw rollout、`RolloutItem::Compacted { replacement_history: ... }`、该 checkpoint 后的 tail，以及 `WorldState` / `TurnContext` 等恢复运行时所需记录。并非所有 UI event 都无条件持久化，仍受 history mode 影响。[4]

#### 用户执行 resume 时，Codex 实际做什么

```text
1. 从 rollout JSONL 读出完整 Vec<RolloutItem>。
2. 从末尾反向扫描，找到最后一个带 replacement_history 的 Compacted item。
3. 令 base = 该 replacement_history。
4. 令 suffix = checkpoint 之后的 rollout items；只顺序 replay suffix。
5. 同一次扫描恢复最新 TurnContext、WorldState patch chain、window lineage、
   rollback/previous settings 等运行时状态。
6. reconstructed_history = base + replay(suffix)。
```

源码注释明确：checkpoint 之前的 rollout items 不再影响这个结果；只有旧格式 `replacement_history=None` 才走兼容 fallback。也就是说，Codex 确实先读完整 JSONL，但不会把 checkpoint 前所有原始记录重新放回 active history。[4]

#### resume 后第一条新消息如何形成模型请求

新 turn 会先 capture 当前环境、当前 AGENTS/instructions、MCP、skills/plugins 等 `StepContext`；再根据恢复出的 reference context 注入完整当前 context 或 context diff。随后记录新用户消息，并构造：

```text
Prompt.input             = reconstructed_history 经 for_prompt 规范化 + 新用户消息
Prompt.tools             = built_tools(当前 tool router)
Prompt.base_instructions = 当前 config override / session meta / 当前 model default
```

因此，Codex 的模型请求不是“旧 rollout + 新消息”，而是：

```text
last replacement_history + post-checkpoint tail
+ 当前 instructions / tools / MCP / skills / world state
+ 新用户消息
```

App Server 的 `thread/resume` 还恢复 stored thread 与 dynamic-tool metadata，并报告已加载的 instruction-file paths；required MCP 初始化失败会使 resume 失败。这是 session 启动层的恢复，和 rollout-history reconstruction 是两层不同的工作。[5]

**留在 prompt 外的东西**：checkpoint 前 raw rollout、非 `ResponseItem` 的 event/meta，以及主要供运行时使用的部分 state 仍在日志中，但不会成为普通 history。即使被选入 history，`for_prompt` 也会规范化内容（例如剥离不支持的媒体），所以 resume 不是字节级重演旧 prompt。[4]

### 2.2 OpenHands SDK：读 active branch，重新派生 View

证据等级：**公开 SDK 源码实现**，固定提交 `68aa583ebf07efefbb9219b63859c1aacecaf7b3`。Condenser 可配置，以下是该 SDK 的 ConversationState / View resume 路径，不应外推成所有部署都相同。[6]

#### 退出前已经存在的持久状态

- `base_state.json` 保存 conversation state，例如 agent、workspace、status、leaf/head、stats 和 agent state。
- 每个 event 独立保存为 `events/event-<index>-<id>.json`；已有的 `Condensation` event 记录哪些 event ID 在 active view 中被 forgotten、summary 应插入何处等信息。

#### 用户恢复会话时，OpenHands 实际做什么

```text
1. ConversationState.create 读取 base_state.json。
2. 构造 EventLog，找到 leaf 到 root 的 active-branch path。
3. 进行 rebuild_view()，即 View.from_events(active_branch)。
4. 依次处理 active branch 中的每个 event：
   - 已有 Condensation：从 View 删除 forgotten_event_ids，在 summary_offset 插入 summary；
   - LLMConvertibleEvent：加入 View；
   - 其他 event：保留在 EventLog，但不加入 View。
5. 校验恢复后的 agent class / tools 兼容性；工具可以新增，不能移除。
```

这和 Codex 不同：OpenHands 不找“最后 checkpoint 后的 tail”。它从完整 active branch 重建一个派生 `View`，并在遍历时应用已有的 `Condensation`。[6]

#### resume 后第一条新消息如何形成模型请求

新用户消息先追加为 `MessageEvent`。下一次 `Agent.step` 将 `state.view.events` 转为 completion `messages`，并把当前 runtime 的 `self.tools_map.values()` 作为独立 `tools=` 参数传给模型。

已有 `SystemPromptEvent` 时，初始化会跳过新的 system prompt；它并不无条件重注入当前 system prompt。当前 tools 则来自 runtime。因此恢复后的请求可以概括为：

```text
messages = rebuilt View + 新 MessageEvent
tools    = 当前 runtime tools
```

**留在 prompt 外的东西**：被 `forgotten_event_ids` 指向的 raw files、非 `LLMConvertibleEvent`（如 Pause/state update/CondensationRequest）和非 active branch 的 event 仍可在 store 中存在，但不会进入 `messages`。公开的 `ConversationState.create` / View rebuild 路径没有读取新用户问题后再搜索旧 event 文件的步骤。[6]

### 2.3 Aider：不保存 compact checkpoint；resume 时从 Markdown 重新构造工作 history

证据等级：**公开源码实现**，固定提交 `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`。[7]

#### 退出前已经存在的持久状态

Aider 默认 append `.aider.chat.history.md`。持久对象是可审阅的 Markdown 原文（包含聊天和显示出的工具/终端内容），而不是当前运行时 `done_messages` 或一个专门的 compact checkpoint。[7]

#### 用户恢复会话时，Aider 实际做什么

```text
1. 用户显式传 --restore-chat-history；默认不会 restore。
2. BaseCoder 读取 .aider.chat.history.md。
3. split_chat_history_markdown() 重新解析 user/assistant messages。
4. parser 默认 include_tool=false：工具段不会进入 restored history。
5. 从重新解析的 messages 重新建立 done_messages；若预算需要，运行时再形成
   当前可用的 summary/history + recent tail。
```

关键区别是：Aider 退出后保留原 Markdown，而不是保存“上一进程已经压缩好的 summary”。所以 resume 不是载入旧 checkpoint，而是 **读取原文 -> 重新解析 -> 重新得到当轮可用 history**。[7]

#### resume 后第一条新消息如何形成模型请求

发请求前 Aider 完成当前 summary 工作，将新的 `done_messages` 放入 `chunks.done`。完整 prompt 还组合当前 system/examples、repo/file context 和新用户消息。

```text
prompt = chunks.done(重新构造的 history) + 当前 repo/file context
       + 当前 system/examples + 新用户消息
```

**留在 prompt 外的东西**：完整 Markdown 仍在磁盘，且工具段默认被 restore parser 排除；源码没有“根据新用户问题检索某段旧 Markdown 并自动回填”的步骤。[7]

## 3. Provider continuation：应用重启后如何继续，不是 CLI `/resume`

这些机制的“resume”由应用完成：进程重启后，应用读 carrier/window/ID，然后发新的 API 请求。

### 3.1 OpenAI Responses：input-array 中已有 server-side compaction item

证据等级：**API 合同**。假设应用在退出前已经有最新 encrypted compaction item 和它之后需要保留的 items；本节不讨论该 item 如何产生。[1]

```text
退出前持久化：H = [latest_compaction_item, post-compact tail]

应用重启：读回 H

下一请求：input = [latest_compaction_item, post-compact tail, 新用户消息]
```

OpenAI 的 input-array 合同允许应用丢弃最新 compaction item **之前**的 items；因此恢复时只需要读回这个保留工作集，而不是重放整个旧 input/output 数组。encrypted item 是 provider continuation state，应用不应改写成普通 summary。服务端如何展开该 opaque state 为最终模型 prompt 未公开。[1]

### 3.2 OpenAI Responses：已有 standalone `/responses/compact` output

这里假设应用退出前已经持久化了完整 `compacted.output`。该 output 是 canonical compacted context window，通常不止一个 item，可能还有 retained items。[1]

```text
退出前持久化：C = compacted.output（整个序列）

应用重启：读回 C

下一请求：input = [...C, 新用户消息]
```

与上一节不同，standalone 的 `compacted.output` 必须整体原样使用，不能 prune 或 rewrite。resume 代码不需要知道 C 内如何压缩，只要按合同保存、加载和传回整个 window。[1]

### 3.3 OpenAI Responses：已有 `previous_response_id`

`previous_response_id` 是服务端 response chain 的指针，不是本地 history 文件，也不是 compaction 命令。

```text
退出前持久化：latest_response_id

应用重启：读回 latest_response_id

下一请求：{ previous_response_id: latest_response_id, input: 新用户消息 }
```

客户端不会扫描或 replay 旧 history；服务端续接 predecessor chain，且调用方不应手工 prune。旧输入 token 仍会计费。Response object 的保存期与 `store` 设置受 API storage/retention 语义约束；服务端如何从 chain 选择、展开旧 items 未公开。`previous_response_id` 也不意味着旧 `instructions` 自动继承，调用方仍须发送本轮需要的 instructions。[1][2]

### 3.4 OpenAI Conversations 与 xAI Context Compaction

- **OpenAI Conversations**：应用持久化/恢复的是 `conv_id`。新请求携带 `conversation = conv_id`，API prepend conversation items，并将本轮 input/output 加回 conversation。它提供服务端会话容器，但不意味着单次上下文窗口无限大。[2]
- **xAI Context Compaction**：应用持久化/恢复的是单个 opaque `encrypted_content` item。重启后，将该完整 item 原样放在新 user turn 前。carrier 的内部内容不可读；如果应用还要审计原文，需另存自己的记录。[3]

## 4. 有 session resume UX，但内部恢复算法未公开的 CLI

### 4.1 Claude Code：恢复 session 与 startup context，JSONL-to-prompt 算法未公开

证据等级：**官方产品文档**，不是可执行源码。[8]

在一个已 compact 的 Claude Code session 上，公开可验证的 resume 步骤是：

```text
1. 用户选 --continue、--resume 或 /resume，定位 session。
2. Claude Code 从 ~/.claude/projects/<project>/<session-id>.jsonl 及 checkpoint/snapshot
   恢复文档列出的 conversation history、模型（有例外）、部分 permission mode、
   active goal、未过期 scheduled task 和 checkpoint。
3. 标准 settings 会重新读取。
4. 后续 turn 使用已 compact 的会话上下文，并重新注入 system prompt、
   root CLAUDE.md、unscoped rules 和 auto memory。
5. 新用户消息进入该恢复后的会话上下文，继续下一轮。
```

sessions 文档称 resume 恢复 full history（含 tool calls/results），同时 context-window 文档说明已 compact 的 active conversation 已由 structured summary 代替 verbatim conversation。两者能同时成立：**持久层有完整 transcript，模型工作层是 compact summary 加重新加载的 startup context。**

公开资料没有 JSONL 如何逐项转换为最终 request、summary payload、tail 选择或完整 prompt 顺序。path-scoped rules 和 nested `CLAUDE.md` 不会自动重注入，早期详细 instructions 可能丢失；这些是 resume 后 active context 与完整 transcript 不同的已知边界。[8]

### 4.2 Grok Build：恢复已保存 session，compact state 的载入方式未公开

证据等级：**官方产品文档**。[9]

```text
1. session store 已保存在 ~/.grok/sessions/，按 working directory 归属；
   公开列出的内容为 prompts、responses、tool calls、file snapshots。
2. 用户用 grok --resume <session-id> 恢复指定 session；无 ID 的 --resume 或 -c
   恢复当前目录最近 session；TUI 也提供 /resume。
3. fork / rewind 可改变恢复的 branch 或 file snapshot 起点。
4. 后续 turn 从恢复后的 compacted conversation 继续。
```

公开资料没有 session schema、compact payload/blob、tail 规则、规则/工具重新加载方式、wire payload 或最终 prompt assembly。因此可以确认 Grok Build 会保存并恢复会话，但不能说明它在内存中如何把已 compact transcript 变成下一次模型请求，更不能用同厂 xAI API 的 `encrypted_content` 格式来填补这个未知。[9]

## 5. resume 后，哪些旧内容还在，哪些进入第一条请求

| 路径 | resume 时读什么 | 第一条恢复后请求使用什么 | 更早原始记录的状态 |
| --- | --- | --- | --- |
| Codex | 全 rollout JSONL；反扫最后 `replacement_history` checkpoint 和 suffix；恢复 runtime state | checkpoint history + suffix + 当前 instructions/tools/runtime + 新消息 | checkpoint 前 raw rollout 留在 JSONL，默认不进 history。 |
| OpenHands | `base_state.json`、EventLog、active branch | rebuilt View + 新 `MessageEvent`，当前 tools 单独传入 | forgotten/non-convertible/non-active-branch events 留在 store，默认不进 messages。 |
| Aider | Markdown history（仅在显式 restore 时） | 重新解析/重建的 `done_messages` + 当前 repo/system context + 新消息 | Markdown 留存；工具段默认不进入 restored history。 |
| OpenAI input-array | 应用自己保存的 latest compact item + tail | item + tail + 新消息 | 是否有完整 raw log 取决于应用是否另存。 |
| OpenAI standalone | 应用保存的整个 `compacted.output` | 完整 output + 新消息，原样 | audit 原记录由应用另存。 |
| OpenAI previous ID | latest response ID | ID + 新消息；服务端解析 chain | 服务端内部保留/展开受 API storage/retention 语义约束，细节未公开。 |
| Claude Code | session JSONL、checkpoint 和公开 runtime state | compact session context + 重新加载的 startup context + 新消息 | transcript 存在；JSONL-to-prompt 和按问题回读算法未公开。 |
| Grok Build | 保存的 session | 已恢复 compacted conversation + 新消息 | session 内容范围已公开，具体 active-context 载入未知。 |

这张表说明三种不同保证：

- **继续任务**：所有路径都用较小的 active context 或服务端 continuation state 继续，而不是把无限历史再次塞进窗口。
- **追溯旧原话**：Codex、OpenHands、Aider 虽保留更早原文，但其公开默认 resume 路径没有“读取新问题后自动检索旧原文并回填”的步骤；Claude Code/Grok Build 的内部是否有该步骤未公开，不能断言有或无。
- **核验外部副作用**：持久 tool trace/summary 只能帮助恢复上下文；它不能证明外部世界在 resume 时仍保持旧结果，仍可能需要重新查询。

## 6. 已知与未知的严格边界

- **可由公开源码证明**：Codex 的“最后 checkpoint 加 tail replay”、OpenHands 的“active branch 重建 View”、Aider 的“Markdown 重新解析并重新建立 working history”。[4][6][7]
- **可由 API 合同证明**：OpenAI/xAI 在重启后应保存/传回哪一种 carrier、window 或 ID，以及哪些项允许裁剪。[1][2][3]
- **只可由产品文档证明**：Claude Code、Grok Build 的 session 保存与 resume UX；其内部 history-to-prompt assembler 仍未公开。[8][9]
- **不能据此声称**：摘要/opaque carrier 与完整历史对所有未来问题语义等价；旧工具结果仍代表当前外部世界；未公开 CLI 一定会或一定不会自动检索旧原文。

## 参考资料

以下资料均于 2026-07-21 访问。

1. OpenAI, [Compaction guide](https://developers.openai.com/api/docs/guides/compaction), [server-side compaction](https://developers.openai.com/api/docs/guides/compaction#server-side-compaction), and [standalone compact endpoint](https://developers.openai.com/api/docs/guides/compaction#standalone-compact-endpoint).
2. OpenAI, [Conversation state](https://developers.openai.com/api/docs/guides/conversation-state) and [Create a response](https://developers.openai.com/api/docs/api-reference/responses/create).
3. xAI, [Context Compaction](https://docs.x.ai/developers/advanced-api-usage/context-compaction).
4. OpenAI Codex source at fixed commit `3bc49e1721ef0453d695950dc67bd2f0616c4883`: [record and persist](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/session/mod.rs#L2780-L2858), [canonical JSONL store](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/thread-store/src/local/mod.rs#L31-L48), [checkpoint state persistence](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/session/mod.rs#L3042-L3085), [resume state install](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/session/mod.rs#L1284-L1419), [rollout reconstruction scan](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/session/rollout_reconstruction.rs#L112-L295), [checkpoint/tail replay](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/session/rollout_reconstruction.rs#L317-L371), [world-state replay](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/session/rollout_reconstruction.rs#L389-L440), [current-context injection](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/session/mod.rs#L3541-L3638), [turn assembly](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/session/turn.rs#L144-L290), [Prompt fields](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/session/turn.rs#L1088-L1160), and [for-prompt normalization](https://github.com/openai/codex/blob/3bc49e1721ef0453d695950dc67bd2f0616c4883/codex-rs/core/src/context_manager/history.rs#L139-L146).
5. OpenAI, [Codex App Server: start or resume a thread](https://learn.chatgpt.com/docs/app-server#start-or-resume-a-thread).
6. OpenHands source at fixed commit `68aa583ebf07efefbb9219b63859c1aacecaf7b3`: [state snapshot save](https://github.com/OpenHands/software-agent-sdk/blob/68aa583ebf07efefbb9219b63859c1aacecaf7b3/openhands-sdk/openhands/sdk/conversation/state.py#L420-L443), [event-file write](https://github.com/OpenHands/software-agent-sdk/blob/68aa583ebf07efefbb9219b63859c1aacecaf7b3/openhands-sdk/openhands/sdk/conversation/event_store.py#L184-L217), [ConversationState resume](https://github.com/OpenHands/software-agent-sdk/blob/68aa583ebf07efefbb9219b63859c1aacecaf7b3/openhands-sdk/openhands/sdk/conversation/state.py#L500-L553), [active branch](https://github.com/OpenHands/software-agent-sdk/blob/68aa583ebf07efefbb9219b63859c1aacecaf7b3/openhands-sdk/openhands/sdk/conversation/state.py#L263-L302), [View reconstruction](https://github.com/OpenHands/software-agent-sdk/blob/68aa583ebf07efefbb9219b63859c1aacecaf7b3/openhands-sdk/openhands/sdk/context/view/view.py#L111-L159), [Condensation apply](https://github.com/OpenHands/software-agent-sdk/blob/68aa583ebf07efefbb9219b63859c1aacecaf7b3/openhands-sdk/openhands/sdk/event/condenser.py#L83-L96), [message-to-completion](https://github.com/OpenHands/software-agent-sdk/blob/68aa583ebf07efefbb9219b63859c1aacecaf7b3/openhands-sdk/openhands/sdk/agent/utils.py#L548-L600), [completion call](https://github.com/OpenHands/software-agent-sdk/blob/68aa583ebf07efefbb9219b63859c1aacecaf7b3/openhands-sdk/openhands/sdk/agent/agent.py#L645-L697), and [system-prompt restore behavior](https://github.com/OpenHands/software-agent-sdk/blob/68aa583ebf07efefbb9219b63859c1aacecaf7b3/openhands-sdk/openhands/sdk/agent/agent.py#L476-L522).
7. Aider source at fixed commit `5dc9490bb35f9729ef2c95d00a19ccd30c26339c`: [history writes](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/io.py#L966-L1000), [tool/output writes](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/io.py#L1117-L1136), [restore option](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/args.py#L221-L293), [Markdown parser](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/utils.py#L148-L194), [head/tail summarization](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/history.py#L27-L123), [restore start](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/base_coder.py#L519-L523), [summary start/end](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/base_coder.py#L1002-L1034), and [prompt chunks](https://github.com/Aider-AI/aider/blob/5dc9490bb35f9729ef2c95d00a19ccd30c26339c/aider/coders/base_coder.py#L1278-L1280).
8. Anthropic, [Manage sessions](https://code.claude.com/docs/en/sessions), [What survives compaction](https://code.claude.com/docs/en/context-window#what-survives-compaction), [How the context window works](https://code.claude.com/docs/en/how-claude-code-works#the-context-window), and [Memory after compact](https://code.claude.com/docs/en/memory#instructions-seem-lost-after-%2Fcompact).
9. xAI, [Grok Build sessions](https://docs.x.ai/build/features/sessions) and [Grok Build CLI reference](https://docs.x.ai/build/cli/reference).
