# 权限 preset

## 背景

权限配置同时服务 TUI 和非交互 `exec`。仅提供多个低层开关容易让 CI 忘记设置 deny，或让团队配置无法快速审计。研究文档建议提供展开后的显式 preset。

## 本轮变更

- 新增 `tools.permission_profile: personal-dev`，展开为 `permission_mode=confirm`、`high_risk_policy=advisory`。
- 新增 `tools.permission_profile: ci-readonly`，强制展开为 `permission_mode=deny`、`high_risk_policy=deny`。
- preset 在 TUI 与 `exec` 入口统一应用；`ci-readonly` 会覆盖同配置块中可能存在的放宽 mode/policy。
- 配置校验拒绝未知 preset，避免拼写错误静默回退默认 unrestricted。

## 边界

preset 只是权限决策层的可审计展开，不是沙箱；`run_command` 仍以当前用户权限执行。`personal-dev` 在非交互 `exec` 中会因 confirm 无审批 UI 而明确拒绝，`ci-readonly` 适合 CI/脚本。
