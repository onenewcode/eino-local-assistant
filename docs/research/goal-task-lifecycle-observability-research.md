# 持久 goal、todo、后台任务与运行中观察：部署式 coding agent 行业实践

> 状态：外部行业研究笔记，不是本仓库实现方案。
>
> 调研日期：2026-08-05。产品和文档会快速演进；采用前应重新核验。
>
> 决策面：部署式 coding agent 如何让用户区分“想完成什么”“列了哪些步骤”“现在是否真的在执行”，以及如何观察、停止或取消具体运行中的工作。
>
> 范围：只记录已部署 coding agent 的公开交互、公开协议/实现材料和明确的持久化边界；覆盖 goal/plan、todo/checklist、前台与后台 shell/subagent、运行中观察、停止/取消、失败和恢复。
>
> 不在范围：本仓库代码或命令设计、框架/SDK/API 的抽象比较、未公开的内部调度/存储推断、具体 UI 视觉方案和本地迁移计划。

本文把产品自述、公开源码/协议和跨产品归纳分开标注：`[事实]` 是来源直接写明或展示的行为，`[综合推论]` 是多个来源支持的产品中立结论，`[证据缺口]` 是公开材料不足以证明的部分。

## 1. 结论

- **[综合推论]** 持久 goal、plan mode、todo/checklist、live run/resource 和观察/控制不是同一个对象。goal 表示较长寿命的意图；plan 是协作或权限阶段；checklist 是进度投影；run、shell 和 subagent 才是会产生实时副作用的执行对象。
- **[事实]** Claude Code 明确把 checklist 与后台任务视图分开：`Ctrl+T` 显示 todo checklist，`/tasks` 显示运行中的 shell 和 subagent；Codex 的公开 Plan Mode 模板也明确说 Plan Mode 与 `update_plan` TODO/checklist 不同。[A1][C1]
- **[综合推论]** 最小而稳定的 CLI 可观察性缺口不是再增加一份详细日志，而是用一个紧凑的生命周期投影回答四个问题：当前对象是什么、它是否真的在运行、最近发生了什么、用户停止的范围和结果是什么。
- **[事实]** Claude Code 的恢复边界并不等于“恢复所有正在执行的资源”：活动 goal 可以随 session 恢复，未过期的 scheduled task 可以恢复，但 background Bash 和 monitor task 不恢复；subagent transcript 在 session 内单独持久化，默认 30 天清理。[A3][A2]
- **[事实]** Codex 的公开协议把中断当前 task 与清理所有后台 terminal 分成两个控制动作，并提供 turn、命令执行、subagent 和等待事件；这说明“停止 agent”不能自动解释为“停止所有子进程”。[C2]
- **[证据缺口]** 这些产品的公开材料没有形成统一的跨 session、跨进程“任务账本”承诺；尤其是崩溃后的 live process、子进程清理是否已完成、旧事件如何被拒绝，不能从状态栏或 checklist 名称推断。

## 2. 术语边界与样本

### 2.1 不应混用的对象

| 对象 | 用户含义 | 不应从它推断什么 |
| --- | --- | --- |
| goal | 一个可跨多轮工作的长期意图或目标元数据 | 有 goal 不代表当前有进程在执行。 |
| plan / plan mode | 研究、讨论、提出方案，或暂时限制变更的协作阶段 | plan 不是执行队列，也不一定是持久任务。 |
| todo / checklist | 对目标的步骤分解及完成投影 | 一个 `in_progress` 项不等于有活跃 shell/subagent。 |
| run / turn | 一次实际 agent loop 或工具执行生命周期 | run 结束不一定意味着 goal 完成；可能只是失败、取消或等待后续。 |
| background shell / subagent | 与主输入并行的执行资源或子 agent | 后台化不等于可跨重启恢复。 |
| observation / control | 对运行状态的可见投影，以及针对对象的停止/跟进动作 | 看见“stopped”不等于底层副作用已清理完。 |

### 2.2 样本选择

本次收敛到四个已经取得官方材料的部署式产品/类别，目的在于覆盖不同生命周期取舍，而不是建立厂商排名：

| 产品/类别 | 用来观察的边界 |
| --- | --- |
| Codex CLI | 明确区分 Plan Mode、TODO/checklist、goal metadata、turn/task 和后台 terminal 的本地 CLI。 |
| Claude Code | 同一 CLI 中的 checklist、前台/后台 subagent、后台 Bash、任务面板、session resume 和 stop。 |
| Gemini CLI | 把 plan 作为 approval mode，有研究、确认、接受/迭代/取消阶段的 CLI。 |
| Aider | 以 `ask`/`code`/`architect` 等对话模式区分讨论与改码，作为缺少公开后台任务账本时的对照。 |

以下比较不把某个产品的命名当作行业标准；四个样本的公开程度也不相同。

## 3. 已部署应用证据

### 3.1 Codex CLI：Plan、checklist、task 和 background terminal 是分开的控制面

**[事实] 触发与信息流。** Codex CLI 的公开 Plan Mode 模板要求先讨论和探索，再形成计划，最后才执行；模板写明 Plan Mode 持续到 developer 显式结束，用户在该模式中要求执行时仍按“规划执行”处理。[C1] 同一个模板还专门区分 Plan Mode 和 `update_plan`：后者只是 TODO/checklist 进度工具，不负责进入或退出 Plan Mode。[C1]

**[事实] 用户可见控制。** Codex 的公开协议定义了 `Interrupt`，语义是中止当前 task 但不终止后台 terminal；另有 `CleanBackgroundTerminals`，语义是有意终止该 thread 的所有运行中后台 terminal。[C2] 这两个动作的目标范围不同，不能用一个“取消”标签代替。

**[事实] 运行中观察。** 同一协议公开了 `task/turn started`、`task/turn complete`、命令开始/输出增量/结束、subagent spawn/interaction、waiting begin/end、resume begin/end 和 `SubAgentActivity` 等事件；还列出了 `Running`、`Interrupted`、`Completed`、错误、关闭等 agent 状态。[C2] 公开实现中的 `update_plan` handler 也把 plan 更新转成单独的 plan update 事件，而不是把 checklist 当作 turn 状态。[C3]

**[事实] 持久化边界。** 协议公开了“更新 thread 的 long-running goal metadata”这一独立事件类型，说明 goal metadata 与 checklist/turn 是不同信息流。[C2] 公开源码还包含 session history、resume/fork 和 rollout 相关材料，但本次使用的官方 CLI 页面/源码没有给出一个完整的用户承诺：哪些 goal、后台进程和子 agent 能跨 CLI 进程恢复。

**[事实] 失败与取消。** Codex 的协议状态至少把 interrupted、completed 和 error 分开；后台 terminal 还需要单独清理。[C2] 这支持“请求中断”“agent turn 已中断”“后台进程已终止”应是不同观察结果。

**[证据缺口]** 本次公开材料没有证明 CLI 是否把所有上述事件聚合成一个稳定的用户任务列表，也没有证明取消后每个 shell、MCP 调用或子 agent 都已经结算；更没有公开崩溃后旧事件、重复取消和工作区回收的完整语义。

### 3.2 Claude Code：明确提供 checklist 与后台任务两套观察入口

**[事实] 触发与信息流。** Claude Code 的交互文档把 `Ctrl+B` 定义为将运行中的 Bash 命令和 agent 放到后台，前台继续工作。[A1] subagent 文档进一步区分 foreground subagent（阻塞主对话直到完成）和 background subagent（并行运行，完成后以通知回到主对话）。[A2]

**[事实] 用户可见观察。** `Ctrl+T` 显示或隐藏 Claude 的 todo checklist；文档特别说明这不是后台任务视图，`/tasks` 才用于查看运行中的 shell 和 subagent。[A1] 后台 subagent 完成后会继续留在 `/tasks` 中并标记为 done，直到 session 清理任务列表；失败或被停止的 subagent 会离开列表。[A2] `Ctrl+O` 则打开更详细的 transcript，显示工具执行、时间戳和模型等信息。[A1]

**[事实] 用户可见停止/跟进。** `Ctrl+C` 中断正在运行的操作；`Esc` 可在 mid-turn 停止 response 或 tool call，同时保留已经完成的工作；`Ctrl+X Ctrl+K` 二次确认后停止当前 session 的所有后台 subagent。[A1] 对运行中的 fork，任务面板的 `x` 停止它，`Enter` 打开其 transcript 并发送 follow-up。[A2]

**[事实] 失败与恢复。** 官方文档说明，后台 subagent 遇到 API error 时标为 failed 并带回最后输出；完成的 subagent 可以被 resume，resume 会以同一 agent ID 的新 run 重新显示为 running。用户主动 stop 的 subagent 不会因为后续 `SendMessage` 自动恢复，需由用户在 transcript 面板中手动恢复。[A2]

**[事实] 持久化边界。** Claude Code session 会持续写本地 transcript，resume 会恢复会话历史及部分 session 状态；仍 active 的 goal 会随 session 带回，但 turn count、timer 和 token baseline 会重置。未过期 scheduled task 会恢复，background Bash 和 monitor task 不恢复。[A3] subagent transcript 独立于主 transcript，在 session 中可继续使用，默认 `cleanupPeriodDays` 为 30 天。[A2]

**[证据缺口]** 文档描述了“停止”与列表状态，却没有在本次材料中承诺 shell 子进程的终止屏障、已经产生的文件副作用是否可回滚，或 resume 时如何处理一个 UI 断开但 OS 进程仍活跃的旧 run。`/tasks` 的产品投影也没有公开一个跨产品通用的 stable run identity 合同。

### 3.3 Gemini CLI：plan 是确认阶段，不等于运行中任务账本

**[事实] 触发与信息流。** Gemini CLI 的官方 Plan Mode 文档把 Plan 作为 approval mode，可通过默认设置、`--approval-mode=plan`、`Shift+Tab`、`/plan` 或模型调用 `enter_plan_mode` 进入。[G1] 文档描述的流程是：模型研究并与用户讨论，必要时使用 `ask_user`，生成 Markdown 计划，用户随后选择接受编辑、手动确认、继续迭代或取消计划。[G1]

**[事实] 用户可见控制。** 进入 plan、修改计划、接受计划、继续规划和取消计划都是明确的阶段动作；接受计划才进入后续编辑流程。[G1] 这使 plan 与实际变更执行有清楚的用户确认边界。

**[证据缺口]** 已取得的 Plan Mode 页面没有公开一个与 plan item 对应的后台 shell/subagent 任务列表，也没有说明活动 goal、计划 Markdown、运行中工具和取消请求在 session 重启后的保留边界。页面也没有给出“取消已请求”与“所有工具/子进程已停止”的独立终态，因此不能把计划取消推断成运行时清理完成。

### 3.4 Aider：讨论/改码模式清楚，但运行时任务语义公开得较少

**[事实] 触发与信息流。** Aider 官方文档将 `ask`、`code`、`architect` 和 `help` 作为不同 chat mode；`ask` 用于讨论和回答问题而不修改代码，`code` 用于改码，模式可以通过 `/ask` 或 `/chat-mode` 切换。[D1]

**[事实] 可见控制边界。** 这里的主要控制是“这条输入处于讨论模式还是改码模式”，而不是一个独立的后台任务控制面。[D1] 因而它适合说明 mode 与 task 的区别：一个只读对话模式不自动等于持久 goal 或可观察 run。

**[证据缺口]** Aider 的该官方页面没有公开持久 goal、可编辑 todo/checklist、后台 shell/subagent 列表、按 run 停止、取消结算或跨重启恢复的完整合同。不能据此断言 Aider 没有这些能力，只能说它们不在本次已取得的官方材料中。

## 4. 机制与权衡

### 4.1 跨产品信息流（综合推论）

```text
用户意图
   │
   ├──► goal / plan：说明要达成什么，或当前是否仍在规划
   │          │
   │          └──► todo / checklist：把意图投影成若干进度项
   │                         │
   │                         └──► foreground run / background shell / subagent
   │                                         │
   │                                         ├──► live events / transcript / task list
   │                                         │
   │                                         └──► success / failure / stop requested / cancelled
   │
   └──► session resume：恢复部分记录，不自动等于恢复 live resource
```

**[综合推论]** 用户需要的是一条“意图到执行”的可解释链，而不是把所有状态压成 `working`：

| 层 | 最小要回答的问题 | 常见误读 |
| --- | --- | --- |
| goal / plan | 还在定义目标，还是已经允许执行？ | 有计划就以为已有运行中的任务。 |
| checklist | 哪些步骤完成、下一项是什么？ | `in_progress` 就以为 shell 还活着。 |
| run/resource | 当前到底有无 agent、shell 或 subagent 在消耗资源？ | 后台运行被误看成 session 已完成。 |
| observation | 最近一次状态变化和阻塞原因是什么？ | 静态 transcript 被误当作实时状态。 |
| stop/cancel | 停的是哪个对象，是否只是请求，是否已经结算？ | 一次 Esc/Ctrl+C 被误解成所有子进程已停。 |

### 4.2 取消是状态转换，不是一个按钮标签

**[综合推论]** 至少应区分以下语义，即使产品最终只把其中一部分直接显示给用户：

| 状态 | 含义 | 可否把它当作完成 |
| --- | --- | --- |
| `planned` / `queued` | 目标或后续工作已记录，但尚无当前执行 | 否。 |
| `running` | 当前 agent、shell 或 subagent 正在执行 | 否。 |
| `waiting` | 等待审批、输入、资源或下一阶段 | 否；需要显示原因。 |
| `stop_requested` | 用户已发出停止，底层仍可能在收尾 | 否。 |
| `cancelled` / `stopped` | 运行不再继续，但已产生的副作用仍需单独说明 | 否，不等于 goal 完成。 |
| `failed` | 运行因错误结束，可带部分输出或可恢复入口 | 否。 |
| `completed` | 这一次 run 正常结束 | 仍不自动代表整个 goal 的所有 checklist 已完成。 |

Claude Code 把失败的后台 subagent、被停止的 subagent、完成后留在任务列表的 subagent 区分开；Codex 把当前 task 中断和后台 terminal 清理拆成控制动作。[A2][C2] 这两组事实共同支持上表，但不证明任何单一产品完整采用这些状态名。

### 4.3 持久化边界的稳定表达

**[综合推论]** 对用户最有用的持久化信息不是“所有运行细节都保存”，而是明确区分：

- **记录型**：goal、plan 文本、checklist、transcript、失败摘要和最后一次控制动作，通常可随 session 或任务记录恢复。
- **实时型**：模型流、shell PID、工具句柄、审批等待和子 agent 当前执行上下文，不能仅因为 transcript 被保存就视为仍然存在。
- **重新启动型**：resume 后可能启动一个新 run，也可能只恢复对话；产品需要公开说明，而不能让用户从旧的 `running` 文本猜测。

Claude 的官方材料直接展示了“active goal 可恢复、background Bash 不恢复”的不对称边界。[A3] Gemini 和 Aider 的本次官方材料没有公开相同粒度的边界，属于证据缺口。[G1][D1]

## 5. 最小而稳定的 CLI 可观察性缺口

这一节是**产品中立的研究综合，不是本仓库实现方案**。

### 5.1 缺口定义

**[综合推论]** 各产品已经分别提供 plan、checklist、task panel、transcript 或 interrupt；较小且稳定的缺口是缺少一个跨层的紧凑投影，让用户一次看清：

1. **对象**：这是 goal、checklist、foreground run，还是后台 shell/subagent？
2. **状态**：它是 planned、running、waiting、stop requested、failed 还是已结束？
3. **活动**：最近一个可解释的事件是什么，当前是否在等待审批/输入/工具？
4. **控制与持久化**：停止会作用于哪个对象；resume 后哪些信息还在、哪些 live resource 不会回来？

Claude Code 的文档甚至主动提醒 checklist view 不是 background-task view；Codex 的公开协议则把 goal、plan、turn、命令执行和 background terminal 分成不同事件/控制面。[A1][C1][C2] 因此这不是要求展示更多内部细节，而是把已有的分层边界在 CLI 上用一个稳定摘要对齐。

### 5.2 最小摘要应包含什么

**[综合推论]** 一个足够小的摘要只需保留四类字段：

```text
对象：goal / checklist / run / background resource
状态：planned | running | waiting | stopping | failed | done
最近活动：当前工具或最后一次状态变化 + 可选原因
控制边界：停止此 run / 停止后台资源 / 仅清除 checklist
```

如果空间允许，再加一项 `持久化：recorded / live-only / resume 后需新 run`。不需要把完整事件流、模型思考或所有工具输出塞进这份摘要；详细内容继续由 transcript、任务面板或日志承载。

### 5.3 验收问题（仍是研究问题，不是实现清单）

一个 CLI 的可观察性是否填上了这个缺口，可以用下面几个用户问题检验：

- “我有一个未完成 goal，但现在到底有没有进程在跑？”
- “这个 checklist 项是模型的计划，还是正在运行的后台任务？”
- “我按停止后，停的是主 turn、某个 subagent，还是所有后台 shell？”
- “屏幕断开或 resume 后，看到的是旧记录，还是一个仍然活着的执行？”
- “失败时我能否看到失败对象、最后活动和可否恢复，而不是只有 `working` 消失？”

**[证据缺口]** 公开资料不足以证明任一产品在所有场景都能对上述问题给出完整答案；这正是需要保持小范围验证、而不应先设计庞大任务编排 UI 的原因。

## 6. Pitfalls 与证据缺口

- **不要把名称当作语义。** `goal`、`plan`、`task`、`todo` 在不同产品中可能指不同层；只有明确触发、状态和控制范围后才能比较。
- **不要把记录恢复当作执行恢复。** Claude 明确说明 background Bash 和 monitor task 不随 session resume 恢复；其他产品未公开的地方应保留 unknown。[A3]
- **不要把 stop 当作 rollback。** 已完成的编辑、shell 副作用和网络操作是否回滚，本次官方资料没有给出统一承诺。
- **不要把失败、取消和用户主动停止合并。** Claude 对 API error、完成、停止和 resume 有不同描述；Codex 也把 interrupted、error 和后台 terminal cleanup 分开。[A2][C2]
- **不要从 UI 反推内部所有权。** `/tasks` 或 transcript 能说明产品向用户展示了什么，不能证明跨进程 ownership、旧事件 fencing、进程树回收或分布式一致性。
- **版本变化风险很高。** Claude 文档中的背景任务、resume 和快捷键包含具体 v2.1.x 版本边界；Codex 使用滚动的 `main` 源码；本笔记只代表 2026-08-05 的公开材料快照。[A1][A2][C1][C2]

## References

- [C1] OpenAI, [Codex CLI Plan Mode public template](https://raw.githubusercontent.com/openai/codex/main/codex-rs/collaboration-mode-templates/templates/plan.md), rolling `main` source, accessed 2026-08-05.
- [C2] OpenAI, [Codex CLI public protocol](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/protocol.rs), rolling `main` source, accessed 2026-08-05.
- [C3] OpenAI, [Codex CLI public plan handler](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/handlers/plan.rs), rolling `main` source, accessed 2026-08-05.
- [A1] Anthropic, [Claude Code interactive mode](https://code.claude.com/docs/en/interactive-mode), rolling documentation; page includes v2.1.x behavior notes, accessed 2026-08-05.
- [A2] Anthropic, [Claude Code subagents](https://code.claude.com/docs/en/sub-agents), rolling documentation; page includes v2.1.x behavior notes, accessed 2026-08-05.
- [A3] Anthropic, [Claude Code session management](https://code.claude.com/docs/en/sessions), rolling documentation; page includes current resume/persistence behavior, accessed 2026-08-05.
- [G1] Google, [Gemini CLI Plan Mode](https://raw.githubusercontent.com/google-gemini/gemini-cli/main/docs/cli/plan-mode.md), rolling `main` documentation, accessed 2026-08-05.
- [D1] Aider, [Chat modes](https://aider.chat/docs/usage/modes.html), official product documentation; no stable publication date shown, accessed 2026-08-05.
