# 迭代：TUI `/plan off` 权限恢复

日期：2026-08-08

## 背景

运行时 plan 切换已经可以保护后续工具调用，但单向进入会迫使用户重启 TUI 才能回到原来的 `confirm` 或 `unrestricted` 模式。主流交互式 agent 通常要求退出保护模式也必须显式操作，并恢复原始会话策略。

## 实现

- `/plan` 仍然进入只读规划模式，`/plan off` 显式退出。
- 退出时恢复 handler 创建时的正常 permission mode 和 high-risk policy；不会默认恢复为 unrestricted。
- 从配置以 `permission_mode: plan` 启动的 session 没有可隐式放宽的 normal mode，`/plan off` 会明确拒绝。
- 进入和退出都只允许在 idle 时执行，避免改变正在执行或审批中的 tool。
- 更新 slash 补全、帮助、状态提示和回归测试。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
