# Iteration 20260808: permission risk visibility

## Goal

让权限确认显示操作风险，帮助用户在 shell、文件修改和 Git 恢复之间快速区分危险程度，同时保持分类与执行策略解耦。

## Changes

- 新增 `RiskLevel`：`low`、`medium`、`high`。
- 对 shell 命令提供保守启发式分类：只读检查通常为 low，普通未知命令为 medium，删除/恢复/sudo/重定向等为 high。
- `edit_file` / `apply_patch` 标为 medium，`git_restore` 标为 high。
- TUI 权限请求显示风险级别；风险字段也随事件上下文传递。
- 风险分类不会自动批准或拒绝请求，仍由现有 permission mode/handler 决定。
- 增加命令分类测试并更新 README。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

后续需要更完整的 shell 解析、可配置风险策略和跨会话授权持久化；本轮不把启发式分类当作沙箱或安全证明。
