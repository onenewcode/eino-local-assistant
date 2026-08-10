# Iteration 20260808: tool permission boundary

## Goal

为高风险工具建立统一的权限边界，避免每个工具自行决定是否允许副作用，并为后续 TUI 逐次确认提供稳定的请求接口。

## Changes

- 新增 `PermissionRequest`、`PermissionHandler` 和 `RequirePermission`。
- `run_command`、`edit_file`、`apply_patch` 在执行副作用前统一检查 handler。
- 新增 `tools.permission_mode`：`unrestricted`（默认）和 `deny`。
- 将 permission handler 随 `Session` turn context 传递，恢复会话和新建会话使用同一策略。
- 增加命令拒绝、文件拒绝且不改变文件的测试。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

`deny` 是非交互保护模式，不等同于主流 code agent 的逐次确认。本轮明确保留该差距；下一步将把 handler 接到 TUI 的待处理请求、Y/N 快捷键和取消/超时处理。
