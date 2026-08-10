# 迭代：命令前缀权限策略

日期：2026-08-08

## 背景

仅靠全局 `unrestricted`、`confirm` 或 `deny` 无法表达主流 code agent 常见的“允许高频只读命令、询问发布命令、拒绝明确危险命令”。Codex 的 prefix rule 与 Claude Code 的 Bash matcher 都把单条工具规则置于全局权限模式之上，并采用更严格结果优先的决策。

## 实现

- `tools.run_command.policy` 支持 `id`、`decision`（`allow`/`ask`/`deny`）和 `prefix` argv 列表。
- 多个匹配规则使用 `deny > ask > allow`；策略拒绝先于命令启动和全局权限 handler。
- `ask` 仍受全局 `confirm`/TUI broker 控制；非交互 `exec` 没有审批 handler 时拒绝该命令。
- 只匹配词法有效、无 shell 运算符的单条命令；管道、重定向、命令替换和复合脚本不会获得前缀放行。
- 状态栏与 `/status` 增加 `policy=<规则数>`，让当前策略是否生效可见。
- 配置映射同时接入 TUI 和 `exec` 两条入口；策略不改变 `workspace_only` 的语义，也不冒充 OS sandbox。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
