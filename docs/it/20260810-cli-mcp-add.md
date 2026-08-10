# CLI MCP add

本轮新增受限的本地 stdio MCP 添加命令：`eino mcp add <name> [--env KEY=VALUE]... -- <command> [args...]`。

## 行为

- `<name>` 后必须使用 `--` 显式分隔 Eino flags 与待保存的子进程 command；`--` 后的 command/args 原样写入配置，但 `add` 绝不启动 command、调用 `tools/list` 或做 health check。
- 支持重复 `-e`/`--env KEY=VALUE`。变量名限制为常规环境变量形式（字母或 `_` 开头，后续可含数字），重复 key、缺少 `=` 和无效 key 会失败。空 value 和 value 内的后续 `=` 保留。
- 成功时将 name、command、args 和 env 写入新的 `[[mcp.servers]]`，默认不显式写 `enabled` 或 timeout，因而沿用运行时的 enabled/default 15 秒 discovery deadline。env key 会稳定排序；成功提示和 `mcp list`/`get` 只显示 key，绝不回显 value。
- 添加前校验完整既有 TOML、新 server 字段和重名；文件必须是现存的普通 `.toml`，拒绝符号链接/非普通文件。更新通过权限继承的临时文件、`fsync`、原子 rename 与目录同步完成。无效 UTF-8 或不安全控制字符会在写入前拒绝。

## 边界

这一小步只覆盖 Eino 已支持的本地 stdio transport。没有伪造 Codex/Claude 的 URL、HTTP/SSE、OAuth、`--scope`、working directory 或 timeout add flags；需要这些字段时仍可编辑用户级 TOML。`--env` 的 value 会写入用户配置（安装器新建时为 `0600`，更新会保留现有权限），也可能进入 shell history，短期敏感 token 不应直接作为命令行文本传入。

Codex、Claude Code 与 OpenCode 对 command 分隔、environment 和 transport 的可观察实践见 `docs/research/mcp-configuration-addition-research.md`。

## 验证

- 增加配置层的追加、TOML escaping、env 排序、默认值、重名/不安全 value/符号链接拒绝测试。
- 增加 CLI 的 `--` 语义、env 脱敏、不会执行不存在 command、帮助与非法参数测试。
- 执行 `go test ./...`、`go build ./...`、`go tool golangci-lint run ./...` 与 `git diff --check`。
