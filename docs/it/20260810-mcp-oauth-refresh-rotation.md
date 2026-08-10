# MCP OAuth Refresh and Token Rotation

本轮完成 remote MCP OAuth access token 的安全刷新路径，使显式登录获得的 DCR client identity、refresh token 和后续 rotation 可以跨 runtime 使用，而不会让 TUI、`exec` 或后台连接触发浏览器。

## 行为

- `mcp login` 在 dynamic client registration 中声明 `authorization_code` 与 `refresh_token` grant，并请求 refresh token。成功后仅在系统 keyring 保存 endpoint-bound access/refresh token、DCR client ID、可选 client secret、token endpoint 和 client authentication style；TOML、CLI 输出、日志和 auth-status 输出不包含这些值。
- Streamable HTTP runtime 从 keyring 加载 credential。access token 过期时使用保存的 OAuth client profile 刷新；刷新响应中的 access token 与 refresh token（包括 rotation）必须先完整写回同一 keyring credential，写入成功后才可以发送 MCP 请求。
- keyring 写回失败会 fail closed：runtime 返回错误，不会使用刚拿到、可能已替换旧 token 的 refresh 响应，也不会把 bearer header 发给 MCP endpoint。OAuth token 与 MCP HTTP 请求都继续禁止自动 redirect。
- 旧 version 1 keyring entry 与未返回 refresh token 的登录在 access token 尚有效时保持兼容；过期后明确提示 `eino mcp login <name>`。缺失、endpoint mismatch、结构无效的 credential 也走相同的显式恢复路径，不会后台登录。
- `mcp auth list|get` 保持纯本地、redacted inspection；`expired` 只描述已保存 access token，不能判断 provider 是否仍接受 refresh token，命令不会自行联网刷新。

## 验证

- keyring 测试覆盖 version 2 credential 的 round trip、深拷贝和 version 1 兼容；refresh 单元测试覆盖 form/client authentication、refresh-token rotation、缓存、无 refresh context、无 writer、无效 profile 与 keyring 写回失败。
- MCP runtime 集成测试确认过期 token 会先向 token endpoint 刷新、只用新 bearer 发现工具、持久化 rotation；写回失败时 token endpoint 可被调用但 MCP endpoint 请求数保持为零。
- CLI 登录测试确认 refresh profile 保存到 credential store，且 access token、refresh token 和 client secret 都不会写到 stdout/stderr。
- 交付前执行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。

## 边界

- 只支持本客户端通过当前 DCR 流程获得并安全保存的 refresh context；不增加预注册 OAuth client 配置、provider-side revoke、token introspection 或远端 health/debug。
- 依赖操作系统 keyring；不可用时绝不降级到普通文件。provider 拒绝已刷新或仍有效的 token 时依然要求用户显式重新登录。
