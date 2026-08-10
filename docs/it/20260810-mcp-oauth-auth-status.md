# MCP OAuth Redacted Auth Status

本轮补齐 MCP OAuth 的本地状态查看面，采用 OpenCode 文档化的独立 auth-status 思路，但不将它伪装成远程 health check 或 credential dump。

## 行为

- `eino mcp auth list` 枚举 `oauth = true` 的 Streamable HTTP server；`eino mcp auth get <name>` 查看一个 remote server。两个命令均提供 `--json`。
- 状态只来自用户配置与系统 keyring：`available`、`expired`、`missing`、`endpoint_mismatch`、`invalid`、`keyring_unavailable`；未启用 OAuth 的 remote `get` 返回 `not_configured`。
- 输出可显示标准 UTC expiry timestamp，但不会输出 access token、refresh token、authorization code、client identity、client secret 或 keyring 错误详情。
- 命令不创建 MCP session、不访问 OAuth metadata、不进行 token refresh、不调用远端 server，也不会打开浏览器。`available` 只表示本地的非过期 token 与 endpoint binding 存在，不能证明远端当前一定接受该 token。

## 验证

- CLI 测试覆盖所有本地 keyring 状态、JSON/text redaction、非 OAuth/stdio/missing server 错误以及 command help 和参数拒绝。
- 交付前执行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。

## 边界

- 这不增加 provider-side token introspection、health check、automatic refresh 或 revoke；这些需要独立的网络与 token-rotation 生命周期。
