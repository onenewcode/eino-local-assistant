# 迭代：结构化 `git_log` 工具

日期：2026-08-08

## 背景

代码审阅和故障定位经常需要知道某个文件最近为何变化。仅让模型调用 shell `git log` 会引入终端文本解析、输出失控和路径边界问题；主流 code agent 通常把 Git 历史作为 bounded 的只读探索能力。

## 实现

- 新增工作区受限的 `git_log` 工具，返回 hash、author、ISO date 和 subject 结构化提交记录。
- 默认最多返回 20 条，硬上限 100 条；通过请求 `limit+1` 检测并返回 `truncated`。
- 支持 `path` 路径筛选，路径必须位于配置 workspace 内；不经过 shell，不修改仓库，也不需要 permission approval。
- 注册到默认工具集，因此 TUI、`exec`、MCP 共用同一工具边界。
- 增加真实临时 Git 仓库测试，覆盖顺序、路径筛选、截断、越界路径和非法上限。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
