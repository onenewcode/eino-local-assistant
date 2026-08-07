# Task / Plan Runtime（Codex 风格 checklist）

多步骤编码工作时，模型可用 **`update_plan`** 维护一份用户可见的进度清单。  
本实现按 **OpenAI Codex CLI** 的 `update_plan` 工具语义对齐：**进度 UI only**，不充当 shell 证据账本，也不阻挡最终交付。

写入安全继续由 **permissions + sandbox + shell impact** 负责，与 checklist 解耦。

## 对标来源（源码）

| 来源 | 路径 / 提交 | 采用点 |
| --- | --- | --- |
| Codex 工具 schema | [`plan_spec.rs`](https://github.com/openai/codex/blob/c87a218bd3e7ee0a0181109b3ff7bbc2156de13e/codex-rs/core/src/tools/handlers/plan_spec.rs) `c87a218` | 单工具 `update_plan`；项为 `step` + `status`（`pending` \| `in_progress` \| `completed`）；**至多一个** `in_progress` |
| Codex 参数类型 | [`plan_tool.rs`](https://github.com/openai/codex/blob/c87a218bd3e7ee0a0181109b3ff7bbc2156de13e/codex-rs/protocol/src/plan_tool.rs) | `UpdatePlanArgs{ explanation?, plan: [{step, status}] }` |
| Codex handler | [`plan.rs`](https://github.com/openai/codex/blob/c87a218bd3e7ee0a0181109b3ff7bbc2156de13e/codex-rs/core/src/tools/handlers/plan.rs) | 解析参数 → 发 `PlanUpdate` 事件 → 返回 **「Plan updated」**；不跑 proof、不批 completion |
| Codex TUI | [`plans.rs`](https://github.com/openai/codex/blob/c87a218bd3e7ee0a0181109b3ff7bbc2156de13e/codex-rs/tui/src/history_cell/plans.rs) | `PlanUpdateCell`：`✔` completed / 强调 `□` in_progress / 弱化 `□` pending；按宽度重排 |

**明确不采用（旧本仓重控制器 / 非 Codex plan 工具）：**

- 需求—场景—任务 DAG + 每任务 shell proof 矩阵  
- `task_complete` 交付硬闸门 / GapPacket 续跑  
- `workspace_access` 独占写任务 / workspace epoch 失效证据  
- 未 plan 就 `apply_patch` 则 `planRequired` 锁交付  

这些曾是本仓自研，**不是** Codex `update_plan` 的行为。

## 包职责

| 关注点 | 位置 | 职责 |
| --- | --- | --- |
| 清单存储、校验、投影 | `internal/agent/task_controller.go` | `UpdatePlan`、状态投影、软中断 |
| 持久化 snapshot v3 | `internal/agent/task_persistence.go` | 版本化 checklist；丢弃旧 v1/v2 proof 快照 |
| 工具 schema | `internal/agent/task_tools.go` | 仅 `update_plan` |
| ReAct 注入 / 批次 | `internal/agent/react.go` | 活跃清单注入 ephemeral system packet；`update_plan` 与其它工具同批 → sequential |
| 会话 DTO | `internal/chat/task_runtime.go` | `TaskRunStatus` 投影；无交付闸门 |
| TUI | `internal/tui/task_plan_render.go` 等 | Updated Plan 卡片、`Ctrl+T`、`/goal` |

## 工具：`update_plan`

### 输入

```json
{
  "explanation": "optional note",
  "plan": [
    { "step": "Inspect the failing test", "status": "completed" },
    { "step": "Apply the fix", "status": "in_progress" },
    { "step": "Re-run tests", "status": "pending" }
  ]
}
```

### 规则

1. `plan` 至少一项；每项 `step` 非空。  
2. `status` ∈ `pending` | `in_progress` | `completed`（缺省 `pending`）。  
3. **至多一个** `in_progress`（与 Codex 工具说明一致）。  
4. 每次调用 **整表替换** 清单（非增量 patch）。  
5. 成功消息固定为 **`Plan updated`**（对齐 Codex handler）。  
6. **不**要求 shell proof；**不**因未勾选完成而拒绝最终回复。

### 输出

结构化 `TaskToolOutput`：`ok`、`run_state`、`message=Plan updated`。  
`complete` 恒为 false（交付不由本工具批准）。

## 与执行 / 安全的边界

| 能力 | 谁负责 |
| --- | --- |
| 展示进度 | `update_plan` + TUI |
| 是否允许 `shell` / `apply_patch` | permissions、approval、sandbox、impact 分类 |
| 最终能否回复用户 | 会话与 ReAct 正常结束路径；**checklist 不参与** |

因此：读文件 shell 被 deny 时，应在策略层排查，而不是指望 task 工具「解独占」；模型仍可交付说明或改用合法命令。

## 会话与持久化

- Snapshot **version 3**：`explanation` + `items[{step,status}]` + `state(active|interrupted)`。  
- 旧 version 1/2（proof DAG）加载时 **丢弃**。  
- 无完成闸门：清单不参与 Session 交付路径。  
- Esc / 新用户消息可 `InterruptTask` 将清单标为 interrupted（展示用）。

## ReAct

1. 存在活跃清单时，在 durable system 前缀后注入 ephemeral「Current plan」包（不入库为用户消息）。  
2. 同一模型响应内若调用数 >1 且包含 `update_plan`，整批 `requires_sequential_execution`（与其它控制工具同批策略一致）。  
3. **不再** 因 checklist 拒绝并行 shell / 写入工具。

## TUI

- `• Updated Plan`：Codex 风格勾选列表；**不**显示 proofs / depends_on / access。  
- `Ctrl+T`：紧凑步骤列表。  
- `/goal`：explanation + progress + steps。  

## 提示词

见 `internal/agent/prompt.go` 的 `AutonomousTaskPolicy`：鼓励多步骤时用 checklist，并写明不挡交付、不授权写入。

## 历史说明

此前本仓实现过「DAG + shell proof + 完成闸门 + 工作区独占」。该设计 **未** 与 Codex plan 工具对齐，且导致「一调用 task 即卡住 / 预算耗尽」类失败。当前文档描述的是 **完全替换后** 的 soft plan 运行时。
