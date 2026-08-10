# MCP bearer token environment variables

本轮补齐远程 Streamable HTTP MCP 的 bearer token 环境变量入口。Codex CLI 0.146.0 的 `mcp add --help` 将 `--bearer-token-env-var` 明确限制在 Streamable HTTP server；相关产品证据、OAuth 与 header 的后续边界已记录于 `docs/research/mcp-remote-transport-research.md`。

## 行为

- `eino mcp add <name> --url <url> --bearer-token-env-var <NAME>` 将 `<NAME>` 持久化为远程 server 的 `bearer_token_env_var`，而不写入 token value。
- 启动 TUI 或 `exec` 时，远程 transport 读取变量当前值；变量不存在或为空会在该 server 连接前失败，错误只包含变量名，不包含或派生 token value。
- 当变量有效时，Streamable HTTP 的初始化、tool discovery、tool call 与 session `DELETE` 均使用 `Authorization: Bearer <token>`。现有 MCP tool approval、session allow/deny、plan 与 yolo 语义不变。
- `mcp list` / `mcp get` 的 remote projection 只显示 `bearer_token_env_var` 的名字；没有 token 字段。未配置 bearer 时 remote JSON 仍保持仅 `type`/`url` 的紧凑形状。

## 安全边界

- bearer token 只允许远程 Streamable HTTP；stdio 配置与 `mcp add` 的 stdio form 都拒绝该 flag。变量名须符合常规环境变量格式。
- 用专用 HTTP client 在 transport 层为每个请求注入 header，复制 request/header 后再修改，避免复用或污染调用方请求。
- 启用 bearer 时 HTTP client 不跟随 redirect。否则重定向后的新 request 也会被 transport 注入 token，可能把凭据发送给另一端点；调用者应直接配置最终 MCP endpoint。
- 本轮仍不支持任意静态 header、OAuth login/logout、token store、token refresh 或 endpoint health/debug；这些需要独立的持久化、交互和撤销设计。

## 验证

- config 回归覆盖 bearer 变量的 TOML load、stdio 拒绝、无效变量名拒绝和远程 config 兼容。
- CLI 回归覆盖 `--bearer-token-env-var` 写入、list/get 只显示变量名，以及与 stdio、空值和非法变量名的互斥错误。
- 远程 MCP 集成测试验证初始化、discovery、echo 调用和 session `DELETE` 都带 bearer header；额外 HTTP 测试验证 bearer client 停在 307 响应且不请求 redirect target。
- 本提交按仓库门槛运行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
