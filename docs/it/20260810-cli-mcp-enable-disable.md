# CLI MCP enable / disable

本轮新增可逆的 MCP 配置启停命令：`eino mcp enable <name>` 与 `eino mcp disable <name>`。

## 行为

- 状态转换会在用户级 `config.toml` 中将目标 `[[mcp.servers]]` 写为明确的 `enabled = true` 或 `enabled = false`。此前省略该字段的 server 默认启用；disable 会在 env 子表之前插入设置，enable 会替换已有设置并保留行尾注释。对省略 enabled 的 server 执行 enable 已是目标状态，因此不写入。
- 状态已是目标值时命令幂等成功且不重写文件。未知名称、空/多余参数、符号链接、非普通文件和包含 multiline TOML string 的配置均明确失败；后两类不会冒险执行行级修改。
- 写入使用既有严格 TOML 前后校验、权限继承临时文件、`fsync`、原子 rename 与目录同步。除 enabled 字段外保留 command、args、env、其他 server 和无关配置。
- 这是 future runtime 的静态配置变更：不启动 server、不调用 `tools/list`、不健康检查，也不会停止或修改另一个已经运行的 TUI/`exec` MCP session。新的 runtime 会按更新后的 enabled 状态决定是否发现该 server。

## 对齐与边界

OpenCode 已文档化 `enabled: false` 作为保留配置的临时禁用；Codex 当前 JSON 可见 enabled status，Claude Code 当前可见 project approval/pending 状态，但本机版本没有直接等价的 server enable/disable 子命令。Eino 将“静态启停”“运行时连接”和“删除配置”明确分开，避免把配置写入说成实时 revoke 或进程终止。详见 `docs/research/mcp-server-enablement-research.md`。

本轮不新增 tool filter、HTTP/OAuth、跨进程 session 停止、自动重连或 health check。

## 验证

- 增加配置层的缺省/显式 enabled 切换、inline comment 保留、幂等不重写、未知名称、符号链接与 multiline TOML 拒绝测试。
- 增加 CLI 的 enable/disable 成功输出、静态列表状态、命令帮助和非法参数测试。
- 执行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
