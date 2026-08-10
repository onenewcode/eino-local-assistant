# 迭代：TUI `/plan` 运行时切换

日期：2026-08-08

## 背景

上一轮加入了配置级 `permission_mode: plan`，但用户必须重启会话才能进入规划模式。主流 code agent 通常允许在交互过程中显式切入 plan，且切换必须影响真正的工具权限，而不是只改状态栏文字。

## 实现

- 新增 TUI `/plan` 斜杠命令，并加入补全菜单和 `/help`。
- 只允许在 idle 时切换；忙碌 turn、审批或 compaction 期间不会改变权限。
- 新增可切换 permission handler：进入 plan 后，后续 permission-gated 的命令、编辑、恢复和 MCP 调用在 handler 入口直接拒绝。
- 原有配置的风险策略和确认 broker仍作为正常模式 delegate；切入 plan 不会弹出新的审批请求。
- 状态栏与 `/status` 立即显示 `approval=plan risk=deny`，并在 transcript 中记录切换提示。
- 该命令是单向进入模式；退出 plan 可重启 session 使用原配置，避免隐式放宽权限。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
