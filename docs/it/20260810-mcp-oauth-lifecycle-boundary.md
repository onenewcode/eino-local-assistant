# MCP OAuth lifecycle boundary

本轮没有把 `mcp login` 伪装成一个只打开浏览器的命令，而是先完成了 OAuth 生命周期调研并固化交付边界，详见 `docs/research/mcp-oauth-lifecycle-research.md`。当前已经可用的 `bearer_token_env_var` 是环境变量凭据机制，不是 OAuth 登录，也不会声明具有 refresh、logout 或 token persistence 能力。

## 结论

- 合格的 OAuth MCP 迭代必须一次性覆盖 metadata discovery、resource indicator、客户端注册或预注册配置、PKCE/state/issuer callback 校验、access/refresh token 的安全持久化与轮换、runtime refresh、redacted status 和 logout/revoke。
- `mcp login <name>` 只能在上述凭据生命周期具备后提供；否则成功退出的 CLI 无法证明 token 被安全保存、下一次运行可恢复，或用户能撤销它。
- 无图形环境和非交互 `exec` 必须有明确、可取消的失败或手工打开 URL 行为，不能让普通 runtime 在收到 401 后无限等待浏览器回调。
- 主流产品已将 bearer env、headers、OAuth、login/logout 和 auth status 分层。后续实现会保持这些状态独立，不把 token 值、authorization code、callback query、client secret 或 refresh token 放入配置、日志、session journal 或 `mcp list/get`。

## 当前边界

- 保留已交付的 `mcp add --url`、remote Streamable HTTP runtime 与 `--bearer-token-env-var`；它们完整通过配置、工具集成和重定向泄露防护测试。
- 尚未新增 `mcp login` / `mcp logout`，也没有自动 401 OAuth 浏览器流、静态 header、client registration、keychain/token store、refresh 或 revoke。
- 这份记录是后续 OAuth 实现的验收清单，而不是把“浏览器跳转可用”误记为认证能力完成。

## 验证

- 本文档记录通过本机 Codex CLI 0.146.0 与 Claude Code 2.1.220 的可观察命令面、OpenCode 当前 MCP 文档、MCP 2025-06-18 authorization specification 和 RFC 9700 交叉核对。
- 文档提交仍按仓库门槛运行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`，确保现有远程 MCP 与 bearer 行为未回归。
