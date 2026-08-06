# Plan/read-only 阶段：Codex CLI 与 Claude Code 行业实践

> 状态：外部行业研究笔记，不是本仓库实现方案。
>
> 研究日期：2026-08-05。本文所列来源均在该日期访问；产品行为和文档会变化，采用前应重新核验。
>
> 决策面：Codex CLI 与 Claude Code 如何把规划、提议计划的确认/继续/退出和执行权限切换串起来，以及 permission/approval 与 sandbox 的边界。
>
> 范围：交互模式、计划产物、批准选项、继续规划、取消/退出、批准后的执行模式，以及公开资料对工具审批和沙箱的明确描述。
>
> 不在范围：本仓库代码审计、具体 API 或数据结构设计、把 UI 标签当成硬安全证明、推断未公开的内部事件顺序；不读本仓库 `internal/` 或 `cmd/` 作为行业证据。
>
> 证据标记：`[Documented fact / 已文档化事实]` 表示来源直接说明或公开源码直接展示；`[Cross-product synthesis / 跨产品综合]` 表示由多个产品事实归纳；`[Evidence gap / 证据缺口]` 表示已取得的公开资料不足以确认。

## 1. 结论

- **[Cross-product synthesis / 跨产品综合]** “plan/read-only 阶段”“permission/approval mode”和“sandbox”不是同一控制面：阶段定义模型和用户协作流程，permission/approval 决定某个工具调用是否运行或是否先询问，sandbox 决定已运行的进程能访问什么。Claude 官方文档直接这样区分；Codex 的公开协议也把 collaboration mode、approval 和 sandbox 表达为不同类型，但类型分离本身不证明所有运行时组合的细节。
- **[Cross-product synthesis / 跨产品综合]** 提议计划的确认应是明确的状态转换，而不是一次工具审批永久升级权限。已取得的三组资料都展示了某种“批准并执行 / 留在规划 / 退出或取消”的分叉；批准后的权限仍然有不同档位，例如 Codex 的 Default、Claude 的自动/手动编辑、Gemini 的自动/手动接受编辑。
- **[Documented fact / 已文档化事实]** Codex 当前公开 TUI 源码把批准弹窗写成三项：`Yes, implement this plan`、`Yes, clear context and implement`、`No, stay in Plan mode`；前两项分别切换到 Default 开始编码，或以计划开启新鲜上下文，最后一项继续与模型规划。Claude 官方文档对应地提供自动执行、手动逐项批准和 `No, keep planning`。
- **[Cross-product synthesis / 跨产品综合]** “退出计划”与“拒绝/取消当前计划”需要分开：继续规划保留阶段和计划，退出是不批准计划而离开阶段，批准则是离开阶段并进入执行权限。若产品只根据下一条自然语言猜测是否执行，用户很难审计实际状态。
- **[Documented fact / 已文档化事实]** 只读规划不等于“不产生任何副作用”。Claude 的 plan mode 允许读取和探索性 shell 命令；计划阶段的命令还可能走内建只读集合、auto classifier 或常规审批。Gemini 明确把允许工具收窄到读取、搜索、研究和交互工具。Codex 计划模板要求非变更探索，但当前已取得的资料不足以证明所有 shell、网络或外部工具在每种配置下都被同一硬边界阻断。

## 2. 已部署应用证据

### 2.1 Codex CLI

#### 计划阶段与提议计划

- **[Documented fact / 已文档化事实]** Codex 的公开 Plan Mode 模板规定三阶段协作：先探索和讨论，再形成 decision-complete 的最终计划，最后执行。Plan Mode 持续到 developer message 显式结束；用户在仍处于 Plan Mode 时要求执行，应被视为“规划执行”，而不是实际执行；允许读取、搜索、静态检查等非变更动作，禁止变更动作；最终计划使用 `<proposed_plan>` 块。
- **[Documented fact / 已文档化事实]** 同一模板明确把 Plan Mode 与 `update_plan` checklist 工具分开：后者是进度清单，不进入或退出 Plan Mode；当前公开 handler 还明确在 `ModeKind::Plan` 中拒绝 `update_plan`。这说明“已有 TODO/进度列表”不等于“已获得计划批准”。

来源：

- [Codex Plan Mode template](https://raw.githubusercontent.com/openai/codex/main/codex-rs/collaboration-mode-templates/templates/plan.md)，访问日期 2026-08-05。
- [Codex plan handler](https://raw.githubusercontent.com/openai/codex/main/codex-rs/core/src/tools/handlers/plan.rs)，访问日期 2026-08-05。

#### 确认、继续与批准后的执行

- **[Documented fact / 已文档化事实]** 当前公开 TUI 源码的确认弹窗标题是 `Implement this plan?`，选项为：
  - `Yes, implement this plan`：动作描述为 `Switch to Default and start coding.`，提交带 Default collaboration mask 的用户消息。
  - `Yes, clear context and implement`：要求已有非空计划，把计划放入“fresh thread”提示中，通过清空 UI 后提交执行消息；源码还显示没有 Default mode 或没有 approved plan 时相应选项会禁用。
  - `No, stay in Plan mode`：没有执行动作，描述为 `Continue planning with the model.`。
- **[Documented fact / 已文档化事实]** Codex 的公开协议把初始 collaboration mode 表达为 `plan` 或 `default`；公开 v2 schema 又把 `AskForApproval`（如 `on-request`、`never` 及 granular approvals）与 `SandboxMode`（`read-only`、`workspace-write`、`danger-full-access`）分别建模。它们支持“阶段、审批、沙箱是不同轴”的观察，但不能单凭 schema 推出每个 TUI 选项最终如何组合所有权限。

来源：

- [Codex plan implementation confirmation](https://raw.githubusercontent.com/openai/codex/main/codex-rs/tui/src/chatwidget/plan_implementation.rs)，访问日期 2026-08-05。
- [Codex collaboration mode schema](https://raw.githubusercontent.com/openai/codex/main/codex-rs/app-server-protocol/schema/typescript/CollaborationMode.ts)，访问日期 2026-08-05。
- [Codex mode kind schema](https://raw.githubusercontent.com/openai/codex/main/codex-rs/app-server-protocol/schema/typescript/ModeKind.ts)，访问日期 2026-08-05。
- [Codex approval schema](https://raw.githubusercontent.com/openai/codex/main/codex-rs/app-server-protocol/schema/typescript/v2/AskForApproval.ts)，访问日期 2026-08-05。
- [Codex sandbox mode schema](https://raw.githubusercontent.com/openai/codex/main/codex-rs/app-server-protocol/schema/typescript/v2/SandboxMode.ts)，访问日期 2026-08-05。

#### 边界与证据缺口

- **[Evidence gap / 证据缺口]** 已取得的 Codex 源码分别展示了模型提示、TUI 选择动作和公开 schema，但没有在一个可直接核验的来源中说明“用户选择确认”如何与模板中的 developer-explicit end 对接，也没有确认事件落盘、模式切换和下一轮模型上下文的完整顺序。
- **[Evidence gap / 证据缺口]** 这些来源展示了 `No, stay in Plan mode`，但没有明确写出单独的“取消并丢弃当前计划”、Esc/关闭弹窗后的计划保留规则、退出后是否清空待执行工具或队列。不能把普通弹窗 dismiss 推断成取消语义。
- **[Evidence gap / 证据缺口]** 公开协议列出 approval 与 sandbox 的枚举，却没有在本文已取得的材料中证明 Plan Mode 对 shell、网络、MCP 或子进程的统一硬阻断范围；模板的“不变更”是协作契约，不能单独当作 OS 沙箱证明。

### 2.2 Claude Code

#### Plan mode、提议计划与确认

- **[Documented fact / 已文档化事实]** Claude Code 官方权限模式文档把 `plan` 定义为：读取文件、运行只读 shell 命令来探索并写出计划，但不编辑源文件；输入 `/plan`、按 `Shift+Tab` 或以 `claude --permission-mode plan` 进入。再次按 `Shift+Tab` 可以不批准计划而离开 plan mode。
- **[Documented fact / 已文档化事实]** 计划准备好后，Claude Code 提供三类选择：`Yes, and use auto mode`（不可用时显示 auto-accept edits；已有 bypass permissions 时显示 bypass permissions）、`Yes, manually approve edits`、`No, keep planning`。批准会退出 plan mode，并切换到所选批准项对应的权限模式后开始编辑；继续规划则仍在 plan mode。之后可再次按 `Shift+Tab` 或用 `/plan` 规划。
- **[Documented fact / 已文档化事实]** `Ctrl+G` 可在 Claude proceeds 前打开 proposed plan 并直接编辑；`showClearContextOnPlanAccept` 还可提供批准并清理规划上下文的选项。官方工具参考把 `EnterPlanMode` 列为无需权限的进入动作，把 `ExitPlanMode` 列为需要权限、会展示计划供批准并退出计划的动作。

来源：

- [Claude Code permission modes](https://code.claude.com/docs/en/permission-modes.md)，访问日期 2026-08-05。
- [Claude Code tools reference](https://code.claude.com/docs/en/tools-reference.md)，访问日期 2026-08-05。
- [Claude Code commands](https://code.claude.com/docs/en/commands.md)，访问日期 2026-08-05。
- [Claude Code interactive mode](https://code.claude.com/docs/en/interactive-mode.md)，访问日期 2026-08-05。

#### Approval 与 sandbox 的边界

- **[Documented fact / 已文档化事实]** Claude 的权限文档说 permission rules 由 Claude Code 执行而不是由模型提示执行；规则分为 deny、ask、allow，并按 deny → ask → allow 的顺序决定结果。permission modes 是基线，显式 ask/deny 规则仍可影响模式，包括 bypass permissions 下仍会触发的明确 ask 规则、组织级 connector ask 和要求用户交互的 MCP 工具。
- **[Documented fact / 已文档化事实]** plan mode 的 shell 行为有条件分支：内建只读集合可运行；若 auto mode 可用且 `useAutoModeDuringPlan` 开启，classifier-approved 命令运行、被拒命令被阻断；否则其他 shell 命令要求审批。官方文档明确说 sandbox 的 auto-allow 不会在 plan mode 中扩大这类审批范围。
- **[Documented fact / 已文档化事实]** 文档同时保留一个重要例外：在“bypass permissions available”的 session 中，plan mode 的编辑阻断不按普通路径成立。因而 `plan` 标签不能独立作为所有配置下的硬只写保护；bypass 是否可用是必须一并观察的前置条件。
- **[Documented fact / 已文档化事实]** Claude 官方 sandbox 文档把两层职责拆开：permission rules 在工具运行前决定工具能否使用，sandbox 在运行后由 OS 限制 Bash 及其子进程的文件系统和网络访问；sandbox 只适用于 Bash，不自动约束 Read、Edit、WebFetch 或 MCP 这类进程内工具。`/sandbox` 也不是 permission mode；sandbox auto-allow 与 auto mode classifier 相互独立，可以组合。

来源：

- [Claude Code permissions](https://code.claude.com/docs/en/permissions.md)，访问日期 2026-08-05。
- [Claude Code sandboxing](https://code.claude.com/docs/en/sandboxing.md)，访问日期 2026-08-05。

#### 取消、退出与未公开部分

- **[Documented fact / 已文档化事实]** Claude 文档明确提供“再次按 `Shift+Tab` 离开且不批准计划”和“`No, keep planning` 留在计划模式”两条不同路径；交互模式文档还规定 `Esc` 在权限对话框打开时关闭对话框，在运行中的响应/工具调用中断当前工作并保留已完成的工作。
- **[Evidence gap / 证据缺口]** 已取得的官方页面没有把 `Esc` 定义为“取消当前 proposed plan”，也没有明确说明关闭 `ExitPlanMode` 对话框后计划是否持久化、是否丢弃、是否重新展示。不能把通用 dialog close 语义扩展为计划取消语义。
- **[Evidence gap / 证据缺口]** 官方页面说明计划可被编辑且批准可清理上下文，但未完整说明计划文件/草稿的存储位置、会话恢复后的状态、拒绝后未完成工具是否排队或被清除。

### 2.3 Gemini CLI：交叉核对

- **[Documented fact / 已文档化事实]** Gemini CLI 的官方 Plan Mode 文档把 Plan 定义为 read-only environment，并提供 `--approval-mode=plan`、`Shift+Tab`、`/plan` 和自然语言进入方式。其工具限制列出读取、搜索、研究子代理和用户交互；只允许把 Markdown 计划写入专用计划目录/自定义计划目录。
- **[Documented fact / 已文档化事实]** Gemini 的流程是：研究和讨论，等待用户确认后起草正式计划，生成可查看/编辑的 Markdown 计划；正式审批提供自动接受编辑或手动接受编辑；反馈或直接编辑计划会迭代；`Esc` 可取消计划。批准计划会自动退出 Plan Mode 并开始实现，`Shift+Tab` 或自然语言也能退出。
- **[Documented fact / 已文档化事实]** Gemini 文档还说明审批继承是上下文相关的：Default/Auto-Edit 中的持久工具批准不适用于 Plan Mode；在 Plan Mode 中授予的批准则被视为有意的全局信任，可适用于所有模式。该行为是产品自述，不应直接当成其他产品的默认规则。

来源：

- [Gemini CLI Plan Mode](https://raw.githubusercontent.com/google-gemini/gemini-cli/main/docs/cli/plan-mode.md)，访问日期 2026-08-05。

## 3. 机制与权衡

| 控制点 | 已观察到的产品行为 | 研究含义 |
| --- | --- | --- |
| 进入规划 | Codex 有 `plan` collaboration mode；Claude 用 `plan` permission mode；Gemini 把 Plan 作为 approval mode | 同一个“Plan”词可能承载协作阶段或审批策略，不能仅按名称比较 |
| 规划中的动作 | Codex 模板允许非变更探索；Claude 允许读取和条件化 shell；Gemini 显式收窄到读、搜、研究和交互工具 | “只读”需要声明允许的工具和副作用，不应只写一句提示词 |
| 计划产物 | Codex 最终使用 `<proposed_plan>`；Claude 有 proposed plan 和 `Ctrl+G` 编辑；Gemini 写 Markdown 计划文件 | 计划是可审阅对象时，批准点比“模型说完了”更可观察；文件是否持久化仍需单独声明 |
| 继续规划 | Codex `No, stay in Plan mode`；Claude `No, keep planning`；Gemini 反馈/编辑后更新计划 | 拒绝执行不应等同于结束规划；应保留明确的 continue 分支 |
| 退出/取消 | Claude 明确支持不批准而 `Shift+Tab` 离开；Gemini 明确支持 `Esc` 取消；Codex 已取得源码只明确显示留在 Plan 的选项 | cancel、exit、dialog close、turn interrupt 不是天然同义词 |
| 批准后执行 | Codex 选 Default 或新鲜上下文执行；Claude 选 auto/手动等权限模式；Gemini 选自动/手动接受编辑 | 批准是“进入执行策略”的选择，不是无条件授予最高权限 |
| approval 与 sandbox | Claude 明确把工具前的 permission decision 与 Bash/子进程的 OS sandbox 分层；Codex schema 也分列 approval 与 sandbox mode | 即使规划阶段不编辑，仍需说明 shell、网络、MCP、子进程和工作区外路径的边界 |

## 4. 跨产品综合

- **[Cross-product synthesis / 跨产品综合]** 一个可复核的计划流程至少有四个可区分状态：`planning`（探索/修改计划）、`awaiting-confirmation`（展示 proposed plan）、`execution`（执行并按新的权限策略审批）、`exited-or-cancelled`（不执行该计划）。Codex 和 Claude 的公开确认项直接支持前三者；Gemini 的 `Esc` 使取消路径更明确。
- **[Cross-product synthesis / 跨产品综合]** “批准计划”最好同时记录选中的执行策略。三个产品分别把批准与 Default、auto/手动、自动/手动编辑联系起来；这比把 plan approval 解释为一个永久的 allow-all 开关更符合公开行为。
- **[Cross-product synthesis / 跨产品综合]** 计划阶段的安全承诺要分成两句：第一句是“哪些工具调用在阶段内允许/需要审批”，第二句是“运行后的进程在文件系统和网络上能到哪里”。Claude 的文档把这两个问题明确分开；Gemini 对 Plan 的工具白名单提供了更窄的交叉样本。
- **[Cross-product synthesis / 跨产品综合]** 计划审批的持久化/继承不能假设为全局默认。Gemini 明确区分从其他模式继承的批准；Claude 区分 Bash 的仓库级命令批准和文件修改的 session 级批准；Codex 已取得材料没有足够信息确认 plan approval 的继承范围。

## 5. Pitfalls 与证据缺口

- **[Evidence gap / 证据缺口]** `plan`、`read-only`、`ask` 或 `bypass` 的状态栏/标签都不是 OS 级安全证明。尤其 Claude 文档明确说明 bypass-available session 是 plan mode 的例外；Codex 模板的禁止变更是模型协作契约，不单独证明底层执行器拒绝写入。
- **[Evidence gap / 证据缺口]** “不编辑文件”不等于“无副作用”：shell 可能生成文件、写临时目录、触发 hook、访问网络或启动子进程；Claude 的 sandbox 只约束 Bash 及其子进程，其他工具必须另行核验。
- **[Evidence gap / 证据缺口]** 已取得的资料没有统一公开 session 恢复、计划取消后的草稿清理、拒绝后工具队列、并发/后台任务和中断后的精确语义。不能用 UI 上的“cancel”或“exit”代替这些未披露的生命周期保证。
- **[Evidence gap / 证据缺口]** Codex 当前公开来源没有完整呈现审批弹窗、协作模板、sandbox profile 三者在每个组合下的运行时映射；公开协议类型可以证明概念分离，不能据此补齐未公开的执行策略。
- **[Cross-product synthesis / 跨产品综合]** 若采用这些资料作为行为参照，最值得保留的是可见的确认分叉、明确的 continue/exit 区分、批准后执行策略选择和独立的 sandbox 说明；它们是跨产品证据支持的交互原则，不是本仓库的实现方案。

## References

- OpenAI Codex CLI：Plan Mode template、plan handler、plan implementation confirmation、collaboration/approval/sandbox schemas，访问日期 2026-08-05。
- Claude Code：permission modes、permissions、sandboxing、tools reference、commands、interactive mode，访问日期 2026-08-05。
- Google Gemini CLI：Plan Mode documentation，访问日期 2026-08-05。
