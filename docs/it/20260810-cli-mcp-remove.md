# CLI MCP remove

本轮补齐 MCP 配置的显式删除命令：`eino mcp remove <name>`。

## 行为

- 命令从唯一的用户级 `config.toml` 删除指定的 `[[mcp.servers]]`，在写入前后均使用正常严格 TOML 校验。空名称、额外位置参数和未知名称会失败，未知名称不会改写文件。
- 更新只移除目标 server 的 array-table 区块及其子表（包括 `mcp.servers.env`）；无关配置和前置用户注释保留。删除最后一个 server 后保留合法的 `[mcp]` 和其他配置。
- 文件必须是普通 `.toml` 文件。符号链接、非普通文件和包含 multiline TOML string 的文件被拒绝；后者避免行扫描把字符串内的 table token 误认作删除边界。成功更新继承原权限，经临时文件 `fsync`、原子 rename 和目录同步完成，避免中途写坏用户配置。
- 命令是用户显式发起的配置写入，不启动 MCP 子进程、不调用 `tools/list` 或 health check；也不会卸载已经由另一个运行中 TUI/`exec` 进程连接的 server。删除配置与 future session 的运行时注册相关，凭据注销属于未实现的独立流程。

## 对齐与边界

Codex CLI 和 Claude Code CLI 都提供 `<name>` 形式的 MCP remove。Claude 额外处理多 scope；Eino 当前只有用户级配置，因此不伪造 local/project scope 选择。OpenCode 的 `enabled = false` 是可逆禁用，不能替代配置删除；相关产品边界见 `docs/research/mcp-configuration-removal-research.md`。

本轮没有添加 `mcp add`、HTTP/OAuth、login/logout、配置并发锁或对运行中 MCP session 的跨进程控制。

## 验证

- 增加配置层的首项/末项删除、子表移除、无关配置与注释保持、未知名称不写入、符号链接及 multiline string 拒绝测试。
- 增加 CLI 删除成功输出、不会启动不存在 command、帮助和非法参数测试。
- 执行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
