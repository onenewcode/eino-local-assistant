# 迭代：TUI 后台只读分析子 agent

日期：2026-08-11

## 背景

当前 TUI 能在 foreground turn 中执行工具循环，也提供一次性的 `/btw` 旁路问题；但没有具备独立身份、查询、取消和结果检查面的后台分析任务。用户必须等待主 turn 或手工维持外部笔记，无法监督一个独立的只读调查。

本轮参考已部署的 Claude Code `--bg` / `claude agents`、Codex CLI 的 stable `multi_agent` feature，以及 OpenCode `mode: subagent` 的角色、权限与 step budget 分离方式。证据范围与没有推断的产品细节记录在 `docs/research/background-subagent-control-research.md`。

## 实现

- 新增 `/agent <analysis task>`：立即创建 process-local `agent-N` 任务，前台 regular turn 保持运行、queue 不变；每个 child 使用独立 cancellation context，最多同时运行 4 个。
- 新增 `/agents`、`/agents show <id>` 与 `/agents cancel <id>`：列表显示 working、cancelling、completed、failed 或 cancelled；结果和失败原因可显式检查，取消只影响指定的 active child。
- child runtime 只向基础 chat model 发出一次 tool-free `Generate` 请求，输入由创建时冻结的 session reference 与一个只读边界 system message 组成；它不能编辑 workspace、调用 shell、请求 approval/escalation，或继续派生 agent。
- 完成结果从 parent session、ledger、usage 和后续 model prompt 完全隔离，只作为 TUI display-only 内容。最多保留 16 个任务，每个结果上限 64 KiB，并进行终端控制字节清理。
- child 记录保留在 TUI 进程内，因此 `/new`、`/resume` 和 `/fork` 后仍可用 `/agents show` 查看已完成结果；为避免跨 session 污染，源 session 已不再 active 时不会在当前页输出完成通知。退出 TUI 会请求取消所有 active child。
- `/tasks` 增加已启用 runtime 的 active/retained child 摘要；slash catalog、自动补全和 busy dispatcher 都识别新的命令。

## 边界与后续

这是可监督的后台分析子集，不是 durable 或 write-capable worker。它没有跨进程恢复、父 agent 自动委派或结果自动合流，也不会创建 worktree、复制工具权限或执行工具。后续若要扩展到编码 worker，必须单独设计 worktree 隔离、显式 approval、资源预算、持久化和冲突/结果合流协议，不能将当前只读 child 的权限无声放大。

## 验证

- 新增 TUI 测试覆盖启动/完成、busy foreground 并行、单任务取消、并发上限、控制字符清理、64 KiB 结果截断、session 切换后的静默通知、命令用法和 `/tasks` 摘要。
- 新增 runtime 测试确认参考上下文冻结、模型调用无 tool schema、child 不能改变 parent transcript、usage 或 durable thread，并覆盖 runtime 输入/模型/空结果验证。
- 更新 slash 解析、slash 菜单补全、busy command 分类和 README 能力边界。
- 提交前运行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
