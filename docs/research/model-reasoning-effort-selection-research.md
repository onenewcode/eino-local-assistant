# Coding-agent 模型与推理强度选择：业界实践

> 状态：业界研究笔记，不是实现方案。
>
> 研究日期：2026-08-05。模型目录、effort 档位、可用性和 CLI 行为变化很快；采用结论前应重新核对引用的版本。
>
> 决策面：已部署的 coding agent 如何让用户选择模型和推理强度，如何表达模型能力与默认值，以及如何处理选择的作用域、校验、回退和可见状态。
>
> 范围：交互式 picker、斜杠命令、启动参数、模型级 effort 选项、会话/任务/子 agent 作用域、自定义 provider、不可用模型和失败反馈。
>
> 不在范围：本仓库实现或设计映射、模型质量或价格基准、provider API 本身作为独立研究对象、未公开的路由/缓存内部机制，以及原始私有 chain-of-thought 的披露。

本笔记使用厂商公开的产品文档、官方产品源码和官方配置资料。它们主要是 vendor self-report，不等同于独立黑盒复现；Codex 的代码证据固定在 `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a`，其余来源按访问日记录。

## 1. Conclusions

1. **[Cross-product synthesis] 模型身份和推理强度应在选择体验中相邻、在状态模型中分离。** Codex 的模型对象携带可选 effort，picker 在选择模型后进入 effort 选择；Claude Code 把 `/model` 与 `/effort` 作为两个入口；Aider 也区分 `reasoning_effort` 与 `thinking_tokens`。这说明“选哪个模型”和“让该模型投入多少推理”是相关但不等价的控制面。[Codex model protocol][codex-model]、[Codex model popup][codex-popup]、[Claude Code model configuration][claude-model]、[Aider reasoning models][aider-reasoning]（访问日期：2026-08-05）。

2. **[Cross-product synthesis] 有效的 effort 集合属于当前模型和 provider，而不是整个 CLI 的全局枚举。** Codex 公开 `supportedReasoningEfforts` 与 `defaultReasoningEffort`；Claude Code 说明可用级别随模型变化；Aider 通过模型元数据描述接受哪些设置；Gemini 的高级配置允许直接使用模型相关的 thinking 参数并警告不兼容值可能延迟到请求时失败。统一硬编码 `low|medium|high` 会丢失能力差异和未来扩展空间。[Codex model protocol][codex-model]、[Claude Code model configuration][claude-model]、[Gemini advanced model configuration][gemini-generation]、[Aider advanced model settings][aider-settings]（访问日期：2026-08-05）。

3. **[Cross-product synthesis] “用户请求了什么”和“实际采用了什么”必须可区分。** Claude Code 公开了不支持的 effort 向不超过请求值的最高支持值回退，并在交互场景区分 requested/applied；Aider 主要在调用前警告或忽略不兼容设置；Gemini 的高级配置则明确允许值绕过最小验证直到 API runtime。产品不能因为把一个字符串发送出去，就把它标成 provider 已确认的 effective 值。[Claude Code model configuration][claude-model]、[Aider reasoning models][aider-reasoning]、[Gemini advanced model configuration][gemini-generation]（访问日期：2026-08-05）。

4. **[Cross-product synthesis] 作用域是选择的一等部分：会话默认、当前会话、当前 turn、计划模式和子 agent 不是同一件事。** Claude Code 区分保存为新会话默认值与只对当前会话生效的选择；Gemini 明确表示主会话的 `/model` 和 `--model` 不覆盖 sub-agent；Codex 的 picker 源码还体现了普通 effort 与 Plan mode effort 的不同处理路径。[Claude Code model configuration][claude-model]、[Claude Code interactive mode][claude-interactive]、[Gemini model selection][gemini-model]、[Codex model popup][codex-popup]（访问日期：2026-08-05）。

5. **[Cross-product synthesis] 有目录时应展示能力和默认值，没有目录时仍需保留明确的 opaque/custom 路径。** Claude Code 支持 provider-native deployment ID，Aider 允许用户为未知模型补充元数据，Copilot CLI 的自定义 provider 要求用户提供模型名并满足工具调用与 streaming 能力。目录 picker 和自由输入不是同一个可信度等级：前者可以提供选择提示，后者必须容纳延迟校验或 provider 错误。[Claude Code model configuration][claude-model]、[Aider advanced model settings][aider-settings]、[GitHub Copilot CLI][copilot-cli]（访问日期：2026-08-05）。

6. **[Evidence gap] 行业没有公开一个统一的模型切换契约。** 现有资料不能证明所有产品都以相同方式处理进行中的 tool call、排队消息、历史重读、effort 继承、fallback 持久化、alias 的 durable identity，或 provider 返回 effective effort 的确认。相似的 picker 外观不能替代这些未披露的语义；下文把已知事实和未知边界分开记录。相关边界见 [Claude Code sessions][claude-sessions]、[Gemini model routing][gemini-routing] 和 [Copilot supported models][copilot-models]（访问日期：2026-08-05）。

## 2. Evidence from deployed applications

本节按应用形态组织证据，而不是把某一个厂商当作行业标准。每个事实都标明证据类型和访问日期。

### 2.1 目录原生、模型后接 effort picker：OpenAI Codex CLI

**[Documented fact]** 在固定提交 `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a` 的公开协议中，`Model` 同时包含 `id`、`model`、`displayName`、`supportedReasoningEfforts` 和 `defaultReasoningEffort` 等字段；这把 provider/模型身份、展示名称和模型级能力放在不同字段中。`ReasoningEffort` 本身是开放字符串，`ReasoningEffortOption` 另外携带 effort 值和说明文字。证据：[Model protocol][codex-model]、[ReasoningEffort][codex-effort]、[ReasoningEffortOption][codex-effort-option]（访问日期：2026-08-05）。

**[Documented fact]** 同一提交的 TUI `model_popups.rs` 先显示模型选择，再针对选中的 `ModelPreset` 打开 `Select Reasoning Level`；默认值和当前值会被标记，`xhigh`、`max`、`ultra` 等高级 effort 还进入额外步骤，并附带消耗 rate limit 更快的提示。模型没有可列出的选项时，picker 仍以模型的默认 effort 作为候选，而不是制造一个全局档位表。证据：[Codex model popup][codex-popup]、[Codex model protocol][codex-model]（访问日期：2026-08-05）。

**[Documented fact]** Codex 的 popup 源码允许模型选择动作同时携带模型和 effort，并区分普通模型选择、advanced reasoning 选择以及 Plan mode 的 reasoning scope。由此可见，effort 不是只在 UI 上显示的装饰字段，而是选择动作的一部分；但这段 picker/protocol 证据本身没有证明上游 provider 已接受每一个候选值。证据：[Codex model popup][codex-popup]（访问日期：2026-08-05）。

**[Evidence gap]** 该固定源码片段没有完整说明模型切换后 provider acceptance 的回执、in-flight 请求的并发边界、所有模型身份字段在 resume 中如何合并，也不能从 picker 列表推断当前账户或自定义 endpoint 一定可用。证据边界见 [Codex model popup][codex-popup] 和 [Codex model protocol][codex-model]（访问日期：2026-08-05）。

### 2.2 会话优先、持久性分层：Anthropic Claude Code

**[Documented fact]** Claude Code 文档规定 `/model` 无参数打开 picker，带 alias 或名称时直接切换；交互 picker 的 `Enter` 会将选择保存为新会话默认值，而 `s` 只作用于当前会话。macOS 的 `Option+P` 或其他平台的 `Alt+P` 打开模型选择且不清除当前 prompt 草稿。若会话已有输出，文档说明切换后的下一次响应会重新读取完整历史，因此交互流程会要求确认这一成本边界。证据：[Claude Code model configuration][claude-model]、[Claude Code commands][claude-commands]、[Claude Code interactive mode][claude-interactive]（访问日期：2026-08-05）。

**[Documented fact]** Claude Code 将 effort 作为独立设置：支持的 level 随模型变化，文档列出 `low`、`medium`、`high`、`xhigh` 和 `max` 等级；当请求值不受当前模型支持时，会回退到“不高于请求值的最高支持级别”。组织级上限还可能进一步限制 applied effort。`/effort` 无参数打开 slider，也可以直接传值或恢复 `auto`；交互会话、settings、环境变量和 skill/subagent frontmatter 的作用域不同。证据：[Claude Code model configuration][claude-model]、[Claude Code commands][claude-commands]（访问日期：2026-08-05）。

**[Documented fact]** Claude Code 接受 alias、Anthropic 完整 model name，以及 Bedrock inference profile ARN、Google Cloud Agent Platform version name、Microsoft Foundry deployment name 等 provider-specific identity。session 文档说明通常会恢复 transcript 保存时的模型，但 provider-specific deployment ID、退休/不可用模型和显式启动 override 属于例外路径。证据：[Claude Code model configuration][claude-model]、[Claude Code sessions][claude-sessions]（访问日期：2026-08-05）。

**[Evidence gap]** 官方文档没有公开 picker 确认后的完整原子性协议：例如正在执行的 tool call 使用哪个模型、已排队消息如何绑定、失败时 picker 和 session state 的精确回滚，以及每一次 effort clamp 是否都有 provider 级别的确认信号。相关公开边界见 [Claude Code model configuration][claude-model]、[Claude Code sessions][claude-sessions]（访问日期：2026-08-05）。

### 2.3 自动路由、可用性与子 agent 分离：Gemini CLI

**[Documented fact]** Gemini CLI 的 `/model` dialog 提供 Auto（按模型家族）和 Manual（从当前可用模型中选择）两类路径，文档推荐 Auto，并说明 Pro 偏向复杂任务和 reasoning、Flash 偏向速度；`--model` 可在启动时指定具体模型，选择会作用于后续交互。文档特别提醒 `/model` 和 `--model` 不覆盖 sub-agent 使用的模型。证据：[Gemini CLI model selection][gemini-model]（访问日期：2026-08-05）。

**[Documented fact]** Gemini 的高级 Model Configuration 使用 alias 和 override，将请求的模型字符串与底层生成配置分开，并可按 agent scope 注入 `thinkingBudget` 等参数。该文档同时警告配置值只经过最小验证，不兼容的组合可能直到 API 请求时才报 runtime error。证据：[Gemini advanced model configuration][gemini-generation]（访问日期：2026-08-05）。

**[Documented fact]** Gemini CLI 的 Model Routing 文档描述了按 quota/server failure 进入 fallback 的路径：默认策略可能先征求用户同意；部分内部 utility call 使用 silent fallback，且不会修改用户配置的模型。fallback 可以只影响当前 turn，也可以影响 session remainder。启动时模型还有 `--model`、环境变量、settings、local router、默认 Auto 的优先级链。证据：[Gemini model routing][gemini-routing]、[Gemini model command source][gemini-model-command]、[Gemini fallback strategy source][gemini-fallback]（访问日期：2026-08-05）。

**[Evidence gap]** Gemini 的公开资料没有完整披露 Manual picker 的目录来源、刷新/缓存一致性、alias 与 provider deployment identity 的 durable 关系，以及每种不可用模型错误是否总能保留原配置。相关资料只描述选择和路由边界，见 [Gemini model selection][gemini-model]、[Gemini model routing][gemini-routing]（访问日期：2026-08-05）。

### 2.4 能力与 entitlement 过滤、可配置 context：GitHub Copilot CLI

**[Documented fact]** GitHub Copilot CLI 支持交互式 `/model` 选择和启动时 `--model`；官方资料说明部分模型在选择后还提供 extended context size 与 configurable reasoning level 选择，并提示更大的 context 或更高 reasoning 会消耗更多 AI credits。模型可见性还受 Copilot client、plan 和组织策略影响，因此“出现在官方支持列表”不等于“当前用户的 CLI picker 一定可选”。证据：[About GitHub Copilot CLI][copilot-cli]、[Copilot supported models][copilot-models]（访问日期：2026-08-05）。

**[Documented fact]** Copilot CLI 的自定义 provider 路径要求通过 `COPILOT_MODEL` 或 `--model` 指定模型；官方说明该模型必须支持 tool calling 和 streaming。这里的能力要求是运行入口的 gate，不是对任意 provider 自动发现完整模型能力目录。证据：[About GitHub Copilot CLI][copilot-cli]（访问日期：2026-08-05）。

**[Evidence gap]** Copilot 的公开 CLI 资料没有给出完整的 reasoning level 枚举，也没有充分说明非法模型名、不可用模型、picker 中禁用项、模型切换持久性和请求进行中切换的精确交互语义。不能把 Copilot 的 extended capability 文案推广成所有 coding agent 的共同契约。资料边界见 [About GitHub Copilot CLI][copilot-cli]、[Copilot supported models][copilot-models]（访问日期：2026-08-05）。

### 2.5 开放 provider 集合、前置兼容性警告：Aider

**[Documented fact]** Aider 同时提供 `--model`、`--reasoning-effort` 和 `--thinking-tokens`；会话内的 `/model` 用于切换主模型。官方文档把 `reasoning_effort` 与 `thinking_tokens` 视为不同 provider/model 设置：OpenAI reasoning models 常用前者，Anthropic reasoning models 常用后者，部分模型不支持可配置 reasoning。证据：[Aider reasoning models][aider-reasoning]、[Aider in-chat commands][aider-commands]（访问日期：2026-08-05）。

**[Documented fact]** Aider 的模型元数据包含 `accepts_settings`。默认检查开启时，若模型没有声明接受某个 reasoning setting，Aider 会提前警告并忽略该设置；文档还提供关闭检查、强制发送的 escape hatch，并明确警告这可能导致 API error。证据：[Aider reasoning models][aider-reasoning]、[Aider model source][aider-models]（访问日期：2026-08-05）。

**[Documented fact]** Aider 允许用户通过 model settings 文件为未知模型或自定义 provider 补充 streaming、system prompt、reasoning tag 和接受的设置等元数据。这种设计把目录缺失和能力未知显式留给用户配置，而不是假定每个自定义 endpoint 都有可靠 discovery。证据：[Aider advanced model settings][aider-settings]（访问日期：2026-08-05）。

**[Evidence gap]** Aider 的公开资料没有建立统一的模型可用性服务，也没有完整说明 `/model` 失败、请求进行中切换、alias 在 resume 中的持久形式，以及 provider 列表变化后的回滚策略。相关公开资料见 [Aider in-chat commands][aider-commands]、[Aider advanced model settings][aider-settings]（访问日期：2026-08-05）。

## 3. Mechanisms and tradeoffs

以下机制是由多个产品事实归纳出的产品中立分析，不是任何单一产品公开宣称的统一标准。

### 3.1 选择的状态链

**[Cross-product synthesis]** 一个可解释的选择状态至少可以沿着下面的链条移动：

```text
model identity / alias / deployment ID
        -> model-specific capability and default
        -> requested effort and selection scope
        -> preflight check or late provider validation
        -> applied effort, provider default, clamp, rejection, or fallback
        -> current-turn/session persistence and user-visible status
```

这条链条的价值在于不把“列表里有这个模型”“用户点了 high”“provider 接受了 high”和“本 turn 实际回退到另一个模型”压缩成一个布尔值。Codex 的协议/级联 picker、Claude 的 requested/applied 文档、Aider 的 `accepts_settings` 和 Gemini 的 late runtime warning 分别覆盖了链条的不同位置。[Codex model protocol][codex-model]、[Claude Code model configuration][claude-model]、[Aider reasoning models][aider-reasoning]、[Gemini advanced model configuration][gemini-generation]（访问日期：2026-08-05）。

### 3.2 关键机制与取舍

| 机制 | 主要收益 | 代价与边界 |
| --- | --- | --- |
| **[Cross-product synthesis] 模型级 capability list + default** | picker 可以只展示当前模型有意义的 effort，并解释默认值；新模型可以扩展新字符串 | 目录可能过时、受账户/区域/gateway 影响，列表不是 provider acceptance receipt |
| **[Cross-product synthesis] 模型后接 effort 子选择** | 用户先确定 identity，再看到与该 identity 相符的控制；避免把所有 effort 伪装成全局选项 | 多一级交互；模型切换时需要处理旧 effort 是否继续、重置、clamp 或回到 provider default |
| **[Cross-product synthesis] 开放字符串 + late validation** | 适合自定义 endpoint、provider-native deployment ID 和未来档位 | 错误可能延迟到首次请求；若没有清晰错误，用户会误以为选择成功 |
| **[Cross-product synthesis] preflight accepts-settings 检查** | 在高成本请求前发现明显不兼容，能把 warning 与配置动作关联起来 | 元数据可能错或不完整；强制 escape hatch 仍需承认 runtime failure 风险 |
| **[Cross-product synthesis] requested/applied/default 三态展示** | 可解释 clamp、组织上限和 provider default，避免虚报实际效果 | 没有 provider 或文档信号时，应用只能诚实地显示 unknown/requested，不能凭 UI 位置推导 effective |
| **[Cross-product synthesis] fallback 保持作用域边界** | quota/server failure 时提高可用性，同时不一定永久改写用户选择 | “当前 turn”“session remainder”“新默认值”影响完全不同；静默回退还可能改变成本、能力和审计结果 |
| **[Cross-product synthesis] context、reasoning、agent budget 分开** | 用户可以独立权衡输入容量、单次推理投入和整体任务预算 | 更丰富的 picker 增加认知负担；同名 level 不能直接换算为 token、质量或延迟 |

上述取舍分别受到 Codex 的模型级选项、Claude 的 clamp、Gemini 的路由/Alias、Copilot 的 extended capability 以及 Aider 的兼容性检查约束；它们是综合分析，不是任何单一产品的统一保证。[Codex model protocol][codex-model]、[Claude Code model configuration][claude-model]、[Gemini model routing][gemini-routing]、[About GitHub Copilot CLI][copilot-cli]、[Aider reasoning models][aider-reasoning]（访问日期：2026-08-05）。

### 3.3 作用域与生命周期

**[Cross-product synthesis]** 选择动作最好携带一个显式 scope，而不是让“保存”成为隐含副作用。至少存在以下边界：

| Scope | 用户期望 | 公开资料中的例子 |
| --- | --- | --- |
| provider/model default | 不指定 effort，由当前模型/provider 决定 | Claude `/effort auto`；Codex model preset 的 default effort |
| session default | 当前会话后续 turn 使用，可能影响新会话默认 | Claude picker 的 `Enter` 与 `s` 区分 |
| current turn | 只影响一次响应或一次 fallback | Gemini routing 文档描述的 current-turn fallback |
| mode/agent scope | Plan mode、skill 或 subagent 使用不同模型/effort | Codex Plan reasoning scope；Claude skill/subagent frontmatter；Gemini agent-scoped override |

这些是产品文档中的不同粒度，不能因为命令名相同就假设继承关系一致。[Claude Code model configuration][claude-model]、[Gemini model routing][gemini-routing]、[Gemini advanced model configuration][gemini-generation]、[Codex model popup][codex-popup]（访问日期：2026-08-05）。

## 4. Cross-product synthesis

1. **[Cross-product synthesis] 选择器的最小可解释单位是 `(model identity, effort mode, scope)`。** Codex 用级联 popup 表达它，Claude 用 model picker 加 effort slider/command 表达它，Aider 则把不同 provider-native setting 分开暴露；视觉上可以不同，但状态上不应把三者混为一个名称字符串。[Codex model popup][codex-popup]、[Claude Code model configuration][claude-model]、[Aider reasoning models][aider-reasoning]（访问日期：2026-08-05）。

2. **[Cross-product synthesis] 目录能力和执行校验是两个阶段。** Codex/Claude/GitHub 的可选列表可以帮助用户作选择，Aider 的 metadata 可以提前发现不兼容，Gemini 仍明确允许最小验证后的 runtime error。即使有 capability list，也仍要保留请求失败和 stale catalog 的处理边界。[Codex model protocol][codex-model]、[Claude Code model configuration][claude-model]、[Copilot supported models][copilot-models]、[Aider advanced model settings][aider-settings]、[Gemini advanced model configuration][gemini-generation]（访问日期：2026-08-05）。

3. **[Cross-product synthesis] `provider default` 应是可观察模式，而不是被猜成某个等级。** 产品资料显示模型默认值和 effort 枚举是模型相关的；把省略值统一展示成 `medium` 会掩盖 provider/model 默认改变，以及“历史上未设置”和“用户明确选择某档”之间的差异。[Codex model protocol][codex-model]、[Claude Code model configuration][claude-model]、[Gemini model selection][gemini-model]（访问日期：2026-08-05）。

4. **[Cross-product synthesis] 模型切换与 effort 选择的联动策略必须可解释。** 旧模型的 `high` 可以在新模型上不存在、被组织上限限制，或在另一个 provider 中语义不同；可选策略包括拒绝整次选择、回到 provider default、按产品规则 clamp，或保留 opaque 值并让 provider 校验。现有产品采取的策略并不一致，不能推导出单一行业默认。[Claude Code model configuration][claude-model]、[Aider reasoning models][aider-reasoning]、[Gemini advanced model configuration][gemini-generation]（访问日期：2026-08-05）。

5. **[Cross-product synthesis] 主会话的模型状态不能自动代表所有 delegated work。** Gemini 明确将 sub-agent model 与主 `/model` 分离；Claude 的 skill/subagent frontmatter 和 Codex 的 Plan mode 也表明更细粒度的覆盖是实际产品能力。状态界面若只显示主模型，应把其范围说清楚。[Gemini model selection][gemini-model]、[Gemini advanced model configuration][gemini-generation]、[Claude Code model configuration][claude-model]、[Codex model popup][codex-popup]（访问日期：2026-08-05）。

6. **[Cross-product synthesis] fallback 是可用性策略，不等于永久模型替换。** Gemini 区分用户配置模型与 utility silent fallback；其他产品的 fallback/过载处理也可能只针对当前 turn。任何产品若要把 fallback 写成新 session 默认、历史模型或下一条消息的 primary，都需要额外的持久性证据，不能从一次成功响应推断。[Gemini model routing][gemini-routing]、[Claude Code model configuration][claude-model]（访问日期：2026-08-05）。

## 5. Pitfalls and evidence gaps

- **[Evidence gap] 进行中切换的原子性未形成公开共识。** 文档通常说明 picker 或命令如何触发，却没有同时说明正在执行的 tool、已经排队的 prompt、compaction 或取消信号与新模型/effort 的绑定关系。[Claude Code interactive mode][claude-interactive]、[Gemini model selection][gemini-model]（访问日期：2026-08-05）。

- **[Evidence gap] alias、展示名和部署 ID 的长期身份关系不完整。** Claude 明确列出 provider-native deployment 的 resume 例外，Codex 协议也分开 `id`、`model` 与 `displayName`，但跨产品仍缺少统一的 transcript/event 语义来说明哪一个字段应代表历史中的实际执行模型。[Claude Code sessions][claude-sessions]、[Claude Code model configuration][claude-model]、[Codex model protocol][codex-model]（访问日期：2026-08-05）。

- **[Evidence gap] capability metadata 不等于实时 entitlement 或健康检查。** Gemini 和 Copilot 公开了可用性/策略相关行为，Aider 则支持开放 custom endpoint；公开资料没有证明一个静态列表可以覆盖账户、组织、region、gateway 和瞬时 provider health 的全部变化。[Gemini model routing][gemini-routing]、[Copilot supported models][copilot-models]、[Aider advanced model settings][aider-settings]（访问日期：2026-08-05）。

- **[Evidence gap] effective effort 的证据通常不足。** Claude 文档给出 documented clamp，Codex/Aider/Gemini 的资料分别更偏向目录、前置检查或直接传参；除非产品报告 applied 值或 provider 返回可验证信号，否则不能把 requested 值写成 effective。[Claude Code model configuration][claude-model]、[Codex model popup][codex-popup]、[Aider reasoning models][aider-reasoning]、[Gemini advanced model configuration][gemini-generation]（访问日期：2026-08-05）。

- **[Cross-product synthesis] 把 `high` 当成可移植的 token/质量/延迟承诺是危险的。** 同名 effort 由不同模型校准；Gemini 的 thinking budget、Aider 的 thinking tokens、Copilot 的 reasoning/context credit 和 Codex 的模型级 options 不是同一个度量。[Claude Code model configuration][claude-model]、[Gemini advanced model configuration][gemini-generation]、[Aider reasoning models][aider-reasoning]、[About GitHub Copilot CLI][copilot-cli]、[Codex model protocol][codex-model]（访问日期：2026-08-05）。

- **[Cross-product synthesis] 把 reasoning visibility 当成 reasoning effort 会误导用户。** effort 控制模型投入，summary/thinking/transcript 控制用户看到的过程信息；展示折叠、摘要或不展示都不能证明模型没有推理，也不能反推出实际 token 或质量。[Claude Code interactive mode][claude-interactive]、[Claude Code model configuration][claude-model]、[Codex model popup][codex-popup]（访问日期：2026-08-05）。

- **[Evidence gap] 公开来源仍不足以判断跨产品的持久化标准。** 对于 model switch、resume、fork、subagent、后台任务和 fallback，当前资料分别披露了局部行为，却没有提供一个可普遍适用的继承/回滚/审计协议。[Claude Code sessions][claude-sessions]、[Gemini model routing][gemini-routing]、[Aider in-chat commands][aider-commands]（访问日期：2026-08-05）。

## References

所有以下链接均为官方产品文档或官方源码；访问日期均为 2026-08-05。

- OpenAI Codex, [TUI model popup][codex-popup] at commit `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a`.
- OpenAI Codex, [model protocol][codex-model], [ReasoningEffort][codex-effort], and [ReasoningEffortOption][codex-effort-option] at commit `e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a`.
- Anthropic, [Claude Code model configuration][claude-model], [commands][claude-commands], [interactive mode][claude-interactive], and [sessions][claude-sessions].
- Google, [Gemini CLI model selection][gemini-model], [model routing][gemini-routing], and [advanced model configuration][gemini-generation].
- Google Gemini CLI source, [model command][gemini-model-command] and [fallback strategy][gemini-fallback].
- GitHub, [About GitHub Copilot CLI][copilot-cli] and [supported models][copilot-models].
- Aider, [reasoning models][aider-reasoning], [advanced model settings][aider-settings], [in-chat commands][aider-commands], and [model source][aider-models].

[codex-popup]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/tui/src/chatwidget/model_popups.rs
[codex-model]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server-protocol/schema/typescript/v2/Model.ts
[codex-effort]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server-protocol/schema/typescript/ReasoningEffort.ts
[codex-effort-option]: https://github.com/openai/codex/blob/e87e2b495bcf8aa1950e2bb24cc95bfdc6fd473a/codex-rs/app-server-protocol/schema/typescript/v2/ReasoningEffortOption.ts
[claude-model]: https://code.claude.com/docs/en/model-config
[claude-commands]: https://code.claude.com/docs/en/commands
[claude-interactive]: https://code.claude.com/docs/en/interactive-mode
[claude-sessions]: https://code.claude.com/docs/en/sessions
[gemini-model]: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/model.md
[gemini-routing]: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/model-routing.md
[gemini-generation]: https://github.com/google-gemini/gemini-cli/blob/main/docs/cli/generation-settings.md
[gemini-model-command]: https://github.com/google-gemini/gemini-cli/blob/main/packages/cli/src/ui/commands/modelCommand.ts
[gemini-fallback]: https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/routing/strategies/fallbackStrategy.ts
[copilot-cli]: https://docs.github.com/en/copilot/concepts/agents/copilot-cli/about-copilot-cli
[copilot-models]: https://docs.github.com/en/copilot/reference/ai-models/supported-models
[aider-reasoning]: https://aider.chat/docs/config/reasoning.html
[aider-settings]: https://aider.chat/docs/config/adv-model-settings.html
[aider-commands]: https://aider.chat/docs/usage/commands.html
[aider-models]: https://github.com/Aider-AI/aider/blob/main/aider/models.py
