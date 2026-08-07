# 沙盒下工具链可见性：业界实践

> 状态：调研笔记，不是实现设计或迁移方案。
>
> 调研日期：2026-08-07。产品、源码和沙盒后端会演进，采用前应重新核验。
>
> 范围：部署中的 coding agent 如何处理用户已安装的编译器、包管理器和诊断工具在
> shell 环境与文件系统沙盒中的可见性，以及命令不可用时的诊断、授权和降级行为。
>
> 不在范围：各产品未公开的 sandbox 内部实现、本仓库的实现细节、某个具体工具链的
> 安装教程，以及把产品行为直接转换为本仓库改造方案。

## 1. 结论

- **跨产品事实的共同方向：** 主流实现把 shell 环境策略和文件系统/网络沙盒作为相邻但
  独立的控制层。是否继承 `PATH` 不能替代“进程是否能读取或执行该路径”，反过来也
  一样；至少 Codex 和 Grok Build 的公开实现分别建模了这两层。[Codex shell environment
  policy](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/config/src/shell_environment_policy.rs)、
  [Codex exec environment](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/core/src/exec_env.rs)、
  [Grok Build shell environment policy](https://github.com/xai-org/grok-build/blob/393430ee4934bc791b0d538f304a21691c517433/crates/codegen/xai-grok-tools/src/util/shell_env_policy.rs)
  （均访问于 2026-08-07）。
- **跨产品综合：** 开发者模式的安全边界通常限制写入、网络和高风险操作，同时保留足够
  的系统/工具链读取与执行能力；仅在工作区内提供读权限、却让工具链目录和 `PATH` 同时
  消失，会把“沙盒不可见”伪装成“工具未安装”。这是基于公开配置的综合，不是所有产品
  的统一合同。[Codex permissions](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/protocol/src/permissions.rs)、
  [Grok Build sandbox guide](https://github.com/xai-org/grok-build/blob/393430ee4934bc791b0d538f304a21691c517433/crates/codegen/xai-grok-pager/docs/user-guide/18-sandbox.md)
  （访问于 2026-08-07）。
- **Codex 与 Grok Build 的已记录事实：** 默认或常用的环境策略可以保留完整环境或至少
  保留 `PATH`、`HOME`、`SHELL` 等核心变量；环境变量过滤是显式策略，而不是由文件系统
  沙盒隐式决定。Codex 的默认环境继承为 `All`，`Core` 策略明确包含这些核心变量；Grok
  Build 文档和工具代码也将默认继承与 `core`、`none`、`include_only`、`exclude`、`set`
  等策略分开表达。[Codex policy source](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/config/src/shell_environment_policy.rs)、
  [Grok Build policy source](https://github.com/xai-org/grok-build/blob/393430ee4934bc791b0d538f304a21691c517433/crates/codegen/xai-grok-tools/src/util/shell_env_policy.rs)
  （访问于 2026-08-07）。
- **跨产品综合：** 工具探测应区分至少三种结果：环境中没有解析到可执行文件、解析到
  文件但执行被文件系统/权限边界阻止、以及命令确实执行后返回非零。将三者都折叠为
  `command not found` 或 `prerequisite missing` 会让 agent 继续错误地安装、修改配置或
  重试。此项是由公开的环境/沙盒分层和诊断能力推导出的可观测性综合，不是某一产品的
  已公开错误码合同。
- **Claude Code 的已记录事实：** 其沙盒文档提供 `/sandbox` 诊断入口，展示模式、覆盖项、
  解析后的配置和依赖状态；文档还描述沙盒不可用时默认警告后以非沙盒方式运行、严格模式
  可改为直接失败，以及失败命令可在正常权限流程下尝试关闭沙盒重跑。由此可见，沙盒后端
  状态和降级策略应当是用户可见、可配置的，而不是隐藏在一次普通 shell 错误里。
  [Claude Code sandboxing](https://code.claude.com/docs/en/sandboxing.md)（访问于 2026-08-07）。
- **证据缺口：** 公开资料没有证明每个产品如何构造完整的子进程 `PATH`，也没有证明
  Claude Code 在所有模式下继承主机的完整 `PATH`；因此“主流产品一定原样复制主机环境”
  不能作为事实。可以确认的是，环境继承/过滤、沙盒可用性和用户诊断在这些产品中被作为
  可单独讨论的控制面。[Claude Code sandboxing](https://code.claude.com/docs/en/sandboxing.md)、
  [OpenCode shell tool](https://github.com/anomalyco/opencode/blob/741244b69d5c267342aef04cebff804eb615bfa6/packages/opencode/src/tool/shell.ts)
  （访问于 2026-08-07）。

## 2. 已部署应用的证据

### Codex：环境继承与执行边界分层

**开源实现快照，非长期产品合同。** Codex 的 `shell_environment_policy` 单独定义 shell
环境的继承策略。公开配置类型将默认策略表达为 `All`，并提供 `Core` 以保留
`PATH`、`HOME`、`SHELL` 等核心变量；环境策略还支持显式的变量包含、排除和设置。由此可
确认，Codex 没有把“子进程使用什么环境”交给文件系统权限层隐式决定。[Codex shell
environment policy](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/config/src/shell_environment_policy.rs)
（源码快照提交 `0bdce9f`；访问于 2026-08-07）。

**开源实现快照，非 UI 行为承诺。** Codex 的执行环境代码把工作区写入权限、只读路径、
网络相关限制和命令执行环境组合成执行配置。公开权限类型允许把执行边界描述为配置，而
不是仅靠项目说明文件告诉模型“不要写某处”。[Codex exec environment](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/core/src/exec_env.rs)
与 [Codex permissions types](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/protocol/src/permissions.rs)
（源码快照提交 `0bdce9f`；访问于 2026-08-07）。

**跨产品综合。** 环境策略和执行边界都公开命名并可观察，是“工具找不到”问题中最重要
  的可解释性线索：用户需要知道当前进程的有效 `PATH`、环境策略和沙盒状态，而不只是看到
  一个由 shell 产生的短错误。这个结论是综合，不是 Codex UI 对工具发现错误的具体承诺。

### Claude Code：诊断、工具链路径和显式降级

**产品文档事实。** Claude Code 的沙盒文档说明 `/sandbox` 可查看当前模式、覆盖项、解析
后的配置和依赖状态；文件系统与网络限制分别配置。文档还说明沙盒配置可以授予特定目录
的读取、写入或执行能力，因此工具链路径需要作为访问边界的一部分处理，而不是只修改
模型提示。[Claude Code sandboxing](https://code.claude.com/docs/en/sandboxing.md)
（访问于 2026-08-07）。

**产品文档事实。** 当沙盒不可用时，文档描述默认的警告并继续运行行为，以及
`sandbox.failIfUnavailable=true` 的严格失败行为；对于失败的沙盒命令，文档还描述可在
正常权限确认流程下以 `dangerouslyDisableSandbox` 重试。它把“后端不可用”“用户选择严格
失败”和“明确批准的降级”区分开来。[Claude Code sandboxing](https://code.claude.com/docs/en/sandboxing.md)
（访问于 2026-08-07）。

**跨产品综合。** 这种设计的关键不是默认一定降级，而是让降级策略显式可选，并在失败时
告诉用户究竟是沙盒后端问题还是命令本身问题。严格模式适合高保证环境，警告/审批后重试
适合需要本地开发工具链的交互式工作流；两者都是权限决策，不能由模型自行静默选择。

### OpenCode：进程环境与命令权限是不同检查

**开源实现快照，非完整 OS 沙盒合同。** OpenCode 的 shell 工具构造子进程环境时以
`process.env` 为基础，再合并工具或调用提供的额外环境变量。其权限文档另行描述命令的
`allow`、`ask`、`deny` 以及 `external_directory`；因此公开代码支持“进程环境”和“命令/外
部目录权限”是不同检查点，但不能据此断言 OpenCode 具有某种特定的 OS 级 sandbox。
[OpenCode shell tool](https://github.com/anomalyco/opencode/blob/741244b69d5c267342aef04cebff804eb615bfa6/packages/opencode/src/tool/shell.ts)、
  [OpenCode external-directory tool](https://github.com/anomalyco/opencode/blob/741244b69d5c267342aef04cebff804eb615bfa6/packages/opencode/src/tool/external-directory.ts)、
  [OpenCode permissions](https://opencode.ai/docs/permissions.md)（源码/文档快照提交
`741244b`；访问于 2026-08-07）。

**跨产品综合。** 继承环境可以降低“本机已安装工具不可见”的摩擦，但不能取代命令参数
解析、外部目录授权、写入保护或网络限制；把 `process.env` 直接透传也可能暴露不应传给
工具的秘密变量，所以继承应有明确的过滤策略和诊断输出。

### Grok Build：全进程沙盒与独立 shell 环境策略

**开源实现与文档快照，非长期产品合同。** Grok Build 的沙盒指南描述沙盒 profile 可以
分别声明 `read_only`、`read_write` 和 `deny`，并说明沙盒施加于整个进程，子进程继承该
边界。其 workspace profile 允许广泛读取，同时限制写入；这为编译器、解释器和包缓存等
开发依赖提供了与工作区写入不同的权限维度。[Grok Build sandbox guide](https://github.com/xai-org/grok-build/blob/393430ee4934bc791b0d538f304a21691c517433/crates/codegen/xai-grok-pager/docs/user-guide/18-sandbox.md)
（源码快照提交 `393430e`；访问于 2026-08-07）。

**开源实现快照，非 UI 行为承诺。** Grok Build 的 shell environment policy 以独立策略
描述环境继承，默认值为 `all`，同时支持 `core`、`none`、`exclude`、`include_only` 和
`set`。这直接表明“沙盒允许访问什么”和“子进程看到哪些环境变量”不是同一份配置。
[Grok Build shell environment policy](https://github.com/xai-org/grok-build/blob/393430ee4934bc791b0d538f304a21691c517433/crates/codegen/xai-grok-tools/src/util/shell_env_policy.rs)
（源码快照提交 `393430e`；访问于 2026-08-07）。

**文档/实现事实。** Grok Build 还记录沙盒事件用于诊断，并将权限与安全说明作为独立的
用户可理解概念。公开材料不足以证明其每一种工具发现失败如何呈现给模型，因此本文只把
“事件可观测性”和“环境策略分层”作为已证实事实。[Grok Build permissions and safety](https://github.com/xai-org/grok-build/blob/393430ee4934bc791b0d538f304a21691c517433/crates/codegen/xai-grok-pager/docs/user-guide/22-permissions-and-safety.md)
（源码快照提交 `393430e`；访问于 2026-08-07）。

## 3. 机制与取舍

下表是对上述证据的设计层综合，不是任一产品的统一配置合同。

| 机制 | 能解决什么 | 主要取舍或边界 | 证据依据 |
| --- | --- | --- | --- |
| 独立的 shell 环境策略 | 保留或筛选 `PATH` 等变量，使子进程能解析用户工具 | 原样继承可能泄露秘密环境变量；`PATH` 可见仍不代表文件系统允许执行 | Codex、Grok Build 源码；OpenCode shell 源码 |
| 工具链目录的只读/执行访问 | 让编译器、解释器、包缓存和诊断工具在开发沙盒中可用，同时限制写入 | 工具可能依赖动态库、插件、证书或缓存目录；只允许单一可执行文件可能仍然失败 | Codex 权限源码；Claude Code、Grok Build 沙盒文档 |
| 有效环境与解析路径诊断 | 区分 `PATH` 缺失、解析失败、访问被拒和命令非零退出 | 诊断信息可能包含敏感路径或变量，需脱敏并按权限展示 | Claude Code `/sandbox`；Grok Build 沙盒事件；跨产品综合 |
| 沙盒后端状态与执行日志 | 说明沙盒是否启用、由哪个后端强制、规则是否生效 | 记录本身可能暴露策略细节；必须避免只记录模型声称而不记录实际执行结果 | Claude Code 解析配置/依赖状态；Grok Build 沙盒事件 |
| 显式审批或窄范围降级 | 工具不可见时允许用户选择授予工具链读取/执行或批准非沙盒重试 | 扩大边界会增加风险；严格模式可能牺牲开发体验，默认降级可能降低保证 | Claude Code sandbox fallback；Codex/Grok 环境与权限分层 |
| 严格失败与可解释错误 | 阻止 agent 把沙盒阻止误判为缺依赖、安装失败或代码错误 | 需要稳定的错误分类和可供模型消费的结构化字段 | Claude Code strict mode；跨产品综合 |

一个可靠的工具探测顺序可以抽象为：先记录 worker 的有效 `PATH` 和环境策略，再解析
可执行文件路径，然后尝试最小版本命令，最后把 shell 退出状态与 sandbox/permission 事件
并列保存。**这是综合建议，不是产品已公开的共同 API。** 关键是让“未解析到命令”和
“命令被执行边界阻止”成为不同状态；二者都不应自动推导为“用户没有安装”。

## 4. 跨产品综合

**综合：环境可见性是执行前提，权限是执行控制。** 模型规则、shell 环境策略、文件系统
沙盒、网络沙盒和高风险操作审批作用在不同阶段：模型规则影响模型提出什么调用；环境策略
决定子进程能看到哪些变量；文件系统/网络边界决定调用能触及什么；审批决定是否允许某个
已解析调用继续。公开证据支持把这些层分开讨论，但不支持把任一产品的内部顺序当作行业
标准。

**综合：开发沙盒应采用“窄写入、足够读取/执行”的默认取向。** Codex 与 Grok Build 的
公开资料都把工作区写入限制和系统读取/环境继承作为可分离能力；Claude Code 也把目录访问
和沙盒状态作为可配置、可诊断内容。对编译、测试和 lint 来说，隐藏所有主机工具链会破坏
开发工作流；更稳妥的安全目标是限制副作用，而不是伪造一个没有工具的主机。这个判断是
跨产品综合，不是对任何默认 profile 的永久承诺。

**综合：失败反馈必须携带“事实来源”。** 对 agent 来说，至少应能区分：

1. worker 环境中没有该命令，或有效 `PATH` 没有包含其目录；
2. 路径已解析，但沙盒/权限拒绝读取、加载依赖或执行；
3. 命令成功启动并以非零码退出；
4. 沙盒后端不可用、状态未知，或调用被审批策略终止。

把以上状态统一翻译成 `prerequisite`，会导致模型把执行环境问题当成项目依赖问题。公开
产品资料支持“分层和诊断”的方向，但没有提供跨产品统一错误枚举，因此具体分类名称仍是
产品实现选择。

**综合：降级应由用户和策略决定，不能由模型自行绕过。** Claude Code 明确公开了警告后
继续、严格失败和审批后关闭沙盒重试等不同选项；Codex 与 Grok Build 的环境/权限分层也
表明执行边界是控制面。由此可得，agent 遇到工具不可见时应报告沙盒事实并请求窄范围能力
或显式批准，而不是静默改用主机路径、安装工具或反复重试。

## 5. 陷阱与证据缺口

- **证据缺口：** 公开源码和文档不足以证明 Codex、Claude Code、OpenCode 或 Grok Build
  在所有平台、模式和启动方式下的完整 `PATH` 内容；不能据此断言某产品会无条件透传
  `/opt/homebrew/bin`、用户自定义目录或 shell 初始化脚本的结果。
- **证据缺口：** 产品通常不会公开每个沙盒后端的精确 syscall、动态库搜索路径、解释器
  启动规则或 macOS 特定文件访问错误如何映射到模型可见消息。工具可执行文件“存在”不等于
  其运行时依赖也可见。
- **综合风险：** 只修 `PATH` 而不扩展或诊断工具链的读取/执行边界，会把
  `command not found` 变成另一个更隐蔽的权限或动态链接失败；只扩展目录而不保留环境，也
  会让依赖管理器、wrapper 和用户配置不可用。
- **综合风险：** 把 `which` 的返回值、shell 的 `127`、sandbox 拒绝和项目自己的
  prerequisite 规则混为一个状态，会让模型生成错误的安装建议或错误地重排任务。
- **综合风险：** 为了解决工具可见性而完全继承环境，可能把 token、云凭据、代理密钥和
  私有配置交给不应访问它们的子进程；应使用显式过滤/包含策略并在诊断中隐藏变量值。
- **证据缺口：** 没有足够的公开一手材料证明各产品是否会在探测失败后自动请求用户批准、
  自动切换后端，或把失败重新标记为依赖缺失；这些行为必须以目标版本的实际交互或源码
  进一步核验。

## References

- OpenAI Codex. [Shell environment policy](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/config/src/shell_environment_policy.rs).
  开源源码快照提交 `0bdce9f`，访问于 2026-08-07。
- OpenAI Codex. [Execution environment](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/core/src/exec_env.rs)
  与 [permission types](https://github.com/openai/codex/blob/0bdce9f424eb9b39d7b3a8811742d10b6fbf8d54/codex-rs/protocol/src/permissions.rs).
  开源源码快照提交 `0bdce9f`，访问于 2026-08-07。
- Anthropic. [Claude Code sandboxing](https://code.claude.com/docs/en/sandboxing.md).
  官方产品文档，页面版本未披露，访问于 2026-08-07。
- Anomalyco. [OpenCode shell tool](https://github.com/anomalyco/opencode/blob/741244b69d5c267342aef04cebff804eb615bfa6/packages/opencode/src/tool/shell.ts)
  与 [external-directory tool](https://github.com/anomalyco/opencode/blob/741244b69d5c267342aef04cebff804eb615bfa6/packages/opencode/src/tool/external-directory.ts).
  开源源码快照提交 `741244b`，访问于 2026-08-07。
- OpenCode. [Permissions](https://opencode.ai/docs/permissions.md).
  官方产品文档，页面版本未披露，访问于 2026-08-07。
- xAI. [Grok Build sandbox guide](https://github.com/xai-org/grok-build/blob/393430ee4934bc791b0d538f304a21691c517433/crates/codegen/xai-grok-pager/docs/user-guide/18-sandbox.md)、
  [shell environment policy](https://github.com/xai-org/grok-build/blob/393430ee4934bc791b0d538f304a21691c517433/crates/codegen/xai-grok-tools/src/util/shell_env_policy.rs)
  与 [permissions and safety](https://github.com/xai-org/grok-build/blob/393430ee4934bc791b0d538f304a21691c517433/crates/codegen/xai-grok-pager/docs/user-guide/22-permissions-and-safety.md).
  开源源码/文档快照提交 `393430e`，访问于 2026-08-07。
