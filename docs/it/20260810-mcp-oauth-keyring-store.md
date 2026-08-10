# MCP OAuth Keyring Credential Foundation

本轮为后续 `mcp login` / `mcp logout` 建立了最小的安全凭据边界，而没有将 access token、refresh token 或客户端注册信息写入用户 TOML 配置。

## 变更

- 新增 `internal/mcpoauth.Store`，只使用操作系统 keyring 保存 OAuth token；keyring 不可用时明确失败，绝不退回普通文件。
- keyring 条目以 MCP server name 的 SHA-256 摘要作为键，并在加密 secret 内保存精确 endpoint binding。服务器重命名复用到不同 URL 时，旧 token 会被拒绝，不能发送到新 endpoint。
- `MCPServerConfig` 新增 remote-only 的 `oauth` 标记，并拒绝与 `bearer_token_env_var` 共存。该标记只允许后续 runtime 从 keyring 读取凭据，当前尚不触发浏览器、token refresh 或自动 401 登录。
- `SetMCPOAuthEnabled` 采用原子写入并保留无关 TOML 内容；符号链接和多行 TOML 配置均拒绝修改，以避免破坏用户配置。

## 验证

- 单元测试覆盖 keyring 读写/删除、endpoint binding、无效输入、OAuth 配置校验、持久化更新、幂等性和拒绝路径。
- 交付前执行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。

## 边界

- 这不是 OAuth 登录完成声明：尚未提供 `mcp login`、`mcp logout`、动态注册、PKCE callback、refresh、revoke 或 runtime token 注入。
- token 值仍不会进入 `eino mcp list/get`、TOML、日志或 session journal。
