# MCP OAuth Login, Runtime Use, and Logout

本轮将之前的 keyring 凭据基础层接入显式的 `eino mcp login <name>` 与 `eino mcp logout <name>`，并让已登录 remote MCP 能在后续 TUI 与 `exec` runtime 中实际使用保存的 access token。

## 行为

- `mcp login` 只接受未配置 `bearer_token_env_var` 的 Streamable HTTP server。它启动随机端口的 `127.0.0.1` callback、发现 protected resource 与 authorization-server metadata、用 dynamic client registration 建立 public client、通过 SDK 发起 authorization-code + PKCE S256 流程，并由 SDK 校验 state、resource indicator 和 provider 宣告时的 issuer。
- 登录时始终显示授权 URL；默认也会尝试打开浏览器。`--no-browser` 支持无图形和远程终端手工打开 URL，`--timeout`（默认 5 分钟）及 Ctrl-C 取消等待。authorization code、callback query、token 与 client secret 不会写入配置、日志或 CLI 成功输出。
- 成功 token 只写入系统 keyring，并绑定 server 名和精确 endpoint；随后才原子设置 `oauth = true`。配置在登录期间改变或更新失败时，会尽力删除刚保存的 credential，避免将 token 留在错误 endpoint 上。
- OAuth-enabled runtime 从 keyring 读取 token，并对所有 Streamable HTTP 请求使用 bearer header。缺失、endpoint mismatch、过期或 401/403 都只提示 `eino mcp login <name>`，不会在 TUI 或 `exec` 的后台网络请求中启动浏览器。
- `mcp logout` 删除本地 token 并设置 `oauth = false`；没有 token 时仍会安全地禁用该标记。它不声称已经调用 provider-side revoke。

## 兼容与边界

- `mcp list`、`mcp get` 和 JSON view 只展示 `oauth: true` / `oauth: enabled` 的静态状态，绝不显示 token。OAuth 与 `bearer_token_env_var` 仍然互斥。
- 使用 SDK 的 DCR 支持 generic MCP OAuth server；需要预注册 client、持久化 DCR identity、refresh-token rotation 或 provider-side revoke 的服务仍是后续迭代，不会被标记为已支持。
- 所有 OAuth HTTP client 禁止自动 redirect，避免把后续带认证头的 MCP 请求隐式发送到另一 endpoint。

## 验证

- OAuth 端到端单测用 Streamable HTTP MCP 与测试 authorization server 覆盖 protected-resource/authorization metadata、DCR、PKCE S256、resource indicator、loopback callback、token exchange 与重试后的 MCP initialization。
- CLI 回归覆盖登录的 keyring 写入、配置开关、token redaction、配置更新失败时的 cleanup、logout 幂等性、transport/bearer 拒绝及命令帮助。
- runtime 回归覆盖 keyring token 的工具发现、未登录/过期凭据与 401 的显式 re-login 错误，以及 OAuth/bearer client 的 redirect 拒绝。
- 交付前执行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
