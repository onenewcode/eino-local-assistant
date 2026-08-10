# TUI 权限状态可见性

## 背景

主流 code agent 会持续显示当前 approval、sandbox/workspace 姿态，避免用户误以为工具处于受限模式。此前本项目状态栏和 `/status` 没有展示有效 permission mode、high-risk policy 或 workspace-only 状态。

## 本轮变更

- `StatusInfo` 增加有效 `PermissionMode`、`HighRiskPolicy` 和 `WorkspaceOnly`。
- TUI idle 状态栏显示 `approval=... risk=... workspace=on/off`。
- `/status` 报告包含同样的安全策略摘要。
- 入口使用 profile 展开后的最终配置，不显示未生效的原始配置。

## 边界

`workspace=on` 只表示 run_command 启动 cwd 约束；由于当前仍是 `sh -c`、无 OS sandbox，状态展示不会把它称作完整 sandbox。
