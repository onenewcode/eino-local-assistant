# 非交互 exec 子命令

## 背景

主流 code agent 通常同时提供 TUI 和适合脚本、CI、管道的单次执行入口。本项目此前 `chat` / `resume` 在非 TTY 下直接失败，无法用于自动化调用。

## 本轮变更

- 新增 `exec <prompt>` 子命令，不要求 stdin/stdout 为 TTY。
- 使用与 TUI 相同的模型、ReAct 工具、thread 账本、上下文配置和权限 handler。
- assistant 响应通过 `stdout` 流式输出，完成后换行；会话仍写入本地 thread store。
- `tools.permission_mode: confirm` 在启动模型前明确拒绝；非交互环境没有安全的人工审批通道。
- `tools.high_risk_policy: confirm` 同样明确拒绝；`unrestricted`、`deny` 及 advisory/deny 风险策略可用于自动化场景。

## 边界

`exec` 不提供 TUI 斜杠命令或交互式审批；恢复、结构化输出和 CI preset 分别由后续迭代补齐。非交互模式不会隐式放宽审批。
