# 迭代：审批等待期间的 Ctrl+C 中断

日期：2026-08-08

## 背景

TUI 在等待工具权限审批时会优先交给审批按键处理器。该处理器对未识别按键统一返回已处理，导致 `Ctrl+C` 无法继续进入全局中断分支。用户看到帮助文案中的 `ctrl+c interrupt`，但实际无法取消正在等待审批的 turn。

## 实现

- `internal/tui/model.go` 将 `Ctrl+C` 从审批按键处理器中放行。
- 全局按键处理器继续调用 `interruptTurn`，取消当前 turn 并清空待处理的权限请求。
- `internal/tui/permission_test.go` 增加审批队列非空时的 `Ctrl+C` 回归测试。
- `Esc` 的语义保持不变：只拒绝当前审批；审批等待期间的全局中断使用 `Ctrl+C`。

## 验证

```sh
go test ./...
go build ./...
go tool golangci-lint run ./...
```
