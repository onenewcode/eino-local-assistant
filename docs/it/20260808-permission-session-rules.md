# Iteration 20260808: session permission rules

## Goal

补齐主流 code agent 的“本次会话允许后不重复打断”体验，同时把授权范围限制在精确的工具、动作和目标组合，避免一次批准扩大成全局权限。

## Changes

- `PermissionBroker` 增加内存中的精确 session rule：`tool + action + detail`。
- `y`/Enter 仍只批准当前调用；`a`/`A` 批准并记住当前会话中的同一请求。
- 命中 session rule 的后续请求直接放行，不再显示确认提示。
- 规则默认写入数据目录 `permissions.json`，使用 `0600` 和原子替换；取消和拒绝不会创建规则。
- 更新 TUI 提示、README 和 broker 测试。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

仍需实现规则的显式查看/撤销、持久化授权策略和更细的命令风险分类；本轮刻意只做当前 TUI 会话内的精确记忆。
