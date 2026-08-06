# Plan/read-only 阶段：行业实践调研

> 状态：外部行业研究笔记，不是本仓库实现方案。
>
> 调研日期：2026-08-05。产品行为和文档会变化，采用前应重新核验。
>
> 范围：部署式 coding agent 如何把只读研究/规划阶段与真实变更执行阶段分开，以及用户如何确认、迭代或退出该阶段。
>
> 不在范围：本仓库代码审计、具体 API 设计、沙箱实现细节推断，以及把某个产品的 UI 标签当成硬安全证明。

## 1. 结论

- **[综合推论]** 只读/规划阶段与审批策略、OS sandbox 是不同维度：阶段决定当前是否允许进入变更流程，审批决定何时询问，sandbox 决定即使执行时的外部边界。
- **[综合推论]** 可验证的 plan 流程至少需要一个执行层可观察的“禁止变更”边界、一个用户可审阅的计划产物或明确确认点，以及显式的进入/退出语义。仅在 system prompt 或状态栏写“请勿修改”不足以证明只读。
- **[综合推论]** 产品在“轻量讨论 mode”和“正式 plan workflow”之间存在明显差异：Aider 的 `ask` 是持续对话模式；Codex 和 Gemini 的公开资料则描述了更强的阶段/确认流程。
- **[证据缺口]** Claude Code 的官方权限页本轮抓取超时，因此不把其 `plan` 模式的底层工具阻断、审批队列或退出细节写成已验证事实。Cursor 页面返回动态文档外壳，本笔记也不据此推断硬边界。

## 2. 已部署应用证据

### 2.1 Codex CLI

**[Documented fact]** OpenAI 的公开 Plan Mode 模板描述了严格的三阶段协作：先讨论和探索，再形成最终计划，最后才执行。模板明确写出：Plan Mode 持续到 developer 显式结束；用户在仍处于 Plan Mode 时要求执行，应被当作“规划执行”而不是实际执行；允许非变更探索，禁止变更。

来源：

- [Plan Mode (OpenAI Codex CLI public source)](https://raw.githubusercontent.com/openai/codex/main/codex-rs/collaboration-mode-templates/templates/plan.md)，访问日期 2026-08-05。

**边界**：该模板直接证明了公开协作契约，但单凭模板不能证明每个底层工具在所有发布版本中都由同一硬权限层阻断；这一点仍需以具体版本运行或更细的 handler 证据核验。

### 2.2 Gemini CLI

**[Documented fact]** Gemini CLI 的 Plan Mode 文档把 Plan 作为 approval mode，可通过默认设置、`--approval-mode=plan`、`Shift+Tab` 或 `/plan` 进入；自然语言流程也可以调用 `enter_plan_mode`。文档描述的工作流包括：模型研究并与用户讨论、使用 `ask_user` 等待确认、生成 Markdown 计划、用户选择自动或手动接受后续编辑，或继续迭代/取消计划。

来源：

- [Plan Mode (Gemini CLI)](https://raw.githubusercontent.com/google-gemini/gemini-cli/main/docs/cli/plan-mode.md)，访问日期 2026-08-05。

**边界**：流程文档清楚描述了用户确认和计划产物，但本笔记不据此断言所有工具的底层隔离实现；工具限制和 sandbox 行为仍需分别核验。

### 2.3 Aider

**[Documented fact]** Aider 官方文档区分 `code`、`ask`、`architect` 和 `help`：`ask` 讨论代码和回答问题但“不修改”，`code` 修改代码，`architect` 也会修改文件。Aider 支持单条消息的 `/ask`，也支持用 `/chat-mode <mode>` 持续切换；官方推荐先在 `/ask` 中讨论目标和方案，再切换到 `/code` 执行。

来源：

- [Chat modes (Aider)](https://aider.chat/docs/usage/modes.html)，访问日期 2026-08-05。

**边界**：Aider 文档证明了对话级只读/执行模式的用户可见语义，但没有在该页面完整说明 shell、编辑器或外部副作用的统一硬阻断机制；不能把 `ask` 标签等同于 OS 级只读沙箱。

### 2.4 Claude Code 与 Cursor

**[Evidence gap]** Claude Code 官方权限页面（[permissions](https://code.claude.com/docs/en/permissions)）本轮访问超时；Cursor 的 Plan Mode 页面（[plan mode](https://docs.cursor.com/en/agent/plan-mode)）返回了动态文档外壳而非稳定正文。已有公开资料通常把两者作为 plan/permission mode 案例，但本轮不把未重新核验的模式转换、工具白名单或硬阻断细节写成事实。

## 3. 机制与权衡

| 控制点 | 轻量讨论 mode | 正式 plan workflow |
| --- | --- | --- |
| 进入方式 | 单条命令或 sticky mode | 命令、设置或显式阶段工具 |
| 主要产物 | 对话中的建议 | 可审阅/可迭代的计划文档或结构化计划 |
| 变更边界 | 产品可能只承诺“不主动编辑” | 通常明确禁止变更直到确认/退出 |
| 执行转换 | 用户手动切换 code | 用户批准后进入执行阶段 |
| 主要风险 | UI 语义与底层能力可能脱节 | 流程更可靠但步骤和状态更多 |

**[综合推论]** 计划阶段的确认应当是阶段转换，而不是把一次工具审批永久升级为“全自动”。它还应独立于工作区/网络 sandbox：计划阶段可以使用受限的读取/研究能力，执行阶段仍需保留其自身的审批和 sandbox 约束。

**[综合推论]** 退出条件需要防止隐式转换。Codex 公开模板中的 developer-explicit end 和 Gemini 的 approve/iterate/cancel 都将“用户要求执行”与“真正离开计划阶段”分开；这比只根据下一条自然语言意图猜测更可审计。

## 4. Pitfalls 与证据缺口

- UI 上显示 `plan`、`ask` 或 `read-only` 不足以证明写操作被底层拒绝；必须观察工具实际结果或公开执行层实现。
- “不编辑代码”不等于“不执行 shell”：测试、依赖安装、网络访问、git hook 和生成器都可能产生外部副作用，需要单独定义边界。
- 只读阶段的计划文件本身是否写入工作区、session ledger 或独立目录，各产品公开资料并不统一。
- 从宽松模式获得的 session/persistent approval 是否泄漏到 plan 阶段，公开资料通常不足；应将其视为需要显式验证的继承规则。
- 计划被取消、用户拒绝或当前 turn 中断后，是否清空暂存计划、是否保留对话和是否继续排队工具，产品间没有统一公开语义。

## References

- OpenAI Codex CLI：Plan Mode public template，访问日期 2026-08-05。
- Google Gemini CLI：Plan Mode documentation，访问日期 2026-08-05。
- Aider：Chat modes documentation，访问日期 2026-08-05。
- Claude Code：permissions documentation，本轮访问超时，作为证据缺口记录。
- Cursor：Plan Mode documentation，本轮返回动态文档外壳，作为证据缺口记录。
