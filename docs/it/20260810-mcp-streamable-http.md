# Streamable HTTP MCP

本轮把 MCP 能力从仅本地 stdio 扩展到远程 Streamable HTTP 的一个可用闭环。命令模型对齐本机可观察的 Codex CLI 0.146.0：`mcp add` 在本地命令和远程 `--url` 之间显式分流；Claude Code 2.1.220 同样把 HTTP transport 与 stdio 命令分开。调研依据与协议生命周期边界记录在 `docs/research/mcp-remote-transport-research.md`。

## 行为

- `[[mcp.servers]]` 支持 `type = "streamable_http"` 与 `url = "https://..."`。未填写 `type` 的现有配置仍按 stdio 解析，保证兼容。
- `eino mcp add <name> --url <url>` 只原子写入用户级 TOML，不做 DNS、HTTP、OAuth 或 `tools/list` 请求；成功只表示配置被保存。
- `eino mcp list` / `eino mcp get <name>` 为远程 server 展示 `transport: streamable_http` 和 URL。JSON transport 是类型分支：remote 项只含 `type` 与 `url`，不会伪造 command、args 或 env fields。
- TUI 与 `exec` 将启用的远程配置交给 SDK 的 Streamable HTTP transport，沿用现有 `connect_timeout_seconds` 作为初始化和工具发现 deadline；发现后的调用仍使用调用上下文控制取消。
- `MCPToolSet.Close()` 对远程 session 调用 SDK close，协议 session server 分配时会执行 HTTP `DELETE`；与 stdio transport 共用同一 tool namespace、过滤与 MCP approval bridge。

## 安全与边界

- 远程 URL 必须是绝对 `http` 或 `https`，并拒绝 user info、query 和 fragment。这样不把 URL 当作静态凭据载体，也避免 `list` / `get` 回显 token。
- `streamable_http` server 不能同时携带 command、args、working_dir 或 env；stdio server 不能携带 URL。未知 transport 和两种配置混用均在严格 TOML load 时失败。
- 本轮刻意不支持静态 headers、bearer token、OAuth 登录/登出、token store、health/debug 或 reconnect 策略配置。受保护 endpoint 会在 runtime 连接时明确失败，不能通过配置字段绕过认证与 secret 管理设计。
- `http` 被保留给 loopback、测试和受控内部部署；使用公网 server 时应选择 HTTPS。远程 MCP tool call 继续属于现有审批/plan/yolo policy 边界，不因 transport 改变权限。

## 验证

- config 回归覆盖 stdio 向后兼容、Streamable HTTP 有效配置、缺 URL、错误 transport、混用 process fields、非 HTTP scheme、URL user info 和 query 的拒绝。
- CLI 回归覆盖 `--url` 持久化、只读 list/get 的 remote 投影、remote JSON 不包含 stdio fields，以及 `--url` 与 `--env`/command 的互斥。
- tool 集成回归使用 SDK `NewStreamableHTTPHandler` 的 `httptest.Server` 完成连接、`tools/list`、echo 调用和 session `DELETE` 关闭，并保留 stdio 与 approval 回归。
- 本提交按仓库门槛运行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
