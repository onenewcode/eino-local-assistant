# Iteration 20260808: permission rule expiry

## Goal

避免长期不使用的 `a` 授权永久扩大工具权限，为持久化规则增加明确的生命周期边界。

## Changes

- `PermissionRule` 增加 `expires_at`。
- 新记住规则默认 30 天有效；配置 `tools.permission_rule_ttl_hours` 可调整，`0` 使用默认值。
- 启动加载和 `/permissions` 列表会忽略过期规则；命中请求也不会使用过期授权。
- 启动加载发现过期条目时会将有效条目原子写回，清理磁盘上的失效规则。
- 旧的无 `expires_at` 规则继续兼容加载，后续显式重新授权时获得 TTL。
- 增加过期持久化规则和短 TTL 测试。

## Verification

- `go test ./...`
- `go build ./...`
- `go tool golangci-lint run ./...`

## Follow-up

后续可增加按项目/工具单独 TTL 和显式刷新授权命令。
