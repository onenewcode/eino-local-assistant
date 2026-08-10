# CLI MCP list

本轮开始补齐主流 code agent 的 MCP 管理命令面，先落地独立、只读的 `mcp list` 子集。参考本机 Codex CLI 0.146.0 的 `mcp list --json` 自动化入口和 Claude Code 2.1.220 的 `mcp list` 命令拓扑；后续 add/get/remove 将拆成其他提交。

## 行为

- `mcp list` 使用用户级 `config.toml` 的严格 TOML 加载和校验，按 `mcp.servers` 配置顺序列出 stdio server。
- 文本表展示 name、transport、command、quoted args、env keys 与 working directory；空字段显示 `-`。
- `mcp list --json` 输出数组，每项包含 `name`、`enabled` 和 `transport`；transport 当前为 `stdio`，包含 command、始终为数组的 args、可选 working_dir 与排序后的 env_vars。
- 环境变量只输出 key，永不输出配置中的 value，避免 `mcp list` 或日志泄露 token。
- 该命令不启动 MCP 子进程、不调用 `tools/list`、不检查 executable 或网络健康。配置 command 即使当前不可执行也能被静态列出。
- 无配置时文本输出 `No configured MCP servers.`，JSON 输出 `[]`。多余位置参数在读取配置前拒绝。

## 参考边界

Codex 当前 JSON 同时覆盖 stdio 与 streamable HTTP，并包含 auth/status/timeout；本仓库目前只支持显式 TOML stdio，因此不伪造未实现的 HTTP、OAuth 或 health 字段。Claude 的 list 会对获批 server 做健康检查，本轮刻意保持静态无副作用，避免“列配置”意外执行任意本地 command。

官方 Codex manual helper 本轮仍因 developers.openai.com 返回 HTTP 403 无法刷新，已安装的 Docs MCP 在当前会话也未 ready；因此精确命令契约以本机上述两个 CLI 的实时 `--help` 和 Codex `mcp list --json` 输出为可复验证据。

## 验证

- CLI 回归覆盖 root/mcp/list help、文本表、参数 quoting、配置顺序、无配置文本与 JSON、额外参数拒绝。
- JSON 回归覆盖 Codex-style name/enabled/transport shape、空 args 编码为 `[]`、env keys 排序与 env value 不泄露。
- 使用不存在的 command 验证 list 不做 executable 探测或 server 启动。
- 按仓库门槛运行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
