# MCP 运行时注册

本轮把已存在的 stdio MCP 连接层接入正常 runtime，使配置不再只能被静态列出。

## 行为

- TUI 与 `exec` 在创建 runtime 时，启动所有 `enabled = true` 或未设置 `enabled` 的 `[[mcp.servers]]`，完成 `tools/list` 后将工具注册为 `mcp__<server>__<tool>`；禁用的 server 不启动也不占用模型上下文。
- 每个启用 server 的连接和发现使用独立 deadline：`connect_timeout_seconds` 省略时为 15 秒，显式配置范围为 1-60 秒。连接、发现、schema 解码或命名冲突失败会终止本次 runtime 启动，不以缺失工具静默降级。
- 运行时持有 `MCPToolSet`；退出时关闭 MCP session/子进程，避免 TUI 或一次性 `exec` 留下外部 server。
- MCP 工具调用接入当前 `ApprovalState`：`ask` 需 approver，`plan` 与已拒绝的 session rule 直接拒绝，`auto`/YOLO 允许；`allow for session` 使用 `mcp:<server>:<tool>` 精确缓存。没有 approver 的 ask 路径 fail-closed。
- `eino mcp list` 仍是无副作用的静态查看，不启动进程；其 `enabled` 字段现在如实显示配置值，环境变量仍只显示名称。

## UX 与边界

这是一条 Codex/Claude 风格“配置与实际可用工具一致”的最小闭环：用户可先用 `mcp list` 审阅配置，再在正常 TUI 或 `exec` 生命周期中使用发现出的工具。启用状态、超时和工具命名都可预测；外部工具仍须经过运行中的审批状态，不能因为已在配置中而获得 shell/apply_patch 以外的隐式授权。

本轮仅支持显式配置的本地 stdio server；不新增 HTTP transport、OAuth、重连或健康检查命令。相关行业证据和未覆盖的边界见 `docs/research/mcp-runtime-lifecycle-research.md`。

## 验证

- 增加配置默认值/超时校验、禁用 server 过滤、静态 `mcp list` enabled 展示测试。
- 增加 MCP 调用在无 approver 时拒绝、session allow 缓存，以及共享 `ApprovalState` 覆盖静态模式的测试。
- 执行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
