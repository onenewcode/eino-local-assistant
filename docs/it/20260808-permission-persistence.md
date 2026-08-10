# Iteration 20260808: persisted permission rules

## Goal

让用户明确选择的 session authorization 在重新启动助手后仍可复用，同时把规则文件限制在本地数据目录，避免把授权散落到项目代码或 thread 内容中。

## Changes

- 新增 `PermissionRuleStore` 和 project-scoped JSON 文件实现。
- confirm 模式启动时从 `<data_dir>/permissions/<workspace-sha256>.json` 加载规则。
- `a`、`/permissions clear`、`/permissions revoke <n>` 的变更同步持久化。
- 使用 JSON version=1、目录 `0700`、文件 `0600`、临时文件 + 原子 rename。
- 工作区绝对路径参与文件哈希，避免同一相对路径授权跨项目复用。
- 持久化失败时仍完成当前内存操作，并在 TUI 显示明确警告。
- 增加文件权限、round-trip 和 broker 加载测试。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

后续需要提供规则过期/按项目隔离、显式导入导出和密钥/策略迁移；当前文件只存精确 tool/action/detail，不扩大匹配范围。
