# Iteration 20260808: expired rule cleanup

## Goal

让授权规则的内存状态和持久化状态一致，启动时不只忽略过期条目，还要清理磁盘上的失效授权，避免规则文件无限增长。

## Changes

- `AttachRuleStore` 加载规则时过滤过期条目。
- 发现过期条目时，将有效规则通过同一 `0600` 原子写入流程写回。
- 清理失败会阻止 confirm broker 启动，避免用户误以为授权状态已被安全恢复。
- 增加测试验证过期条目从内存和持久化文件同时消失。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

后续继续实现按项目/工具独立 TTL 和显式刷新授权命令。
