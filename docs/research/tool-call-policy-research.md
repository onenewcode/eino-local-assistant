# 工具调用策略：业界实践

> 状态：调研笔记，不是实现设计或迁移方案。
>
> 调研日期：2026-08-06。产品和预览接口变化很快，采用前应重新核验。
>
> 范围：编码 agent 对 shell / 文件工具的调用前控制、规则文件、只读识别与重复调用
> 约束，以及 turn、模型采样和工具执行的计数边界。
>
> 不在范围：各产品未公开的模型提示词、私有 sandbox 实现，以及本仓库的实现细节。

## 1. 结论

- **跨产品综合（安全模型，不是统一产品合同）：** 自然语言项目指令影响模型提出什么
  调用；显式授权规则在执行前决定 allow / prompt / deny（或等价）结果；实际执行边界
  应独立表达。公开资料不足以证明所有产品的 sandbox 细节，因此不能把这一抽象当作
  任一产品未公开安全实现的事实。
- **Codex 开源实现快照，非 UI 行为承诺：** `run_turn` 是包含多次 model sampling
  request 的循环；一次 sampling request 可以返回多个 item，其中可包含多个 function
  call。工具结果再送入下一次 sampling request。这说明一个工具调用不必等同于一个
  sampling request；不把框架内部图节点直接作为用户预算单位，是据此得出的综合建议。
- **SDK 协议旁证，不是产品实践证据：** xAI SDK 的 `max_turns` 定义为 server-side
  agentic turn 上限，且并行工具调用可在一个 turn 内发生多个调用。它不能推出 Grok
  消费端 UI、审批、规则或 sandbox 行为，也不计入本文的跨产品实践结论。
- **Codex preview README 的已记录事实：** `execpolicy` 使用 Starlark 的 argv 前缀
  规则；所有命中规则共同生效并取最严格决策。README 明确说明该命令仍处于 preview，
  不能将其视为稳定产品合同。
- **跨产品综合，且有边界：** 规则目录、加载顺序和冲突模型必须可解释；但“项目规则
  必须经显式信任”是 Codex 已公开实现的特定安全边界，不是本文已证明的行业统一规范。
- **已记录事实加综合：** Gemini 可把工具的 `readOnlyHint` 用作规则匹配条件。将只读
  提示视为减少审批摩擦的保守输入、而非用户授权或 sandbox 替代品，是安全综合，
  不是 Gemini 文档对内部执行保障的声明。
- **跨产品综合：** 模型采样、实际工具执行和重复调用检测是不同控制点。Codex 快照
  区分 sampling 与工具阻塞时间，OpenCode 单列重复调用控制；没有公开证据表明存在
  统一的跨产品 `max_steps` 术语或计数合同。

## 2. 已公开的产品与实现证据

### Codex：turn、模型采样和工具执行是不同单位

**开源实现快照，非 UI 或长期产品合同。** Codex core 的 `run_turn` 注释将 turn 描述为
一个循环：每次 sampling request，模型返回 function call 或 assistant message；单次请求
可以返回多个 item。若返回 function call，系统执行它并把输出放到下一次 sampling
request；只有纯 assistant message 才结束该 turn。其 timing 状态又把
`sampling_request_count`、sampling 时间和 `ToolBlocking` 时间分别记录。源码不说明所有
Codex 产品表面如何展示这些数字。
[Codex turn loop](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/core/src/session/turn.rs)
与 [Codex turn timing](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/core/src/turn_timing.rs)
（快照提交于 2026-08-06；访问于 2026-08-06）。

### xAI SDK：协议旁证，不是 Grok 产品行为

**SDK 协议旁证，非部署产品证据。** xAI SDK 对 `chat.create(max_turns=...)` 的说明把它
定义为 server-side tools 的最大 agentic turn 数，并明确说明：启用
`parallel_tool_calls` 时，单个 turn 可以出现多个工具调用，所以 `max_turns` 不一定等于
工具调用总数；关闭并行时，每个 response 最多一个工具调用。SDK 还描述 client-side
工具循环，以及 server-side workflow 的一个 response 可含 `tool call -> tool result ->
final answer` 多个 outputs。这些是该 SDK/API 的协议行为；本笔记没有据此断言 Grok
消费端的规则、审批、sandbox 或 UI，也没有以它支撑跨产品综合。
[xAI chat SDK](https://github.com/xai-org/xai-sdk-python/blob/4358bc235e8641ba5f0cb54599675d098385d4bf/src/xai_sdk/chat.py)
与 [xAI SDK changelog](https://github.com/xai-org/xai-sdk-python/blob/4358bc235e8641ba5f0cb54599675d098385d4bf/CHANGELOG.md)
（快照提交于 2026-07-14；访问于 2026-08-06）。

### Codex execpolicy：preview 中的前缀授权与单独安全分类

**预览 README 的已记录事实，非稳定产品合同。** README 明确说明 `execpolicy` commands
仍处于 preview，API 未来可能有 breaking changes。其当前规则语言为 Starlark：
`prefix_rule(pattern, decision?, justification?, match?, not_match?)`；pattern 的每个 argv
token 可以是字符串或字符串备选列表。`decision` 缺省为 `allow`，可用值为 `allow`、
`prompt`、`forbidden`；`match` / `not_match` 是加载时验证的示例。
[Codex execpolicy README](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/execpolicy/README.md)
（快照提交于 2026-08-06；访问于 2026-08-06）。

**预览 README 的已记录事实，非稳定产品合同。** 同一文档规定：所有匹配规则都会出现
在结果中，最终决策取严格度最高者 `forbidden > prompt > allow`；没有命中时结果不带
decision。绝对可执行路径先作精确匹配，启用 host executable resolution 时才可按 basename
回退；`host_executable(name, paths)` 可限制回退路径。
[Codex execpolicy README](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/execpolicy/README.md)
（快照提交于 2026-08-06；访问于 2026-08-06）。

**开源实现快照，非 UI 行为承诺。** Codex core 的规则加载器使用 `rules/` 目录和
`.rules` 扩展名，默认文件名为 `default.rules`；每层先按路径排序，然后按配置层由低到高
合并。该模块还把规则评估与 `is_known_safe_command` 分开：后者对普通只读命令、受限
`find`、受限 `rg`、受限 Git 子命令和受限 `sed` 等做解析后判断。
[规则加载器](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/core/src/exec_policy.rs)
和 [安全命令分类器](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/shell-command/src/command_safety/is_safe_command.rs)
（快照提交于 2026-08-06；访问于 2026-08-06）。

**开源实现快照，非 UI 行为承诺。** Codex 将项目本地 config、hooks 与 exec policy
置于同一信任门之后。用户 config 的 `[projects."<absolute path>"]` 记录只有
`trust_level = "trusted"` 才会启用项目层；未记录或显式 untrusted 的项目层会带 disabled
reason 而不参与生效配置。
[Codex project trust loader](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/config/src/loader/mod.rs)
与 [project config type](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/config/src/config_toml.rs)
（快照提交于 2026-08-06；访问于 2026-08-06）。

**安全综合。** 上述 Codex 信任门可阻止仓库内容自行扩大本地命令授权；它是一个可复用
的安全设计，不证明其他产品都采用相同项目信任模型。

**开源解析器快照，非完整产品集成证据。** Codex 解析器定义 `network_rule`；README 的
当前公开示例重点仍是前缀规则与 host executable。解析器能识别某语言，不等于任意产品
网络工具都会执行该规则，因此其最终产品表面和审批呈现仍需按目标版本复核。
[Codex parser](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/execpolicy/src/parser.rs)
（快照提交于 2026-08-06；访问于 2026-08-06）。

### Claude Code：分层权限与未证实的复杂 shell 细节

**已记录事实。** Claude Code 文档描述 `allow`、`ask`、`deny` 规则集，并明确区分
`CLAUDE.md` 的模型指导和权限系统的执行约束。公开设置示例还表明用户、项目、local
与 enterprise 等来源可以有不同的适用范围。
[Configure permissions](https://code.claude.com/docs/en/permissions)
（页面版本及发布时间未披露；访问于 2026-08-06）。

**证据缺口。** 本次固定来源没有保存可定位的一手变更记录，以证明复杂或过长 shell
输入、复合命令、重定向和内置只读命令在全部模式下的精确处理。因此本文不把这些细节
写成 Claude Code 的已记录事实；同样没有足够公开资料建立其工具调用、并行批次或模型
采样的统一 turn 计数语义。
[Configure permissions](https://code.claude.com/docs/en/permissions) 与
[Permission modes](https://code.claude.com/docs/en/permission-modes)
（页面版本及发布时间未披露；访问于 2026-08-06）。

### Gemini CLI：条件化 TOML 规则

**已记录事实。** Gemini CLI 文档描述 TOML policy rule：规则可组合工具、参数、
approval mode 和交互状态，并给出 `allow`、`deny`、`ask_user` 决策与 priority。文档
说明最高优先级的匹配规则生效，headless 中的 `ask_user` 作为拒绝处理。
[Policy engine](https://github.com/google-gemini/gemini-cli/blob/d5c9a97dc03758649da8a7b9b739c4c73b15910e/docs/reference/policy-engine.md)
（快照提交于 2026-08-06；访问于 2026-08-06）。

**已记录事实。** 同一文档要求重定向必须由规则明确允许，并警告其 workspace policy
层当前不可用。这证明一个声明的文件层级可能未参与实际加载；不证明其他产品采用相同
的加载或冲突语义。
[Policy engine](https://github.com/google-gemini/gemini-cli/blob/d5c9a97dc03758649da8a7b9b739c4c73b15910e/docs/reference/policy-engine.md)
（快照提交于 2026-08-06；访问于 2026-08-06）。

**已记录事实。** Gemini 的规则条件可匹配工具提供的 `toolAnnotations`，其示例包含
`readOnlyHint = true`。
[Policy engine](https://github.com/google-gemini/gemini-cli/blob/d5c9a97dc03758649da8a7b9b739c4c73b15910e/docs/reference/policy-engine.md)
（快照提交于 2026-08-06；访问于 2026-08-06）。

**跨产品综合。** 将只读注解作为减少审批摩擦的保守匹配输入，而非用户授权或 sandbox
替代品，是安全模型上的推论，不是该注解对实际执行保障的声明。

### OpenCode：项目覆盖与循环控制

**已记录事实。** OpenCode 支持按工具与命令模式的 `allow`、`ask`、`deny`，并说明
使用最后匹配规则；它还提供项目配置、agent 覆盖和 `external_directory` 控制。
[Permissions](https://opencode.ai/docs/permissions.md) 与
[Configuration](https://opencode.ai/docs/config.md)
（页面版本及发布时间未披露；访问于 2026-08-06）。

**已记录事实。** OpenCode 的 `doom_loop` 权限会在同一工具以相同输入重复三次后触发。
这说明其将重复调用控制作为单独权限项；不证明所有产品使用同一阈值或术语。
[Permissions](https://opencode.ai/docs/permissions.md)
（页面版本及发布时间未披露；访问于 2026-08-06）。

## 3. 机制与取舍

**跨产品综合，非逐项产品承诺。** 下表归纳前述事实与证据缺口；“收益”和“取舍”是
设计推论，不是任何单一产品的保证。SDK 旁证不作为表中跨产品依据。

| 机制 | 综合后的收益 | 主要取舍或证据边界 | 主要依据 |
| --- | --- | --- | --- |
| 用户/项目规则层 | 用户可审阅并版本化规则 | 加载顺序、失效行为和来源必须可见；显式项目信任仅有 Codex 快照证据 | Codex trust snapshot；Claude / Gemini / OpenCode 分层文档 |
| argv 级前缀匹配 | 缩小宽泛字符串匹配扩大授权面的风险 | wrapper、引号、重定向和复合 shell 仍需解析或保守回退 | Codex preview README |
| 严格度聚合 | 防止一条宽 allow 掩盖一条更窄禁止 | 与 Gemini priority、OpenCode last-match 不可直接互换 | Codex、Gemini、OpenCode 文档 |
| 内置只读分类 | 为常见检查减少不必要审批 | 选项和组合命令误判风险高，未知形式应保守 | Codex classifier snapshot；Gemini annotation matching |
| 独立的 turn / sampling / tool 计数 | 可分别限制模型循环、实际副作用次数和总时长 | 术语并非跨产品统一；SDK 定义不代表产品 UX | Codex turn snapshot；OpenCode `doom_loop` |
| 独立循环/预算控制 | 避免前提缺失或拒绝后无界重试 | 固定阈值会中止部分合理调试，需说明终止原因 | OpenCode `doom_loop`；综合 |
| 独立执行边界 | 将可达范围和后果与授权规则分开表达 | 各产品 sandbox 的具体强制语义未公开，不能据本文断言 | 证据缺口；安全综合 |

## 4. 跨产品综合

**跨产品综合，非统一产品术语或实现合同。** 可复用的抽象是四个相邻但不能互相替代的层：

1. **模型指导：** 任务说明、项目约定和工具说明影响模型提出的调用。
2. **授权规则：** 对已解析调用应用明确的 allow / prompt / forbidden（或等价）决策，
   并暴露命中依据。
3. **影响分类：** 只在能够证明时将命令视为只读；它服务于交互和状态管理，而非直接
   扩大授权。
4. **执行边界（规范性抽象）：** sandbox、cwd/path 钳制、网络限制和不可逆操作检测
   可以约束实际执行；本文没有把各产品未公开的实现细节当作事实。

**跨产品综合。** 用于可观测性和资源上限的计数应保持独立：一个 **turn** 可被产品定义为
从用户输入到最终回答或终止的一次 agent 工作单元；一个 **model step** 可定义为一次可
请求工具的模型采样；一个 **tool call** 是一次实际工具执行。单个模型采样可以提出零到
多个工具调用，多个调用也可以并行。Codex 开源快照显示 turn loop/timing 将 sampling 与
工具阻塞分开，OpenCode 将相同输入的重复调用另列 `doom_loop`；据此可以综合出，不应把
工具执行、审批等待、重试和框架编排节点偷换成同一种“步骤”。xAI SDK 的 `max_turns`
仅作协议旁证，不支撑此跨产品结论。预算命中时应报告具体维度，而不是把含糊的
`max_steps` 解释成工具调用数。

**跨产品综合。** 不同产品在规则冲突模型上并不统一：Codex preview 规则的严格度聚合、
Gemini 的 priority 和 OpenCode 的 last-match 都不可直接互换。因此不存在脱离产品语义的
“一份行业标准配置文件”；采用某产品规则语言时应同时采用其完整匹配语义与版本边界。

## 5. 风险与证据缺口

- **证据缺口：** 公开文档和源码不足以证明任意产品的全部 UI、服务端审批或 sandbox
  实现；本文不据此推断未公开的内部保障。
- **证据缺口：** 没有足够的一手公开材料证明“首次启动时把完整默认规则复制到用户目录”
  是跨产品标准实践；这类初始化策略应被标注为具体产品的部署选择，而非行业事实。
- **已记录事实与风险：** Codex preview README 中 `prefix_rule` 的决策只有 `allow`、
  `prompt`、`forbidden`；将产品自定义的 `impact` 字段或 `ask` 决策写入该函数会破坏
  该 preview 语言的兼容性。`network_rule` 的 `deny` 必须按该函数自己的语义核验。
  [Codex execpolicy README](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/execpolicy/README.md)
  与 [Codex parser](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/execpolicy/src/parser.rs)
  （快照提交于 2026-08-06；访问于 2026-08-06）。
- **综合风险：** 只对 shell 字符串首段做前缀判断，会把 `safe && unsafe` 错当成安全；
  解析不够时应要求审批或拒绝。
- **综合风险：** 以框架图节点或每一个 tool call 代替模型采样预算，会使一次合理的并行
  工具批次消耗不稳定的“步骤”数量，并可能在最终回答前耗尽预算。
- **已记录事实加安全综合：** Codex 快照要求项目层受用户侧 trust gate 控制；将规则来源
  保持可见、避免工具在当前执行中随意改写它，是由该边界得出的安全建议，不是已证明的
  通用行业语义。
  [Codex project trust loader](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/config/src/loader/mod.rs)
  （快照提交于 2026-08-06；访问于 2026-08-06）。
- **证据缺口：** Codex 的 `network_rule` 已在当前开源解析器出现，但其每个产品表面对
  网络工具、代理和审批的最终集成未由 preview README 完整说明。
  [Codex parser](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/execpolicy/src/parser.rs)
  （快照提交于 2026-08-06；访问于 2026-08-06）。

## 来源快照元数据

| 来源集合 | 类别与发布状态 | 可复现版本或快照 | 发布/更新时间信息 | 访问日 |
| --- | --- | --- | --- | --- |
| OpenAI Codex `execpolicy`、core、config、shell-command 源码 | 开源实现快照；`execpolicy` README 标为 preview | [`openai/codex@bfb6a6e`](https://github.com/openai/codex/commit/bfb6a6ea226b4f2d710e09736bb08fd5d366db31) | 单个文件发布日期/更新时间未披露；快照提交时间 2026-08-06T12:39:11Z | 2026-08-06 |
| xAI Python SDK 源码与 changelog | SDK/API 协议旁证，不是部署产品行为 | [`xai-sdk-python@4358bc2`](https://github.com/xai-org/xai-sdk-python/commit/4358bc235e8641ba5f0cb54599675d098385d4bf) | 单个文件发布日期/更新时间未披露；快照提交时间 2026-07-14T17:02:15Z | 2026-08-06 |
| Gemini CLI policy engine 文档 | 已部署 CLI 的公开产品文档快照 | [`gemini-cli@d5c9a97`](https://github.com/google-gemini/gemini-cli/commit/d5c9a97dc03758649da8a7b9b739c4c73b15910e) | 单个文档发布日期/更新时间未披露；快照提交时间 2026-08-06T01:53:50Z | 2026-08-06 |
| Claude Code permissions / modes 文档 | 官方产品文档 | 未固定，采用前待复核 | 页面版本、发布日期和更新时间未披露 | 2026-08-06 |
| OpenCode permissions / configuration 文档 | 官方产品文档 | 未固定，采用前待复核 | 页面版本、发布日期和更新时间未披露 | 2026-08-06 |

## 参考资料

- OpenAI. [Codex execpolicy README](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/execpolicy/README.md). Preview source snapshot, accessed 2026-08-06.
- OpenAI. [Codex exec-policy loader](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/core/src/exec_policy.rs). Source snapshot, accessed 2026-08-06.
- OpenAI. [Codex execpolicy parser](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/execpolicy/src/parser.rs). Source snapshot, accessed 2026-08-06.
- OpenAI. [Codex known-safe command classifier](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/shell-command/src/command_safety/is_safe_command.rs). Source snapshot, accessed 2026-08-06.
- OpenAI. [Codex turn loop](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/core/src/session/turn.rs). Source snapshot, accessed 2026-08-06.
- OpenAI. [Codex turn timing](https://github.com/openai/codex/blob/bfb6a6ea226b4f2d710e09736bb08fd5d366db31/codex-rs/core/src/turn_timing.rs). Source snapshot, accessed 2026-08-06.
- xAI. [Chat SDK](https://github.com/xai-org/xai-sdk-python/blob/4358bc235e8641ba5f0cb54599675d098385d4bf/src/xai_sdk/chat.py). SDK source snapshot, accessed 2026-08-06.
- xAI. [Python SDK changelog](https://github.com/xai-org/xai-sdk-python/blob/4358bc235e8641ba5f0cb54599675d098385d4bf/CHANGELOG.md). SDK source snapshot, accessed 2026-08-06.
- Anthropic. [Configure permissions](https://code.claude.com/docs/en/permissions). Version/update date not disclosed, accessed 2026-08-06.
- Anthropic. [Permission modes](https://code.claude.com/docs/en/permission-modes). Version/update date not disclosed, accessed 2026-08-06.
- Google. [Gemini CLI policy engine](https://github.com/google-gemini/gemini-cli/blob/d5c9a97dc03758649da8a7b9b739c4c73b15910e/docs/reference/policy-engine.md). Source snapshot, accessed 2026-08-06.
- OpenCode. [Permissions](https://opencode.ai/docs/permissions.md). Version/update date not disclosed, accessed 2026-08-06.
- OpenCode. [Configuration](https://opencode.ai/docs/config.md). Version/update date not disclosed, accessed 2026-08-06.
