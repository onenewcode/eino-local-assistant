# CLI exec fork session

本轮为 `exec` 增加 `--fork-session`，对齐 Claude Code 在 `--resume` / `--continue` 时创建新 session ID 的行为，并复用 Codex `fork` 的“从既有历史分支，而不改写父会话”语义。

## 行为

- `exec --resume <id> --fork-session <prompt>` 从指定父 thread 的已完成可见历史创建独立子 thread，再只在子 thread 执行新 prompt。
- `exec --continue --fork-session <prompt>` 先按现有 workspace / `--all` 规则选出 latest thread，再 fork。
- 子 thread 使用本次运行的 model 与 workspace metadata，默认标题为 `Fork of <父标题>`；父标题为空时使用父 session ID。
- 父 thread 的 journal、revision、消息数和 checkpoint 均不变；子 thread 以现有 durable snapshot turn 保存继承历史，后续可独立 resume。
- flag 必须与 `--resume` 或 `--continue` 一起使用。它与 `--recover` 不兼容：活动父 turn 必须先由用户显式恢复/终止，不能在 fork 操作中隐式改写父账本。
- `--ephemeral` 仍不能和任何 resume/continue 流程组合，因此也不会生成“临时 fork 后立即删除”的含混生命周期。

## 验证

测试覆盖 flag help 和组合校验，并通过本地 OpenAI-compatible SSE 服务执行真实 forked exec。回归断言结果返回新的 session ID、父 revision 与消息数保持不变、子 metadata 使用当前 model/workspace，以及子 transcript 同时包含父历史和新 turn。
