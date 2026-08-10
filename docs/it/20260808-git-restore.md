# Iteration 20260808: permission-gated git restore

## Goal

补齐修改后的恢复路径，同时把可能丢弃用户工作的 Git 操作放入现有 permission handler，避免 agent 无意中执行全仓库清理。

## Changes

- 新增 `git_restore` 工具，仅接受一个工作区相对文件路径。
- 支持恢复工作树文件，以及只取消一个路径的 staged 状态。
- 空路径直接拒绝，不提供全仓库 restore 入口。
- 在执行 Git 命令前发出 `restore_worktree` / `restore_staged` 权限请求。
- 在权限请求和工具结果中携带恢复前的受限 diff 预览，便于确认和审计。
- 增加批准后恢复、拒绝后保持文件不变和空路径保护测试。
- 接入默认工具 registry，并更新 README。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

仍需补齐恢复前 diff 快照展示、回滚审计记录、批量操作的逐路径确认，以及授权规则记忆；本轮不提供全仓库丢弃或历史改写。
