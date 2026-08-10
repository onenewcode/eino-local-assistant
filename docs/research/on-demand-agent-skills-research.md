# Agent skills 的按需发现与读取：业界实践

> 状态：调研笔记，不是实现设计或迁移方案。
>
> 调研日期：2026-08-11；产品和实现会演进，采用前应再次核验。
>
> 范围：coding-agent CLI 中项目工作流（skills）的发现、选择、上下文装载与关闭边界。
>
> 不在范围：各产品未公开的 skill 文件解析器、prompt 拼接顺序、插件安装协议，以及任何本仓库映射。

## 1. 结论

- **综合：** 可发现的 skill 目录应与实际内容读取分开。先向 agent 暴露紧凑的名称和用途，再仅为选中的工作流读取指令，能避免不相关工作流长期占用有限上下文。
- **综合：** 读取的内容是低信任的项目数据，不应成为绕过系统、项目安全规则或工具权限的控制通道。可用性与权限是不同的边界。
- **综合：** discovery 和 read 都需要资源上限：限定 workspace、可扫描的目录深度、skill 数量和单次读取字节数；超过限制时应让调用方看到截断或失败，而不是静默扩大范围。

## 2. 已部署应用证据

### Codex CLI：稳定的 skill 搜索能力

**事实（本机已部署产品观察）：** Codex CLI `0.146.0` 的 `codex features list` 将 `skill_search` 列为 `stable true`。观察日期：2026-08-11。该功能标志证明产品提供 skill-search 能力，但不公开其文件发现、内容加载或 prompt 注入的内部细节；不能据此推断这些细节。公开入口为 [Codex 文档](https://developers.openai.com/codex/) 与 [openai/codex](https://github.com/openai/codex)。

### Claude Code：skills 可解析，并有全局关闭控制

**事实（本机已部署产品观察）：** Claude Code `2.1.220` 的 `claude --help` 表示 `--disable-slash-commands` 会“Disable all skills”；`--bare` 仍说明 skills 通过 `/skill-name` 解析；`--safe-mode` 则把 skills 与项目自定义内容一起关闭。观察日期：2026-08-11。帮助输出足以说明 skills 是独立、可关闭的产品能力，但不公开其目录扫描或上下文装载顺序。官方入口为 [Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference/)。

### OpenCode：项目 skill 文档作为可复用指令入口

**事实（公开文档）：** OpenCode 的 [Skills documentation](https://opencode.ai/docs/skills/) 将 skills 描述为可复用 instructions，并说明可从项目和用户级位置发现。访问日期：2026-08-11。本文不将该文档未说明的读取大小、注入时机或权限优先级视为既定行为。

## 3. 机制与取舍

| 机制 | 用户可见效果 | 主要取舍 |
| --- | --- | --- |
| 只列名称、路径与摘要 | agent 可先判断是否相关 | 需要另一次读取调用 |
| 选中后按需读取 | 少占用默认上下文，行为可审计 | 工作流需要明显的 discover/read 契约 |
| workspace 作用域与约定目录 | 项目不会意外读取邻近或用户目录 | 需要为自定义位置提供显式配置，而非无界递归 |
| 单次读取字节上限 | 避免超大提示占满模型上下文 | 长 instruction 必须能显示截断状态 |
| 独立关闭与工具筛选 | 允许最小权限运行 | 不能把“已发现”误解成“允许执行任何指令” |

## 4. 跨产品综合

**综合：** 一个稳健的 skills 流程可分成四个可观测阶段：

1. 在受信任的项目作用域中发现候选，并返回轻量元数据；
2. 根据任务选择一个候选；
3. 在明确的大小上限内读取其内容，并保留来源与截断状态；
4. 将其作为任务上下文，而工具权限、沙箱和高优先级安全规则继续独立生效。

这种流程不会要求每次 turn 都自动加载全部 skills，也不会把技能文件当作更高优先级指令。若用户关闭 skills 或调用范围排除了相应工具，第一阶段就不应向 agent 暴露该能力。

## 5. 风险与证据缺口

- **证据缺口：** `skill_search stable true` 没有公开解释 Codex 的扫描位置、选择算法或最终 prompt 布局；任何此类推断都需要源码或官方设计文档支持。
- **证据缺口：** 本次对 Claude Code 的事实依据是已部署 CLI help，而非在不同配置和权限模式下的端到端 trace；版本升级后应重新核验关闭语义。
- **风险：** 无界递归、符号链接逃逸或把全文无条件放入每个 prompt，都会分别扩大文件访问面、引入路径边界问题或浪费上下文。
- **风险：** skill 内容可能包含与安全策略冲突的项目文本；产品必须持续强调其低优先级和现有权限边界。

## References

- Codex CLI `0.146.0`: `codex features list`，本机观察于 2026-08-11；[Codex documentation](https://developers.openai.com/codex/)；[openai/codex](https://github.com/openai/codex)。
- Claude Code `2.1.220`: `claude --help`，本机观察于 2026-08-11；[Claude Code CLI reference](https://code.claude.com/docs/en/cli-reference/)。
- OpenCode: [Skills documentation](https://opencode.ai/docs/skills/)，访问于 2026-08-11。
