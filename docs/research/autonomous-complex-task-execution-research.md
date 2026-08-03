# 复杂 Coding 任务执行：行业实践

> 状态：调研笔记，不是实现方案，也不审计当前仓库。
>
> 调研日期：2026-07-30。CLI 与产品行为变化很快，采用前应复核引用页面。
>
> 范围：复杂 coding 任务的计划、进度、运行中输入、中断、恢复和验证。
> 不在范围：某个产品的内部实现、云端调度、权限模型或具体 UI 迁移。

## 1. 摘要

- 没有通用的“任务图 slash 命令”标准。当前 Codex CLI 用 `Enter` 纠偏当前 turn、`Tab` 排队下一 turn；它有产品特定的 `/goal`、`/ps`、`/stop` 和 `/title`，但公开命令表没有 `/tasks`、`/steer` 或 `/redirect`。任务进度是可选的终端标题项（`tui.terminal_title`），不是 `/statusline` 的 footer 项。[C1][C2]
- 相同名词在产品中含义不同。Claude Code 的 `/tasks` 面向运行中的 shell / subagent，`Ctrl+T` 则显示 todo checklist；Grok Build 也把后台任务与 todo 分开。不能据此推导每个 coding agent 都应暴露 DAG 查看、取消或改范围命令。[A4][G1][G2]
- 任务/计划状态是复杂工作中的常见内部机制，但通常按需显示。Codex 的 `/goal` 是持久目标表面，不是通用验收图；Claude Code 的 Task 工具只在多步骤工作中创建、更新和读取工作项，简单请求可以跳过。[C2][A1]
- 主流默认质量门是实际的 build、test、lint、diff 或浏览器结果。独立 verifier / evaluator-optimizer 适用于高风险、开放式或难自动判定的结果，不是每一项 coding 任务都必须增加一次 LLM 审核。[A2]
- 可恢复执行应保存最小状态、控制事件和证据引用；恢复时对未完成的外部动作重新验证或重规划，而不是自动重放。这是从 durable-workflow 原则和 coding-agent 会话恢复语义得出的设计推论，不是所有 CLI 公开承诺的实现细节。[T1][A3]

## 2. 不要混淆的三层

| 层 | 用户看到什么 | 运行时可以保存什么 | 不应直接推出 |
| --- | --- | --- | --- |
| 交互层 | 普通输入、`Enter`/`Tab`、中断键、少量稳定 slash 命令、简短状态 | 队列和当前显示状态 | 每个内部状态都需要一条 slash 命令 |
| 编排层 | 可选计划、todo、子 agent、模式切换 | 任务依赖、假设、失败原因 | todo 必然等于持久 DAG 或验收合同 |
| 证据与恢复层 | 测试结果、diff、会话恢复 | 事件、快照、artifact 引用、恢复点 | 可以安全重放旧 shell / 编辑操作 |

“任务图”“后台任务”“会话”和“proof”分别属于不同层。把它们都叫作 `/tasks` 会让用户误以为一个命令同时负责计划、运行进程、质量证明和恢复。

## 3. 可观察到的产品模式

| 产品 | 运行中输入 / 中断 | 计划或任务可见性 | 机制区别 |
| --- | --- | --- | --- |
| Codex CLI | `Enter` 纠偏当前 turn，`Tab` 排队下一 turn；有 `/plan`、`/goal`、`/ps`、`/stop`、`/status` 等命令。 | `/goal` 管理持久目标；`/ps` / `/stop` 管理后台终端。没有 `/tasks`、`/steer`、`/redirect`；`/title` 的终端标题可显示 task progress，`/statusline` 不显示。 | 目标、后台进程和进度有不同表面；当前 turn 的纠偏仍是普通输入。[C1][C2] |
| Claude Code | `Ctrl+C` 或 `Esc` 中断；会话持续保存并可 resume / branch。 | `/tasks` 看运行 shell 与 subagent，`Ctrl+T` 看多步骤 checklist；TaskCreate/TaskUpdate/TaskGet/TaskList 跟踪工作项。 | 后台运行视图和计划 checklist 是两个表面，不能合并解释为规格/证明图。[A1][A3][A4] |
| Grok Build | 运行中可继续对话；后台命令可进入 tasks pane。 | `/tasks` 列后台命令、subagent、定时任务；todo 则保存计划项及其状态。 | 即使存在 `/tasks`，其语义也可能是进程监控而非规格/证明图。[G1][G2] |
| Cursor | 将 Plan Mode 作为独立的探索与计划表面。 | 公开文档将计划和实施分开，而非规定统一的 slash 控制词。 | 计划是可选工作模式，不等于要求用户操纵内部图。[U1] |

## 4. 合理的默认模式

### 4.1 让交互表面保持小

对本地 CLI，先提供普通输入、明确的中断、follow-up 队列、会话恢复与简短进度。只有当一项控制具有稳定、独立且频繁的用户意图时，才适合成为 slash 命令，例如会话、权限、模型、模式或产品特定的目标生命周期。

运行中的“纠偏”和“改范围”首先是用户自然语言的新指令；运行时可在安全点把它转为重新计划、队列或新的 turn。产品不应要求用户先判断它属于 `steer` 还是 `redirect`。Codex 把这一行为放在输入注入/排队中，同时把目标和后台进程留给不同的专用表面。[C1][C2]

### 4.2 复杂度分级，而非总是建图

| 任务形态 | 合理机制 | 通常不需要 |
| --- | --- | --- |
| 单文件、小改动、格式化 | 直接工具调用 + 相关检查 | 显式计划、持久任务状态、独立 verifier |
| 多步骤改动 | 短计划或 todo、逐步测试、简短进度 | 向用户展示完整依赖图 |
| 长运行、可暂停、跨进程恢复 | 结构化状态、事件记录、artifact 引用、恢复后检查 | 自动重放未完成副作用 |
| 高风险或验收难自动化 | 额外 review、独立复现或人工确认 | 只相信实施者的自然语言总结 |

Anthropic 的公开建议是先用简单、可组合模式，并仅在可测量地改善结果时增加复杂度；coding agent 的优势之一正是能把测试结果作为反馈。[A2]

### 4.3 证据优先，验证分级

1. 先运行与改动相关的确定性检查：测试、build、lint、静态检查、迁移或浏览器流程。
2. 把失败的实际输出带回下一轮决策；不要以“已完成”的文本替代结果。
3. 对安全、跨模块、大范围重构或主观验收，再增加独立 review / verifier；其 finding 应包含可复现步骤或待验证检查。

这比“每个节点必须由第二个 LLM 盖章”更符合成本、延迟和可验证性的主流取舍。[A2]

### 4.4 持久化的边界

成熟产品公开的恢复能力主要是会话/转录恢复：Claude Code 保存本地会话并在 resume 时带回会话状态；Codex 提供会话 resume 与 fork。[A3][C2] 对确实需要 crash/restart 恢复的长执行，可靠系统会记录状态转换和历史，并从已记录的边界恢复；Temporal 的 replay 模型说明了为什么不能把内存中的“正在做”当作恢复真相。[T1]

合理的最小持久化集合是：稳定任务或 todo ID、状态、最近的失败/观察、已接受的证据引用、控制事件，以及到原始工具输出或 artifact 的链接。完整模型上下文、重复的巨型工具输出和运行中的进程句柄不应复制进任务快照。

恢复后的 `working` 不应被当作“仍在执行”。在没有可证明幂等性的前提下，将它转成待检查或待重规划，再读取工作区和重跑必要检查，是保守且可解释的选择。这是工程推论；公开 coding CLI 文档并未统一定义这一状态名。[T1][A3]

## 5. 常见误区

- **把内部机制逐一映射成命令。** 用户想表达的是“改成 API only”，不是学习 `steer`、`redirect` 的边界。
- **混用后台任务与计划任务。** 进程的停止、todo 的取消、规格的放弃和任务图的失效是不同状态转换。
- **把 verifier 设为无条件门槛。** 它会增加成本与循环风险；先用可复现的确定性检查。
- **把 resume 解释为重放。** 恢复转录或状态不等于安全地再次执行旧命令。
- **为很小的任务建立永久工作流。** 多数编辑只需要清晰工具、局部检查和诚实的结果说明。

## 6. 仍需复核的问题

- Codex 的快捷键和 feature-gated 命令会随版本变化；采用前应以当前官方命令页和本地 `codex --help` 为准，尤其不要把 `/title` 的 task progress 误记成 `/statusline` 项。
- 各产品的 todo/task 是否跨进程、跨设备或仅在当前 session 保存，不能由 UI 名称推断。
- “范围改变”是新需求、追加需求还是取消旧需求，只有用户文字和当前任务上下文才能决定；不存在可靠的通用关键词分类器。

## 参考资料

- [C1] OpenAI, [Prompting: steering and queuing](https://learn.chatgpt.com/docs/prompting#steering-and-queuing), accessed 2026-07-30. Codex CLI 中 `Enter` 纠偏当前 turn、`Tab` 排队下一 turn。
- [C2] OpenAI, [Developer commands - Codex](https://learn.chatgpt.com/docs/developer-commands#built-in-slash-commands), accessed 2026-07-30. 公开 slash 命令表，以及 `/statusline` 与 `/title` 的项目区别。
- [A1] Anthropic, [Todo Lists](https://code.claude.com/docs/en/agent-sdk/todo-tracking.md), accessed 2026-07-29.
- [A2] Anthropic, [Building effective agents](https://www.anthropic.com/engineering/building-effective-agents), 2024-12-19, accessed 2026-07-29.
- [A3] Anthropic, [Manage sessions](https://code.claude.com/docs/en/sessions.md), accessed 2026-07-29.
- [A4] Anthropic, [Interactive mode](https://code.claude.com/docs/en/interactive-mode.md), accessed 2026-07-29.
- [G1] xAI, [Background Tasks](https://docs.x.ai/build/features/background-tasks), accessed 2026-07-29.
- [G2] xAI, [Sessions and todos](https://docs.x.ai/build/features/sessions#todos), accessed 2026-07-29.
- [U1] Cursor, [Plan Mode](https://docs.cursor.com/en/agent/plan-mode), accessed 2026-07-29.
- [T1] Temporal, [Workflow Execution overview](https://docs.temporal.io/workflow-execution.md), accessed 2026-07-29.
