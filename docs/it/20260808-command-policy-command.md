# 迭代：命令策略可观测命令

日期：2026-08-08

## 背景

状态栏只显示命令策略数量，`/permissions` 管理的是用户记住的工具授权，两者不能替代彼此。主流 code agent 会让用户能检查当前有效的规则来源和决策，避免策略配置变成不可见的安全假设。

## 实现

- 新增只读 `/policy` 命令，展示每条规则的 ID、`allow`/`ask`/`deny` 决策和 argv 前缀。
- `/policy` 与 `/rules`、`/status` 等诊断命令一样，在 busy 或审批等待期间即时执行，不进入 follow-up 队列。
- `/permissions` 的记住授权管理语义保持不变；`/policy` 不修改配置、不写持久化规则，也不回显实际命令输出。
- 规则摘要从 CLI 配置传入 TUI，显示内容与实际用于构造 `run_command` 的规则保持同源。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
