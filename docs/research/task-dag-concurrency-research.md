# 任务计划与并发：行业实践调研（已落地对照）

> 状态：调研 + 与本仓实现对照。  
> 调研日期：2026-08-07；落地重构同日基于 Codex 源码对齐。  
> 范围：编码代理如何展示计划、表达进度，以及写入安全与计划工具的边界。  
> 范围外：各产品未公开的调度器、锁实现细节。

## 1. 结论

- **综合判断**：计划清单、工具权限、会话交付是三个不同概念。清单面向用户理解进度；权限决定能否改工作区；交付由正常对话结束路径决定，**不应**被 checklist 硬锁。  
- **综合判断（Codex 源码）**：Codex 的 `update_plan` 是 TODO/checklist 工具：整表更新 `step`+`status`，至多一个 `in_progress`，handler 只发 UI 事件并返回 `"Plan updated"`——**没有** shell proof 矩阵，**没有** completion gate。  
- **综合判断**：共享工作区的写入安全应落在 permissions / sandbox / approval，而不是 plan 工具二次加锁。  
- **本仓决策**：废弃自研「DAG + proof + 独占写 + 交付闸门」；改为 Codex 同构的 soft checklist（见 `docs/task-runtime.md`）。

## 2. 已部署产品的证据

### Codex CLI（源码快照 `c87a218`，2026-08-07 访问）

- **事实**：`create_update_plan_tool()` 定义工具名 `update_plan`；参数为 optional `explanation` + required `plan` 数组；每项 `step`（string）+ `status` enum `pending|in_progress|completed`；描述写明 *At most one step can be in_progress at a time*。  
  源码：[`plan_spec.rs`](https://github.com/openai/codex/blob/c87a218bd3e7ee0a0181109b3ff7bbc2156de13e/codex-rs/core/src/tools/handlers/plan_spec.rs)

- **事实**：协议类型 `UpdatePlanArgs` / `PlanItemArg` / `StepStatus` 与上述 schema 一致，`deny_unknown_fields`。  
  源码：[`plan_tool.rs`](https://github.com/openai/codex/blob/c87a218bd3e7ee0a0181109b3ff7bbc2156de13e/codex-rs/protocol/src/plan_tool.rs)

- **事实**：`PlanHandler` 解析参数后 `session.send_event(..., EventMsg::PlanUpdate(args))`，工具输出文案恒为 `"Plan updated"` / success；在 `ModeKind::Plan`（产品 Plan mode）下 **禁止** 调用 update_plan（说明 checklist 属于执行态 TODO，不是「规划模式」本体）。  
  源码：[`plan.rs`](https://github.com/openai/codex/blob/c87a218bd3e7ee0a0181109b3ff7bbc2156de13e/codex-rs/core/src/tools/handlers/plan.rs)

- **事实**：TUI `PlanUpdateCell` 按 Completed / InProgress / Pending 渲染 `✔` / 强调框 / 弱化框，并按终端宽度 wrap。Proposed plan 另有 markdown 源以便 resize 重绘。  
  源码：[`plans.rs`](https://github.com/openai/codex/blob/c87a218bd3e7ee0a0181109b3ff7bbc2156de13e/codex-rs/tui/src/history_cell/plans.rs)

- **事实**：多代理状态（Pending init / Running / …）在独立 multi-agent UI 中展示，**不**塞进 plan 项。  
  源码：[`multi_agents.rs`](https://github.com/openai/codex/blob/c87a218bd3e7ee0a0181109b3ff7bbc2156de13e/codex-rs/tui/src/multi_agents.rs)

### Claude Code

- **事实**：子代理独立上下文与工具权限；官方建议将淹没主对话的搜索交给子代理后只回摘要。  
  [Claude Code：Subagents](https://code.claude.com/docs/en/sub-agents)

- **事实**：Agent teams 共享任务列表（pending / in progress / completed），未解决依赖不可认领，文件锁防竞争。这是 **多代理协作** 模型，不是单会话 checklist 硬闸门。  
  [Claude Code：Agent teams](https://code.claude.com/docs/en/agent-teams)

### OpenCode

- **事实**：按 agent 类型拆权限：Plan（分析限写）、Explore（只读）、General（可并行执行单元）。计划与执行权限分离。  
  [OpenCode：Agents](https://opencode.ai/docs/agents/)

## 3. 机制与取舍

| 机制 | 解决的问题 | 代价与限制 |
| --- | --- | --- |
| Codex `update_plan` 整表 checklist | 用户可见进度；模型自更新 | 不表达复杂 DAG；不保证验证已跑 |
| 至多一个 in_progress | 清单语义清晰、可预期 | 并行工作只能在清单外用工具完成 |
| permissions + sandbox | 真实写入边界 | 与 plan 无关，需单独配置 |
| 硬 proof + 完成闸门（**已弃用**） | 试图强制「测过才交付」 | 与 Codex 不对齐；shell deny/默认写独占导致死循环与预算耗尽 |

## 4. 本仓旧设计问题（故障复盘）

观测（产品内 TUI）：

1. 模型调用重 task 工具建立多节点图 + proof。  
2. `shell cat …` **denied**（策略层）→ proof 无法入账。  
3. 完成闸门要求「scenario owns done task with passing proof」→ 无法交付。  
4. 决策预算耗尽 → `task paused`。  

根因：**把计划 / 证据 / 交付锁揉进同一套工具**，且默认 `workspace_write` 独占放大误伤。这不是 Codex plan 语义。

## 5. 落地映射

| Codex | 本仓现状 |
| --- | --- |
| `update_plan` | `update_plan`（`internal/agent/task_tools.go`） |
| `pending/in_progress/completed` | 同左；TUI 映射为 pending/working/done 展示 |
| 至多一个 in_progress | 控制器校验 |
| 「Plan updated」 | 工具 message / display_hint |
| PlanUpdate 卡片 | `• Updated Plan`（无 proofs 行） |
| 无 completion gate | `TaskCompletionGate` 始终不 Active |
| 写入另轨 | permissions / sandbox / shell impact 不变 |

实现说明见 [task-runtime.md](../task-runtime.md)。

## 6. 证据缺口与风险

- Codex 内部是否另有未开源的「长程任务」控制器：公开 plan 工具路径 **没有** proof gate；不能把 UI todo 推断为硬锁。  
- 去掉硬闸门后，模型可能在未跑测试时交付：靠提示词与用户审批/沙箱，而不是假 proof。  
- 旧 v1/v2 task snapshot 在 resume 时丢弃，避免复活闸门。

## 参考资料

1. OpenAI Codex `plan_spec.rs` / `plan.rs` / `plan_tool.rs` / `plans.rs`，提交 `c87a218bd3e7ee0a0181109b3ff7bbc2156de13e`，2026-08-07。  
2. Anthropic Claude Code：Subagents、Agent teams 文档，2026-08-07。  
3. OpenCode Agents 文档，2026-08-07。  
