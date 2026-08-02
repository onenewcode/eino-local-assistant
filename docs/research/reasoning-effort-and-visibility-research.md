# 大模型思考强度控制与 Agent 思考展示：业界实践

> 状态：业界调研笔记，不是实现方案。  
> 调研日期：2026-07-16。采用前请复核原文；模型能力、参数档位与 CLI 行为变化很快。  
>  
> **范围**：大模型 API 如何控制推理资源；coding agent 如何在会话、任务和步骤级应用这些控制；Agent UI 如何展示推理摘要、计划、进度和工具轨迹。  
> **不在范围**：训练阶段的 test-time compute 算法、本仓库现状审计、本仓库落地方案、提示词泄露或绕过厂商推理保护的方法。

## 1. 摘要

1. **“思考强度”已经从单一 token 上限演进为模型校准的 effort 档位。** OpenAI、Anthropic、Gemini 都提供低到高的离散档位；Anthropic 与 Gemini 还保留固定 token budget 或自动/自适应模式。档位名相同不代表跨模型、跨厂商具有相同 token 数或质量增益。
2. **Agent 需要分开管理三类预算**：单次模型调用的推理强度、整轮 agent 的步骤/费用/时间预算、最终回答的长度或 verbosity。它们解决不同问题，不能互相替代。
3. **合理默认正转向“自动/中档 + 按任务升级”。** 简单检索、格式化和局部修改使用较低 effort；复杂调试、架构决策和跨文件变更升到高 effort；超高档不适合作为无差别默认，因为延迟、成本和过度思考会增加。
4. **成熟 Agent 不把原始私有推理链当作主界面。** 主界面通常展示简短 reasoning summary、当前阶段、计划/checklist、工具调用与结果；更详细的 transcript 放在可展开视图。原始 reasoning 若存在，也通常默认关闭、折叠、脱敏或只以不透明签名延续上下文。
5. **“思考展示”必须是事件模型，而不是一段不断增长的日志文本。** 至少区分 reasoning summary、assistant commentary、plan、tool start/progress/result、approval、final、usage，才能支持折叠、恢复、审计和流式 UI。
6. **推理摘要不是可验证的完整思维链。** 它适合解释方向、报告进度和帮助调试，但事实可信度仍应来自工具结果、引用、测试和最终产物，而不是“看起来很合理”的内心独白。

## 2. 问题边界

### 2.1 四个经常被混淆的控制面

| 控制面 | 控制什么 | 常见参数或机制 | 不能替代什么 |
| --- | --- | --- | --- |
| 模型推理强度 | 单次模型调用愿意投入多少推理资源 | `reasoning.effort`、`output_config.effort`、`thinking_level`、`budget_tokens` | Agent 总步骤、总费用、工具权限 |
| Agent 运行预算 | 一次任务能迭代多久、调用多少次模型/工具 | `max_turns`、step limit、wall-clock、cost budget、cancel | 单次调用内部推理深度 |
| 回答表现 | 最终文本多长、多详细、何种格式 | verbosity、`max_output_tokens`、structured output | 推理质量和工具循环 |
| 可观察性 | 用户看到哪些过程、怎样折叠和审计 | summary、plan、status、tool events、transcript | 模型实际投入的推理资源 |

一个常见错误是把 `max_output_tokens` 当作“思考预算”。它通常约束可见输出上限；推理模型可能另计 reasoning tokens，Agent 还可能进行多次模型调用。因此，面向 Agent 的预算至少应形成下面的层级：

```text
task budget
├── wall-clock / cost / max agent turns
├── per-turn model effort
├── tool execution budget and permissions
└── visible response length / verbosity
```

### 2.2 “显示思考过程”至少有五种不同含义

| UI 内容 | 性质 | 是否适合默认展示 |
| --- | --- | --- |
| 当前状态：正在检索、修改、测试 | Runtime 确定性状态 | 是，短句展示 |
| 计划/checklist | Agent 显式产物，可更新 | 是，复杂任务展示 |
| Reasoning summary | 模型生成的推理摘要 | 可展示，但应可折叠 |
| 工具轨迹与结果 | Runtime 事件，可审计 | 摘要默认展示，详情可展开 |
| 原始 reasoning / chain-of-thought | 模型内部或供应商返回的推理内容 | 不宜作为默认产品契约 |

本文所说的“可见思考”优先指 **摘要、计划、进度和动作证据**，而不是承诺提供完整、逐 token、忠实的私有思维链。

## 3. API 层如何控制思考强度

### 3.1 OpenAI：effort 与 summary 分离

OpenAI Responses API 的生成类型定义把两个维度分开：

- `reasoning.effort`：约束推理投入；当前 SDK 类型列出 `none`、`minimal`、`low`、`medium`、`high`、`xhigh`、`max`，但并非每个模型支持全部值。
- `reasoning.summary`：请求 `auto`、`concise` 或 `detailed` 的推理摘要。
- Reasoning item 还可包含 `summary`、可选 `content`、`encrypted_content` 和 `status`。不透明加密内容用于跨轮延续，不等于应直接展示给用户。

这体现了一个重要分层：**投入多少推理资源**与**向调用方展示多少推理信息**是两个独立旋钮。

Codex 的公开实现进一步表明客户端不应硬编码一个全局档位集合：

- `ModelPreset` 携带 `default_reasoning_effort` 和模型自己的 `supported_reasoning_efforts`。
- `ReasoningEffort` 接受已知档位，也接受未来的自定义字符串，避免客户端遇到新档位即失效。
- 配置分别包含 `model_reasoning_effort`、`plan_mode_reasoning_effort`、`model_reasoning_summary`，说明计划态可以有不同的推理预算。

**观察**：模型目录/能力协商比“所有模型共用 low/medium/high”更稳健。UI 应只展示当前模型实际支持的档位，并明确当前生效值。

### 3.2 Anthropic：adaptive effort 与固定 budget 并存

Anthropic 当前类型定义提供两套控制方式：

- `output_config.effort`：`low`、`medium`、`high`、`xhigh`、`max`。
- `thinking.type = adaptive`：模型按步骤复杂度自行决定是否以及思考多少。
- `thinking.type = enabled` + `budget_tokens`：固定 thinking token 预算；SDK 注明至少为 1024，且小于 `max_tokens`。
- `thinking.display`：`summarized` 或 `omitted`；省略显示时仍可返回签名维持多轮连续性。

Claude Code 把这些能力产品化为多层入口：

- `/effort`、`--effort`、设置文件和环境变量均可控制 effort。
- skill 和 subagent frontmatter 可以覆盖会话 effort，说明 **角色/子任务级预算**是一等能力。
- 当前 effort 显示在 logo/spinner 附近，用户无需打开设置即可确认生效档位。
- 官方文档明确指出同名 effort 是按模型校准的；`max` 可能收益递减并导致 overthinking，不建议未经评测全面启用。
- 对支持 adaptive reasoning 的模型，effort 是主要控制；较老模型仍可回退到 `MAX_THINKING_TOKENS` 固定预算。

**观察**：从固定 token 数转向自适应 effort，降低了应用层替每一步猜 token 数的负担；固定 budget 仍适合需要硬成本边界或旧模型兼容的场景。

### 3.3 Gemini：thinking level、budget 与动态模式

Google Gen AI SDK 的公开类型显示：

- `thinking_level`：`MINIMAL`、`LOW`、`MEDIUM`、`HIGH`。
- `thinking_budget`：`0` 表示关闭，`-1` 表示自动，正数表示 token 预算；允许范围依模型而异。
- `include_thoughts`：请求在响应中包含可用的 thought 内容。
- `thought_signature`：用于后续请求复用的不透明签名。

Google 的类型注释也显示新模型逐步偏向 `thinking_level`，固定 `thinking_budget` 更像模型代际相关能力。这与 OpenAI/Anthropic 的方向一致：**面向用户提供语义档位，面向兼容或精确约束保留数值预算。**

### 3.4 跨厂商对比

| 系统 | 语义档位 | 固定 token budget | 自动/自适应 | 可见推理控制 | 连续性载体 |
| --- | --- | --- | --- | --- | --- |
| OpenAI Responses | `none` 到 `max`，依模型支持 | API 主路径不以固定 budget 为统一接口 | 模型/档位相关 | `summary=auto/concise/detailed` | reasoning item / encrypted content |
| Anthropic Messages | `low` 到 `max` | `budget_tokens` | `thinking.type=adaptive` | `display=summarized/omitted` | thinking signature |
| Gemini API | `MINIMAL` 到 `HIGH` | `thinking_budget` | `-1` 自动 | `include_thoughts` | thought signature |

不能据此做的推断：

- `high` 在三家之间不是等价性能或成本。
- 固定 8k thinking tokens 不等于某个厂商的 `high`。
- 开启 summary 不会自动提高 reasoning effort。
- 关闭可见 thought 不代表模型没有推理，也不代表不计费。

## 4. Agent 如何应用思考强度

### 4.1 会话默认 + 单轮覆盖 + 子任务覆盖

成熟 coding agent 正形成三层优先级：

```text
model capability/default
        ↓
session or project preference
        ↓
turn / mode / skill / subagent override
```

代表行为：

- Codex 模型目录提供模型默认值和可用 effort 列表，并允许 plan mode 使用单独 effort。
- Claude Code 支持会话 `/effort`、启动参数、持久设置，以及 skill/subagent frontmatter 覆盖。
- Aider 以 `--reasoning-effort` 和 `--thinking-tokens` 直接透传不同供应商的两类控制，并在启动信息中显示生效的 reasoning effort 或 thinking token 数。

**合理模式**：用户选择的是“本任务偏快/均衡/深入”，runtime 再映射为当前模型支持的参数；provider-specific 数值作为高级设置，而不是基础 UX。

### 4.2 按任务阶段动态分配

一个 Agent turn 可能包含探索、计划、修改、验证和总结。每个阶段的价值曲线不同：

| 阶段 | 常见合理 effort | 原因 |
| --- | --- | --- |
| 列目录、读取已知文件、格式转换 | 低 | 主要受工具 I/O 支配，深推理收益小 |
| 搜索根因、制定多文件方案 | 中到高 | 需要整合证据和权衡路径 |
| 高风险修改、复杂调试、审查 | 高 | 错误代价高，值得增加推理投入 |
| 执行确定性测试、等待命令 | 低或不调用模型 | Runtime 可直接处理 |
| 根据失败决定下一步 | 中到高 | 需要解释新证据并调整计划 |
| 最终摘要 | 低到中 | 重点是忠实压缩已有证据，不应重新发散 |

这不是要求每一步都切档。频繁切换会增加实现和可解释性成本。更实用的策略是：

1. 会话以自动或中档开始。
2. 遇到高复杂度/高风险阶段时提升。
3. 工具执行和确定性搬运不额外消耗高 effort。
4. 连续失败、反复修改或证据冲突时升级，而不是无限增加 agent turns。
5. 达到任务预算前主动降级为“汇总现状、指出阻塞”，不要把硬停伪装成完成。

### 4.3 升级信号与降级信号

**可用于升级 effort 的信号**（业界归纳）：

- 多文件、多模块或跨系统依赖。
- 需求存在冲突约束，必须比较替代方案。
- 首次方案被测试或工具证据否定。
- 高副作用操作、生产事故、安全/权限边界。
- 需要长链数学、逻辑或架构推理。

**可用于保持低 effort 的信号**：

- 工具参数和下一步确定，模型只需发起调用。
- 机械性编辑、格式化、固定模板生成。
- 已有计划明确，当前只执行一个可验证步骤。
- 请求对延迟敏感，错误可快速回滚。

**不宜作为唯一升级信号**：prompt 长度、用户写了“仔细想”、工具调用次数。长 prompt 可能只是日志，工具多也可能只是并行读取；更好的信号是任务不确定性、风险与失败反馈。

### 4.4 双预算而不是单预算

Agent 至少需要同时限制：

```text
quality knob: per-call reasoning effort
safety knob: max turns / time / cost
```

只提高 effort 不能防止 agent 循环失控；只限制 turns 也不能保证每一步决策质量。一个高 effort、低 turn 上限的 Agent 适合昂贵决策；一个低 effort、较多 turn 的 Agent 适合大量可快速验证的探索。产品应把这两个维度分别记录和观测。

## 5. Agent 如何显示思考过程

### 5.1 Codex：摘要默认可见，原始 reasoning 单独开关

Codex 公开配置区分：

- `hide_agent_reasoning`：隐藏 `AgentReasoning` 事件，默认 `false`，即摘要型 reasoning 事件通常可见。
- `show_raw_agent_reasoning`：显示 raw reasoning content，默认 `false`。
- `model_reasoning_summary`：单独控制请求的 reasoning summary。

这种“双开关”结构很有代表性：**展示经过产品化的 reasoning 事件**与**暴露原始 reasoning 内容**不是同一能力。公开实现还把工具开始/结束、文件修改、搜索、审批等建模为独立事件，因此 UI 不必把所有过程塞进 reasoning 文本。

### 5.2 Claude Code：默认折叠，详细 transcript 按需展开

Claude Code 当前文档表现出两层渐进披露：

- Thinking output 默认折叠；开启后以弱化样式展示。
- `Ctrl+O` 打开 transcript viewer，查看更详细的工具使用、执行信息、时间戳和模型；重复 MCP 调用默认还会折叠成一行摘要。
- 主界面另有 spinner、task checklist、状态区和当前 effort 标识。

这说明 CLI 的主路径目标不是“实时滚动所有内部文字”，而是让用户回答四个问题：

1. Agent 现在还活着吗？
2. 它当前在做什么？
3. 它已经执行了哪些可验证动作？
4. 我是否需要审批、打断或纠偏？

详细 transcript 是调试和审计入口，不应淹没正常对话。

### 5.3 Aider：可见 reasoning 与最终内容分流

Aider 的公开实现兼容 `message.reasoning_content` / `message.reasoning`，并用独立 reasoning tag 格式化后显示；在后续处理前又能移除 reasoning content，避免将其当作最终编辑内容。它也在模型启动信息中显示 `reasoning <level>` 或 `<n> think tokens`。

这里有两个可复用的机制：

- **配置回显**：用户始终知道当前使用的 reasoning 设置。
- **内容分流**：reasoning 只用于展示/上下文，不混入最终补丁、提交信息或结构化输出。

### 5.4 OpenHands：Think 是事件，UI 可选择隐藏某类事件

OpenHands 前端的公开事件过滤代码把 action、observation、agent state 分开，并明确不渲染 `think` observation。即使底层事件流保留思考类事件，聊天 UI 也可以选择隐藏它，同时继续展示命令、观察结果和状态变化。

这支持一个更一般的结论：**先保存语义事件，再由不同视图决定可见性**，优于在生成阶段把所有内容拼成一段不可逆 transcript。

### 5.5 推荐的展示层级（行业综合）

| 层级 | 默认内容 | 交互 |
| --- | --- | --- |
| L0：忙碌状态 | “正在检索相关实现”“正在运行测试” | 始终可见，可中断 |
| L1：里程碑/评论 | 1–3 句说明发现、假设变化、下一步 | 默认可见，频率受控 |
| L2：计划与工具摘要 | checklist、文件/命令/搜索摘要、成功失败 | 默认可见或半折叠 |
| L3：推理摘要 | provider summary 或 Agent 主动总结 | 默认折叠，可展开 |
| L4：详细 transcript | 完整工具输入输出、时间戳、事件元数据 | 专门查看器/导出 |
| L5：raw reasoning | 仅供应商和政策允许时的调试能力 | 默认关闭，不作为稳定契约 |

主界面建议优先展示 **L0–L2**。这些信息来自 runtime 或明确产物，通常比自由生成的“我正在想……”更可靠。L3 用于解释复杂决策，L4 用于诊断，L5 不应成为普通用户理解 Agent 的前提。

## 6. 事件与数据模型

### 6.1 不要只有 `thinking: string`

一个可恢复、可审计、可多端渲染的 Agent 至少需要下列事件类型：

```text
turn.started
reasoning.summary.delta / completed
assistant.commentary.delta / completed
plan.created / plan.updated
tool.started / tool.progress / tool.completed / tool.failed
approval.requested / resolved
usage.updated
turn.completed / interrupted / failed
```

每个事件应携带稳定的 `turn_id`、`item_id`、时间、状态和可选父子关系。这样可以：

- 流式更新而不重复打印。
- 折叠同一工具的多次进度。
- 恢复会话后重建 UI。
- 区分模型摘要、Agent 评论和工具事实。
- 在取消时明确哪些动作已经完成。

### 6.2 展示内容的可信度分级

| 内容 | 证据等级 | UI 措辞 |
| --- | --- | --- |
| 工具已成功、测试通过、文件已写 | Runtime 已确认 | 陈述事实 |
| 正在执行某工具 | Runtime in-progress | “正在……” |
| Agent 计划下一步 | 可变意图 | “计划……” |
| 模型推理摘要 | 生成式解释 | “判断/考虑……” |
| 未经工具验证的外部事实 | 弱证据 | 标明待核对或给引用 |

不要让 reasoning summary 使用与工具结果相同的视觉权重。否则用户会把模型自述误认为已验证事实。

### 6.3 流式节流

“显示思考”最常见的 UX 失败不是信息不足，而是刷新过密。合理策略是：

- 按语义边界更新，而不是逐 token 把 reasoning 打到终端。
- 短步骤只显示 spinner/status，不生成额外 commentary。
- 长步骤在 30–60 秒或关键发现处发里程碑更新。
- 相同工具的多次调用聚合显示，失败或审批再自动展开。
- 最终回答自包含，不要求用户回看所有过程消息。

## 7. 高效、合理的产品模式

### 7.1 基础模式：Auto / Fast / Deep

对普通用户，比暴露供应商全部档位更稳定的方式是三档意图：

| 产品档位 | 语义 | 可能映射 |
| --- | --- | --- |
| Auto / Balanced | 默认；按复杂度分配 | provider default / medium / adaptive |
| Fast | 优先延迟和成本 | minimal / low / 较小 budget |
| Deep | 优先复杂任务质量 | high / xhigh；同时仍受任务预算约束 |

高级界面再显示原始 provider 档位和 token budget。映射必须由模型 capability 决定，而不是全局常量。

### 7.2 配置必须回显

主流 CLI 的共同好处是用户能看到生效状态。一个合理界面至少回显：

```text
Model: <model>
Reasoning: Auto | Low | High
Task budget: <turn/time/cost limit if set>
Visibility: summaries | compact | transcript
```

如果模型不支持所选档位，应降级并明确提示，不能静默假装生效。

### 7.3 默认展示“工作证据”，不是“心理活动”

高价值展示顺序通常是：

1. 已理解的任务目标。
2. 当前阶段与计划。
3. 工具动作和关键结果。
4. 因新证据产生的决策变化。
5. 最终结果与验证。

相比之下，逐步展示“我先想 A，再想 B”会增加噪音、诱发用户过度信任，也可能暴露提示词、敏感上下文或无关中间猜测。

### 7.4 评测应按任务与档位做 Pareto 比较

选择默认 effort 时，不应只比较最终准确率。至少记录：

- 任务成功率和回归率。
- 首 token、首工具、总完成延迟。
- reasoning tokens、总 tokens、费用。
- agent turns、工具调用数、重复调用率。
- 用户中断/纠偏次数。
- 高档位相对中档的边际收益。

目标是在质量、延迟和成本之间找 Pareto 前沿，而不是假设“越高越好”。

## 8. 常见陷阱与反模式

### 8.1 把 raw chain-of-thought 当成稳定 API

问题：供应商可能只返回摘要、脱敏块或不透明签名；模型升级后格式也会变化。依赖 raw reasoning 做业务逻辑、审计结论或自动评分会非常脆弱。

更合理：业务控制依赖结构化 tool call、状态、stop reason 和验证结果；reasoning summary 只作为辅助说明。

### 8.2 把 effort 档位硬编码为跨模型常量

问题：不同模型支持不同档位，同名档位也按模型校准；新模型会新增档位。

更合理：从模型目录或 capability 获取支持列表、默认值和描述，未知值向前兼容。

### 8.3 所有 Agent 步骤都使用最高 effort

问题：工具 I/O 和机械步骤几乎不受益，却增加延迟和费用；超高 effort 还可能过度分析、延迟行动。

更合理：自动/中档默认，在高不确定性、高风险或失败后升级。

### 8.4 只有 effort，没有整轮预算

问题：即使单次调用是 low，Agent 仍可能无限循环；即使是 high，也可能反复执行错误策略。

更合理：同时设置 turns/time/cost/cancel 边界，并输出明确终止原因。

### 8.5 把 reasoning、commentary、tool output 混成一个流

问题：无法折叠、恢复、审计或区分事实与猜测，最终内容也容易污染补丁和结构化结果。

更合理：使用类型化事件和独立 channel，渲染层再组合。

### 8.6 用高频“思考中”文字制造透明感

问题：大量生成式状态并不等于真实进度，反而让用户错过审批、失败和关键发现。

更合理：状态来自 runtime；评论只在里程碑或方向变化时发出；长输出聚合到 transcript。

### 8.7 把摘要当作忠实审计记录

问题：reasoning summary 是模型生成内容，可能遗漏、重构或合理化中间路径。

更合理：审计使用工具调用、参数、结果、文件 diff、测试、权限决策和时间戳；摘要只做导航。

## 9. 开放问题

1. 各厂商对 reasoning token 的计费、缓存和上下文复用细节仍在快速变化，需要按目标模型逐一复核。
2. “自动 effort”是否能稳定识别 coding agent 中的高风险步骤，公开资料缺少跨模型、跨仓库的统一基准。
3. Provider summary 的忠实度和可重复性没有形成统一标准，不宜承担合规审计职责。
4. 多 Agent 场景中，父 Agent 与 subagent 的 effort 继承、总预算分摊和 UI 聚合仍缺少行业统一语义。
5. Raw reasoning 的可用性受模型、账号、API 与政策影响；任何产品设计都应允许它完全缺失。
6. 本次环境无法稳定访问部分 OpenAI/Gemini 文档页面，相关 API 字段以官方生成 SDK 类型和公开实现交叉核对；采用前应再次读取目标模型当日官方文档。

## References

### OpenAI / Codex

- OpenAI, [Reasoning guide](https://platform.openai.com/docs/guides/reasoning), accessed 2026-07-16（本次环境页面访问受限，使用下列官方 SDK 类型交叉核对）。
- OpenAI Python SDK, [`Reasoning` request type](https://github.com/openai/openai-python/blob/main/src/openai/types/shared_params/reasoning.py), accessed 2026-07-16.
- OpenAI Python SDK, [`ResponseReasoningItem`](https://github.com/openai/openai-python/blob/main/src/openai/types/responses/response_reasoning_item.py), accessed 2026-07-16.
- OpenAI Codex, [`ReasoningEffort` and model capability metadata](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/openai_models.rs), accessed 2026-07-16.
- OpenAI Codex, [reasoning visibility and effort configuration](https://github.com/openai/codex/blob/main/codex-rs/config/src/config_toml.rs), accessed 2026-07-16.
- OpenAI Codex docs, [configuration entry](https://github.com/openai/codex/blob/main/docs/config.md), accessed 2026-07-16.

### Anthropic / Claude Code

- Anthropic, [Claude Code model configuration: effort and extended thinking](https://code.claude.com/docs/en/model-config), accessed 2026-07-16.
- Anthropic, [Claude Code interactive mode and transcript viewer](https://code.claude.com/docs/en/interactive-mode), accessed 2026-07-16.
- Anthropic Python SDK, [`ThinkingConfigEnabledParam`](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/thinking_config_enabled_param.py), accessed 2026-07-16.
- Anthropic Python SDK, [`ThinkingConfigAdaptiveParam`](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/thinking_config_adaptive_param.py), accessed 2026-07-16.
- Anthropic Python SDK, [`OutputConfigParam.effort`](https://github.com/anthropics/anthropic-sdk-python/blob/main/src/anthropic/types/output_config_param.py), accessed 2026-07-16.

### Google Gemini

- Google, [Gemini API thinking](https://ai.google.dev/gemini-api/docs/thinking), accessed 2026-07-16（本次环境连接不稳定，使用 SDK 类型交叉核对）。
- Google Gen AI Python SDK, [`ThinkingLevel`, `ThinkingConfig`, thought signatures](https://github.com/googleapis/python-genai/blob/main/google/genai/types.py), accessed 2026-07-16.

### Coding agent peers

- Aider, [Configuration options: reasoning effort and thinking tokens](https://aider.chat/docs/config/options.html), accessed 2026-07-16.
- Aider, [CLI argument definitions](https://github.com/Aider-AI/aider/blob/main/aider/args.py), accessed 2026-07-16.
- Aider, [reasoning content rendering and separation](https://github.com/Aider-AI/aider/blob/main/aider/coders/base_coder.py), accessed 2026-07-16.
- OpenHands, [frontend event visibility filtering](https://github.com/All-Hands-AI/OpenHands/blob/main/frontend/src/components/features/chat/event-content-helpers/should-render-event.ts), accessed 2026-07-16.
