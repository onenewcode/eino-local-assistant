# Iteration 20260808: permission rule inspection

## Goal

让会话级授权记忆可见、可撤销，避免用户不知道 `a` 已经放行了哪些精确请求，也避免只能重启进程才能清理规则。

## Changes

- 新增 `/permissions`，按稳定顺序列出当前 TUI 会话的 `tool/action/detail` 规则。
- 新增 `/permissions clear`，立即撤销全部记忆规则。
- `/permissions` 在 turn 运行时也可立即执行，不会进入 follow-up 队列。
- 无 `confirm` broker 时给出明确提示，不伪造规则列表。
- 增加 broker 规则查询/清空 API，并保留规则只存在内存的边界。
- 更新帮助文本、README 和 slash 命令目录。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

后续可继续实现按条目撤销、授权规则持久化和跨会话策略导入；持久化前需要明确用户授权范围和配置格式。
