# CLI Doctor

本轮恢复 `eino doctor`，提供主流 Code Agent 常见的本地启动前诊断入口，并按当前的 Streamable HTTP MCP、OAuth、sandbox 与 approval 模型定义明确边界。

## 行为

- 严格加载并验证 TOML 配置，报告 model identity（脱敏 endpoint）、解析后的 workspace、session storage 状态，以及有效的 approval/sandbox 摘要；不回显 API key、URL 用户信息、query 或 fragment。
- 检查已启用 stdio MCP command 能否由当前 PATH 解析。远程 MCP 只报告静态 transport 与认证配置：OAuth 不读取 keyring 或联网，bearer 配置只确认环境变量是否存在且非空，不输出 token。
- storage 尚未创建时输出 `pending`，由首次 durable session 创建；doctor 不创建 storage、session、投影、用户 tool-policy 或项目规则。
- 不调用模型、OAuth metadata、远程 MCP endpoint，也不会启动本地 MCP process。静态前置条件失败时返回明确错误。

## 验证

- CLI 测试覆盖 root/help、脱敏 model endpoint、OAuth 不读 credential、缺失 bearer 环境变量和无效配置；不需要外部网络或 MCP subprocess。
- 交付前执行 `git diff --check`、`go test ./...`、`go build ./...` 与 `go tool golangci-lint run ./...`。
