# Iteration 20260808: revoke one permission rule

## Goal

在查看和全部清空之外支持精确撤销单条 session permission rule，避免用户为了收回一个授权而丢失其他仍需要的授权。

## Changes

- 新增 `PermissionBroker.RevokeRule(index)`，编号与 `/permissions` 的稳定排序一致。
- 新增 `/permissions revoke <n>`，非法编号返回明确错误。
- 该命令在 turn 运行时仍作为即时本地命令执行。
- 增加规则 API 边界测试，并更新帮助和 README。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

后续仍需考虑跨会话授权持久化、规则过期和更细粒度的命令风险分类。
