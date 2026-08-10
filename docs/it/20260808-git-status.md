# Iteration 20260808: structured git status

## Goal

在 `git_diff` 之外提供结构化工作树状态，让 agent 能可靠区分已修改、已暂存、冲突和未跟踪文件，减少对 shell 文本输出的脆弱解析。

## Changes

- 新增只读 `git_status` 工具，执行 `git status --porcelain=v1 -z`。
- 返回路径、index 状态、worktree 状态、重命名原路径和未跟踪标记。
- 正确消费 rename/copy 的双 NUL 记录，并返回 `renamed` / `conflicted` 标记。
- 支持按工作区相对路径筛选，拒绝越界路径。
- 接入默认 registry，增加真实 Git 仓库测试，覆盖 tracked 修改和 untracked 文件。
- 更新 README 和工具清单。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

后续继续覆盖回滚和恢复操作、授权规则记忆，以及 git 操作的权限确认；本轮仍只读，不执行提交、暂存或丢弃变更。
