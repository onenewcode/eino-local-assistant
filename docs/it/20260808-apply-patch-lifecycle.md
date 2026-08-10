# Iteration 20260808: patch file lifecycle

## Goal

继续完善 `apply_patch`，覆盖 code agent 修改文件时常见的新建和删除场景，使一次受控 patch 不必退回任意 shell 重定向或手工删除。

## Changes

- 支持 `*** Add File`，包括空文件和逐行 `+` 内容。
- 支持 `*** Delete File`，并支持标准 unified diff 的 `/dev/null` 新建/删除标记。
- 新建文件默认使用 `0644`，已存在文件修改仍保留原权限。
- 新建、更新、删除都继续执行工作区相对路径校验；更新 hunk 仍在写入前全部验证。
- 增加生命周期测试，确认新增文件内容、删除结果和既有 patch 行为不回归。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

仍需实现权限确认/拒绝策略、git diff 与回滚、跨平台换行和编码处理，以及多文件 patch 的原子事务语义。
