# Iteration 20260808: interactive tool confirmation

## Goal

把上一轮的 permission handler 接入 TUI，形成主流 code agent 的最小逐次确认闭环：工具执行前显示请求，用户明确批准或拒绝，取消 turn 时不继续执行。

## Changes

- 新增 `PermissionBroker`，在工具 goroutine 和 Bubble Tea UI 之间传递带 ID 的请求。
- `confirm` 模式显示工具名、动作和目标；`y`/Enter 批准，`n`/Esc 拒绝。
- 多个并发请求按 FIFO 排队，当前请求完成后再处理下一个。
- Ctrl+C/turn context 取消会取消 broker 中的等待请求；工具收到拒绝或取消错误后不会产生副作用。
- 更新配置示例、帮助提示和 broker 单元测试。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

仍需补齐持久化的授权规则（按工具/路径/命令记忆）、权限请求超时显示、非 TTY 下的 confirm 行为，以及更完整的 shell 沙箱和命令风险分级。
