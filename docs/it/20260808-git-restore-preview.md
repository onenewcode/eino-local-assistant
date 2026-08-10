# Iteration 20260808: restore preview evidence

## Goal

让 destructive restore 的确认基于实际变更内容，而不是只显示一个文件名；恢复前预览还要进入工具结果，成为 thread 审计证据。

## Changes

- `git_restore` 在执行前读取目标路径的 working-tree 或 staged diff。
- 预览以有界文本进入 `PermissionRequest.Preview`，TUI 最多展示 2400 字节。
- 工具成功结果保留最多 64 KiB 的 `preview` 字段，随已有 tool.completed 事件落盘。
- 预览失败不会绕过权限检查；没有 diff 时仍按当前 Git 命令结果处理。
- 增加测试，确认批准请求和结果都包含删除/恢复内容线索。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

仍需实现回滚审计摘要、批量操作逐路径确认和授权规则记忆；预览不是备份，不能替代真正的恢复点。
