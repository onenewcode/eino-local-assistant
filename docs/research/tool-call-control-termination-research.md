# Agent 工具调用控制与终止：runtime 业界实践

> 状态：业界调研笔记，不是实现方案。  
> 调研日期：2026-07-16。采用前请复核原文；CLI/SDK 语义会变。  
>  
> **主轴**：成熟 agent / coding agent runtime 如何控制工具调用、如何判断继续或结束。  
> **非主轴**：厂商 Chat/Messages API 字段手册（只保留 agent 必须消费的契约背景）。  
> **不在范围**：权限策略细表、上下文压缩算法细节、本仓库落地建议。

## 1. 摘要

成熟 agent 的核心不是“把模型 API 调一遍”，而是一个 **runtime 循环**：

```text
prompt + tools + history
        │
        ▼
   model response
        │
   has tool calls? ── no ──► finalize (final text / structured output)
        │ yes
        ▼
 execute tools (permission / parallel policy / hooks)
        │
 append tool results
        │
 hard limit? ── yes ──► stop with explicit end reason
        │ no
        └──────────────► next model call
```

跨产品收敛点：

1. **软停（正常结束）**：模型产出**不再包含 tool call** 的响应。  
2. **硬停（runtime 熔断）**：`max_turns` / 图步数 / 费用预算 / 用户取消 / 审批阻塞 / 不可恢复错误。  
3. **控制工具**分两层：  
   - **模型可见层**：tools 列表、可选 `tool_choice`、并行 tool 请求；  
   - **执行层**：allow/deny/ask、sandbox、hooks、只读并行/写串行、错误回灌。  
4. **短路结束**是一等能力：`stop_on_first_tool` / `return_direct` / `ToolReturnDirectly`，用 tool 结果直接当 final。  
5. **停止原因要可区分**：success vs max_turns vs budget vs cancel；不要把硬停伪装成“模型答完了”。

## 2. 问题边界

| 问题 | 归属 | 例子 |
| --- | --- | --- |
| 模型这次要不要提 tool call？ | 模型 + 请求级选择 | tools schema、`tool_choice` |
| 提了的 tool 能不能执行？ | agent 执行门禁 | allowlist、approval、sandbox、hooks |
| 执行完后还要不要再调模型？ | agent 循环策略 | open tool calls？return_direct？ |
| 整轮何时强制停？ | agent 预算/中断 | max_turns、max_budget、cancel |
| 终态如何对外交付��� | session / result | ResultMessage / final_output / END |

本文只回答 **agent runtime** 的控制与终止；厂商 API 只解释“runtime 读什么信号”。

## 3. 统一 runtime 模型

把常见实现压成同一骨架：

```text
┌──────────────────────────────────────────────┐
│ Agent Runtime                                │
│  1) select visible tools / tool_choice       │
│  2) call model                               │
│  3) route: tool_calls ? tools : finalize     │
│  4) execute with policy                      │
│  5) budget / cancel / short-circuit checks   │
│  6) emit end_reason                          │
└──────────────────────────────────────────────┘
```

关键定义（业界常用但命名不一）：

- **Turn / step / super-step**：一次“模型决策 +（可选）工具执行”或图上的一步迁移。计数语义产品间不同，见 §6。  
- **Open tool call**：assistant 已声明 tool 请求、尚未配齐 result。  
- **Soft stop**：无 open tool call，进入 final。  
- **Hard stop**：预算/取消/错误打断，即使模型还想继续。  
- **Short-circuit stop**：某类 tool 执行后不再回模型。

## 4. 代表系统怎么做

### 4.1 Claude Code / Claude Agent SDK

一手来源：[How the agent loop works](https://code.claude.com/docs/en/agent-sdk/agent-loop)。

**循环**

1. 收 prompt（system + tools + history）  
2. 模型评估：文本、一个或多个 tool call，或两者都有  
3. SDK 执行 tools，结果回灌  
4. 重复，直到模型产出 **无 tool calls** 的响应  
5. 产出 final `AssistantMessage` + `ResultMessage`

官方定义：**turn = 一次含 tool calls 的 round trip**（模型提 tool → 执行 → 回灌）。无 tool 的最终文本响应结束循环。

**控制**

- 工具门禁：`allowed_tools` 预批准、`disallowed_tools` 硬禁、`permission_mode` 管剩余；hooks 可在执行前拦截/改写/阻止。  
- 拒绝不是静默吞掉：denied tool 以 rejection 作为 tool result 回给模型，模型可改策略。  
- 并行：只读类（Read/Glob/Grep 及 read-only MCP）可并发；改状态类（Edit/Write/Bash）串行。自定义 tool 默认串行，需 `readOnlyHint` 才并行。

**终止**

| 条件 | 行为 |
| --- | --- |
| 无 tool calls 的最终响应 | 正常结束，`ResultMessage.subtype == success` |
| `max_turns` / `maxTurns` | 限制 tool-use round trips；默认 **无上限** |
| `max_budget_usd` / `maxBudgetUsd` | 费用阈值；默认 **无上限** |
| 触达 turns/budget | `ResultMessage` 错误 subtype：`error_max_turns` / `error_max_budget_usd` |

生产向默认建议（官方表述）：open-ended 任务应设预算；不设则可能很长。

### 4.2 OpenAI Agents SDK (`Agent` + `Runner`)

一手来源：[Running agents](https://openai.github.io/openai-agents-python/running_agents/)、[Agents](https://openai.github.io/openai-agents-python/agents/)。

**循环（Runner）**

1. 用当前 agent + input 调 LLM  
2. 分支：  
   - **final output** → 结束  
   - **handoff** → 切换 agent，继续  
   - **tool calls** → 执行、追加结果，继续  
3. 超过 `max_turns` → 抛 `MaxTurnsExceeded`；`max_turns=None` 关闭限制

**Final output 规则（官方）**  
“产生期望类型的文本/结构化输出，**且没有 tool calls**”。

**控制**

- `ModelSettings.tool_choice`：`auto` / `required` / `none` / 指定工具名。  
- **`reset_tool_choice` 默认 True**：tool 调用后把 `tool_choice` 复位为 `auto`，防止 forced tool 死循环。  
- `tool_use_behavior`：  
  - `run_llm_again`（默认）：tool 结果回模型  
  - `stop_on_first_tool`：第一个 tool 输出即 final  
  - `StopAtTools([...])`：指定 tool 触发 final  
  - 自定义 `ToolsToFinalOutputFunction`  
- 执行侧：`tool_execution.max_function_tool_concurrency` 限制本地并发；`parallel_tool_calls` 控制模型是否可在**单次响应**发多 tool。  
- 未知 tool / 审批拒绝：可配置抛错或把错误回灌给模型。  
- `error_handlers["max_turns"]`：硬停时可返回受控 final，而不是只抛异常。

### 4.3 Codex CLI（coding agent harness）

一手/近一手：OpenAI 对 Codex agent loop 的公开拆解与 Codex 安全文档（[Unrolling the Codex agent loop](https://openai.com/index/unrolling-the-codex-agent-loop/)、[Agent approvals & security](https://developers.openai.com/codex/agent-approvals-security)）。本次抓取部分页面有网络限制，下列以公开设计叙述为准，**采用前需再核当前版本**。

**循环特征**

- 经典 harness：模型推理 → tool（shell/MCP/plan/web 等）→ 把 `function_call` + `function_call_output` 追加回请求 → 再推理。  
- **正常结束**：模型不再请求 tool，产出 assistant message。  
- 单次用户 turn 内可有大量 model↔tool 迭代；上下文靠 compaction 管理，而不是只靠“少调工具”。  
- **暂停 ≠ 结束**：approval / sandbox 升级会阻塞执行，等待用���；这是人机门禁，不是 final answer。

**控制**

- 两层安全：sandbox（能做什么）+ approval_policy（何时问人）。  
- 工具执行结果进入下一轮前缀，利于 prompt cache；中途改 tools/权限可能打断缓存，但不改变“有 tool call 就继续”的主循环。

**终止相关开放点**

- 公开设计强调 **assistant message 作为主停止信号** 与上下文/compaction 边界。  
- “全局 max_turns 默认值/交互态是否同等强制”在社区与版本间有差异，**不能当成跨版本保证**；无人值守路径应显式设上限并核版本。

### 4.4 LangGraph prebuilt tool-calling agent

一手来源：[`tools_condition`](https://reference.langchain.com/python/langgraph.prebuilt/tool_node/tools_condition)、[Graph API recursion limit](https://docs.langchain.com/oss/python/langgraph/graph-api)。

**循环**

- 条件路由：最后一条 `AIMessage` **有 `tool_calls` → `"tools"`**，否则 **`"__end__"`**。  
- 这是把“open tool call 判定”显式写成图边。

**终止 / 熔断**

- 图级 `recursion_limit`：限制 **super-steps**；触达抛 `GraphRecursionError`。  
- 自 1.0.6 起文档写明默认 recursion limit 为 **1000**（仍应在 invoke config 中按任务显式设置）。  
- 可用 `config["metadata"]["langgraph_step"]` 做 proactive 降级，而不是只等硬错误。  
- 工具层 `return_direct=True`（LangChain tool 语义）常用于短路：tool 输出直接作为对用户结果，少一次回模型。

### 4.5 Eino ReAct Agent

一手来源：[ReAct Agent Manual](https://www.cloudwego.io/docs/eino/core_modules/flow_integration_components/react_agent_manual/)。

**循环**

- 图节点：`ChatModel` ⇄ `Tools`；state 持有中间历史。  
- **继续直到 ChatModel 返回不含 tool call 的 message**，该 message 为 final。  
- 若配置 `ToolReturnDirectly`：Tools 后加 Branch——命中则 **直接 END**，否则回 ChatModel。

**硬停与检测**

- `MaxStep`：节点迁移一步算一步；默认约 `node_count + 2`。  
  - 一次完整循环 ≈ ChatModel + Tools = **2 steps**。  
  - 文档例子：默认 12 ≈ 最多约 6 个循环，但最后必须是无 tool 的 ChatModel，故 tool 轮次更少；要 10 个循环设 20，要 20 个循环设 40。  
- `StreamToolCallChecker`：流式下不同模型 tool call 出现时机不同（有的先 tool，有的先文本后 tool）。默认检查**第一个非空 chunk**；极端情况需扫全量 chunk，但会削弱“尽早分流”的流式收益。文档建议用 prompt 约束“要调工具就直接��出 tool，少废话”。

## 5. 控制栈：业界高效分层

把上面系统的旋钮对齐成一层栈（从上到下）：

```text
0. tools registry / per-turn 可见工具集
1. 请求级选择：tool_choice = auto | required/any | none | force(name)
2. 模型并行请求：是否允许多 tool call 同响应
3. 执行门禁：allow / deny / ask / sandbox / hooks
4. 执行并发：只读并行、写串行；max concurrency
5. 错误策略：回灌可观察错误 vs 抛错中止
6. 短路：return_direct / stop_on_first_tool / StopAtTools
7. 循环预算：max_turns | MaxStep/recursion_limit | max_budget
8. 终态与可观测：end_reason / ResultMessage.subtype / metrics
```

**高效默认（跨产品归纳，非某家口号）**

| 场景 | 合理默认 |
| --- | --- |
| 日常 coding agent | tools 全开或按模式裁剪；`tool_choice=auto`；执行门禁打开；设 turns 或费用上限 |
| 强制结构化一步 | 单步 `required`/force tool，然后 **复位 auto**（Agents SDK 默认行为） |
| 只回答不行动 | `tool_choice=none` 或卸掉副作用工具 |
| 工具结果即答案 | short-circuit（stop_on_first_tool / return_direct） |
| 无人值守 / CI | 同时设 turns + budget（若产品支持），并区分 success/error end reason |
| 高副作用 shell/写文件 | 串行执行 + 审批；不要默认与只读工具同等并行 |

## 6. 终止判定：软停 / 硬停 / 短路

### 6.1 软停（主路径）

协议无关伪代码：

```text
if has_open_structured_tool_calls(model_response):
    execute_and_continue
else:
    finalize  # final text / structured output
```

这与下列实现同构：

- Claude Agent SDK：直到 no tool calls  
- OpenAI Agents SDK：desired output type **and no tool calls**  
- LangGraph：`tools_condition` → `tools` or `__end__`  
- Eino ReAct：message without tool call → final  

**辅信号**（provider `finish_reason` / `stop_reason`）只用于截断、拒绝、服务端 pause 等，**不能单独替代 open-tool-call 检查**。

### 6.2 硬停

| 机制 | 代表 | 语义注意 |
| --- | --- | --- |
| max tool-use turns | Claude `max_turns` | 计的是 tool-use round trips；默认无限 |
| max model turns | OpenAI Agents `max_turns` | 超限异常；可 handler 收尾；可 `None` 关闭 |
| graph steps | Eino `MaxStep`；LangGraph `recursion_limit` | **不是**“工具次数”同义词；一步迁移 ≠ 一次用户可见 turn |
| cost budget | Claude `max_budget_usd` | 生产 agent 常用第二熔断 |
| human gate | Codex/Claude approval | 暂停等待，不是 success final |
| cancel / host exit | SDK interrupt / worker_shutting_down | 需保证会话可恢复、无半截 open call |

### 6.3 短路停

当产品语义是“这个 tool 的输出就是答案”时，成熟 runtime 提供显式出口，而不是再烧一轮模型：

- Agents SDK：`stop_on_first_tool` / `StopAtTools` / custom final function  
- Eino：`ToolReturnDirectly`  
- LangChain/LangGraph 生态：`return_direct`

### 6.4 终态交付

好的 runtime 把“循环停了”和“为什么停”分开：

| 终态 | 例子 |
| --- | --- |
| success | Claude `ResultMessage.subtype=success`；Agents SDK normal `final_output` |
| budget/turns exhausted | Claude `error_max_turns` / `error_max_budget_usd`；Agents `MaxTurnsExceeded`（或 handler 结果） |
| graph overflow | LangGraph `GraphRecursionError`；Eino max steps 错误 |
| interrupted / denied path | 审批取消、host shutdown；工具拒绝以 result 回灌后模型自行收束 |

另外还有会话层约束（跨产品反复出现）：

- final assistant 不应残留 open tool calls  
- tool call 与 result 必须按 **call id** 成对  
- 流式路由必须能处理“先文本后 tool calls”，否则会误 finalize

## 7. Provider API 背景（仅契约）

Agent 只需稳定理解这些输入输出，不必把厂商手册当设计中心：

| 契约 | runtime 用法 |
| --- | --- |
| `tools[]` / function declarations | 本 turn 模型可见能力 |
| `tool_choice` / function-calling mode | auto / 强制 / 禁止 / 指定 |
| 响应中的 structured tool calls | **主** continue 信号 |
| `finish_reason` / `stop_reason` | **辅**信号（截断、refusal、pause…） |
| tool result + call id 回传 | 历史成对；错误也走同一通道 |
| parallel tool 请求开关 | 模型可否同响应多 call；执行侧仍可串行 |

字段名因 OpenAI / Anthropic / Gemini 等而异，对 agent 的抽象相同：**有没有 open tool call，以及如何安全执行并回灌**。

## 8. 常见坑

1. **只信 finish_reason**：强制 tool 或兼容网关下，reason 与载荷可能不��致。  
2. **流式只看首 chunk**：先文本后 tool calls → 误 END → 历史残留 open calls。  
3. **步数语义混用**：MaxStep/recursion_limit/max_turns 计数单位不同；按“工具次数”估会错。  
4. **强制 tool_choice 不复位**：tool 结果回模型后再次被强制 call，形成死循环（Agents SDK 因此默认 reset）。  
5. **权限拒绝静默丢弃**：模型无观察，只会重复同一 tool。  
6. **把 approval 暂停当成 final**：用户体验与会话状态都会错。  
7. **只读与写操作同等并行**：写文件/shell 竞态。  
8. **硬停无 end_reason**：上层无法区分“做完了”和“被掐断了”。  
9. **无限默认用在 open-ended 生产任务**：Claude 默认 turns/budget 无限；生产应显式封顶。

## 9. 开放问题

- “turn”是否包含 compact、hook 触发的额外模型调用、纯文本重试。  
- 触达 max_turns 时：硬失败 vs 强制无工具收尾摘要，哪个 UX 更好（Agents SDK 用 error_handlers 支持后者）。  
- coding CLI 交互模式与 exec/headless 模式对预算强制是否一致。  
- server/builtin tools 与 client tools 混排时的统一 stop 语义。  
- 流式“尽早分流”与“全量扫描防误判”之间的产品权衡（Eino 文档已点出张力）。

## 10. 参考资料

1. Claude Agent SDK — How the agent loop works：https://code.claude.com/docs/en/agent-sdk/agent-loop  
2. Claude Agent SDK overview / options（`max_turns`、`max_budget_usd`、permissions）：https://code.claude.com/docs/en/agent-sdk/overview  
3. OpenAI Agents SDK — Running agents（loop、`max_turns`、final output 规则）：https://openai.github.io/openai-agents-python/running_agents/  
4. OpenAI Agents SDK — Agents（`tool_choice`、`tool_use_behavior`、`reset_tool_choice`）：https://openai.github.io/openai-agents-python/agents/  
5. LangGraph — `tools_condition`：https://reference.langchain.com/python/langgraph.prebuilt/tool_node/tools_condition  
6. LangGraph — Graph API recursion limit：https://docs.langchain.com/oss/python/langgraph/graph-api  
7. CloudWeGo Eino — ReAct Agent Manual（MaxStep、ToolReturnDirectly、StreamToolCallChecker）：https://www.cloudwego.io/docs/eino/core_modules/flow_integration_components/react_agent_manual/  
8. OpenAI — Unrolling the Codex agent loop：https://openai.com/index/unrolling-the-codex-agent-loop/  
9. OpenAI Codex — Agent approvals & security：https://developers.openai.com/codex/agent-approvals-security  

---

**一句话**：业界高效 agent 以 runtime 为中心——**有 open tool call 就执行并继续，没有就 finalize**；再用 turns/步数/预算/取消做硬停，用 return_direct 类机制做短路，用权限与并行策略管执行。厂商 API 只提供 tools 控制旋钮与响应信号，不应成为调研主体。
