# 迭代：glob_files 工具

日期：2026-08-08

## 背景

`list_files` 适合查看树状结构，但模型在定位源码、配置或测试文件时还需要 glob 查询。Claude Code 的 Glob、Codex 的受限文件探索都强调路径结果有边界、可截断且不把隐藏目录无意带入上下文。

## 实现

- 新增工作区受限的 `glob_files`，支持 `pattern`、`path`、`max_results` 和 `include_hidden`。
- 默认最多返回 200 条，硬上限 1000，超限返回 `truncated=true`；无效 glob 或越界 path 直接拒绝。
- 默认跳过隐藏路径并始终跳过 `.git`；symlink 不会被递归跟随。
- 已加入默认 registry，与 TUI/`exec` 共用同一 workspace 边界。
- 本轮未引入 MCP client：当前 Eino 依赖只提供 MCP schema，仓库没有可复用 transport/client；后续接入需单独评估依赖、进程生命周期和外部工具权限模型。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
